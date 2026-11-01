package core

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// receiveAt delivers a message sent at a given time; live false stores it as history.
func (h *harness) receiveAt(chat, text string, fromMe bool, at time.Time, live bool) model.Message {
	h.n++
	m := model.Message{Chat: chat, ID: "H" + strings.Repeat("x", h.n), Sender: "200@s.whatsapp.net", SenderName: "Sam",
		Time: at.UTC(), FromMe: fromMe, Text: text}
	h.core.OnUpdate(model.Update{Kind: model.New, Message: m}, live)
	return m
}

// quiet checks that p gets no events for a short while.
func quiet(t *testing.T, p *fakePusher) {
	t.Helper()
	select {
	case got := <-p.events:
		t.Fatalf("pushed %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// stored waits until a sent message is in the store.
func (h *harness) stored(chat, id string) {
	h.t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if _, found, _ := h.st.Message(h.ctx, chat, id); found {
			return
		}
	}
	h.t.Fatalf("message %s was not stored", id)
}

func texts(msgs []model.Message) string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Text
	}
	return strings.Join(out, "|")
}

func TestWindowModeWithChatAgent(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	_, _ = h.core.Run(ctx, Owner, "/name Bob")
	h.receiveAt("g@g.us", "from yesterday", false, time.Now().Add(-24*time.Hour), false)
	h.receive("g@g.us", "said before", false)
	h.receive("g@g.us", "Bob, someone else may not open it", false)
	if _, open, _ := h.st.Window(ctx, "g@g.us"); open {
		t.Fatal("a message from someone else opened a window")
	}

	// Naming the agents is not enough to open a window; a wake phrase is.
	h.receive("g@g.us", "Bob, what do you think?", true)
	if _, open, _ := h.st.Window(ctx, "g@g.us"); open {
		t.Fatal("the name alone opened a window")
	}

	// The window opens, says so, and the chat's agent gets the message.
	h.receive("g@g.us", "bob wake up, what do you think?", true)
	start := h.expect("Now monitoring this chat until")
	if start.chat != "g@g.us" || !strings.HasPrefix(start.text, "🤖 *Bob*: Now monitoring") ||
		!strings.Contains(start.text, "the last 30 minutes") || !strings.Contains(start.text, "extend it by 1 hour") {
		t.Errorf("start message = %+v", start)
	}
	agent := h.agent("chat-team")
	if got := events(t, agent.fakePusher); len(got) != 1 || got[0].Message.Text != "bob wake up, what do you think?" {
		t.Fatalf("the agent got %+v", got)
	}
	if agent.host.Chat != "g@g.us" || agent.host.ChatName != "Team" || agent.host.Name != "Bob" {
		t.Errorf("agent host = %+v", agent.host)
	}
	me := Caller{Session: "chat-team"}
	h.stored("g@g.us", start.id)
	read, err := h.core.ReadChat(ctx, me, "g@g.us", 0)
	if err != nil || texts(read.Messages) != "said before|Bob, someone else may not open it|Bob, what do you think?|"+
		"bob wake up, what do you think?|"+start.text {
		t.Errorf("window read = %q, %v", texts(read.Messages), err)
	}
	if _, err := h.core.ReadChat(ctx, me, "self", 5); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("chat agent read the self-chat: %v", err)
	}
	if _, err := h.core.ReadChat(ctx, me, "other@g.us", 5); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("chat agent read another chat: %v", err)
	}

	// While the window is open, every new message goes to the agent, and its answers go out.
	h.receive("g@g.us", "a reply from Sam", false)
	if got := events(t, agent.fakePusher); got[0].Message.Text != "a reply from Sam" {
		t.Fatalf("the agent got %+v", got)
	}
	agent.host.Post("  NO_REPLY ")
	agent.host.Post("Looks fine to me.")
	h.expectText("🤖 *Bob*: Looks fine to me.")

	// Naming the agents again moves the end; a stop phrase closes the window.
	before, _, _ := h.st.Window(ctx, "g@g.us")
	time.Sleep(10 * time.Millisecond)
	h.receive("g@g.us", "thanks bob", true)
	after, _, _ := h.st.Window(ctx, "g@g.us")
	if !after.Until.After(before.Until) || after.From != before.From {
		t.Errorf("extend moved %+v to %+v", before, after)
	}
	_ = events(t, agent.fakePusher)
	h.receive("g@g.us", "Bob, stop watching please", true)
	h.expectText("🤖 *Bob*: I am done here. Mention Bob when you need me again.")
	if _, open, _ := h.st.Window(ctx, "g@g.us"); open {
		t.Fatal("the stop phrase left the window open")
	}

	h.receive("g@g.us", "after the window", false)
	quiet(t, agent.fakePusher)
	if read, _ := h.core.ReadChat(ctx, me, "g@g.us", 0); len(read.Messages) != 0 {
		t.Errorf("read after the window = %q", texts(read.Messages))
	}
	agent.host.Post("one more thing")
	h.expectCode("chat-team wants to send to Team")
}

func TestWindowEndsAtItsTime(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	if out, _ := h.core.Run(ctx, Owner, "/window close Team"); out != "Team has no open window." {
		t.Errorf("/window close without a window = %q", out)
	}
	h.receive("g@g.us", "Agent start", true)
	h.expect("Now monitoring")
	if out, _ := h.core.Run(ctx, Owner, "/status"); !strings.Contains(out, "Open window: Team, until") {
		t.Errorf("/status = %q", out)
	}
	w, _, _ := h.st.Window(ctx, "g@g.us")
	w.Until = time.Now().Add(-time.Second)
	_ = h.st.SetWindow(ctx, w)
	h.expect("I am done here")
	if _, open, _ := h.st.Window(ctx, "g@g.us"); open {
		t.Fatal("the window outlived its end")
	}

	h.receive("g@g.us", "agent, monitor this again", true)
	h.expect("Now monitoring")
	if out, _ := h.core.Run(ctx, Owner, "/window close Team"); out != "Closed the window of Team." {
		t.Errorf("/window close = %q", out)
	}
	h.expect("I am done here")
}

func TestKeywordAndOffModes(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	_, _ = h.core.Run(ctx, Owner, "/name Bob")
	if out, err := h.core.Run(ctx, Owner, "/route Team billing keyword"); err != nil || !strings.Contains(out, "keyword mode") {
		t.Fatalf("/route with a mode = %q, %v", out, err)
	}
	token, _, _ := h.core.CheckIn(ctx, model.Session{Name: "billing"})
	p := newPusher()
	go func() { _ = h.core.Subscribe(ctx, token, "channel", p) }()
	time.Sleep(50 * time.Millisecond)
	billing := Caller{Session: "billing"}

	h.receive("g@g.us", "plain chatter", false)
	h.receive("g@g.us", "Bob from Sam", false)
	h.receive("g@g.us", "the owner without the name", true)
	h.receive("g@g.us", "Bob, check the numbers", true)
	if got := events(t, p); len(got) != 1 || got[0].Message.Text != "Bob, check the numbers" {
		t.Fatalf("keyword mode pushed %+v", got)
	}
	out, err := h.core.Send(ctx, billing, "Team", "The numbers are right.")
	if err != nil || !strings.HasPrefix(out, "sent") {
		t.Fatalf("keyword send = %q, %v", out, err)
	}
	answer := h.expect("The numbers are right.")
	// The owner's reply to an agent message is a trigger too.
	// A second later, so the reply sorts after the answer even on a fast machine.
	reply := model.Message{Chat: "g@g.us", ID: "R1", Time: time.Now().UTC().Add(time.Second), FromMe: true, Text: "and the dates?", ReplyTo: answer.id}
	h.core.OnUpdate(model.Update{Kind: model.New, Message: reply}, true)
	if got := events(t, p); got[0].Message.Text != "and the dates?" {
		t.Fatalf("reply pushed %+v", got)
	}
	read, _ := h.core.ReadChat(ctx, billing, "Team", 2)
	if texts(read.Messages) != "🤖 *Bob*: The numbers are right.|and the dates?" {
		t.Errorf("keyword read = %q", texts(read.Messages))
	}
	if found, _ := h.core.Search(ctx, billing, "chatter", "", 10); len(found) != 0 {
		t.Errorf("keyword search found %d hidden messages", len(found))
	}

	if out, _ := h.core.Run(ctx, Owner, "/mode Team off"); out != "Team: off." {
		t.Errorf("/mode off = %q", out)
	}
	h.receive("g@g.us", "Bob, are you there?", true)
	quiet(t, p)
	if read, _ := h.core.ReadChat(ctx, billing, "Team", 0); len(read.Messages) != 0 {
		t.Errorf("off read = %q", texts(read.Messages))
	}
	if out, _ := h.core.Send(ctx, billing, "Team", "hello?"); !strings.Contains(out, "waiting for the owner") {
		t.Errorf("off send = %q", out)
	}
	if out, _ := h.core.Run(ctx, Owner, "/routes"); !strings.Contains(out, "Team -> billing (off)") {
		t.Errorf("/routes = %q", out)
	}
	if out, _ := h.core.Run(ctx, Owner, "/unroute Team"); !strings.Contains(out, "mode of every chat applies") {
		t.Errorf("/unroute = %q", out)
	}
	if _, ok, _ := h.st.ChatMode(ctx, "g@g.us"); ok {
		t.Error("/unroute left the chat's mode")
	}
}

func TestModeAndWindowCommands(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team <> Sam")
	run := func(line string) string {
		t.Helper()
		out, err := h.core.Run(ctx, Owner, line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		return out
	}
	if out := run("/mode"); out != "Every chat: window of 1 hour that reads back 30 minutes (the default)" {
		t.Errorf("/mode = %q", out)
	}
	if out := run("/mode Team <> Sam window 2h 1d"); out != "Team <> Sam: window of 2 hours that reads back 1 day." {
		t.Errorf("/mode with lengths = %q", out)
	}
	if out := run("/mode * all"); out != "Every chat: all." {
		t.Errorf("/mode * = %q", out)
	}
	if out := run("/mode"); out != "Every chat: all\nTeam <> Sam: window of 2 hours that reads back 1 day" {
		t.Errorf("/mode list = %q", out)
	}
	for _, bad := range []string{"/mode Team", "/mode Team sometimes", "/mode nowhere all", "/window lookback soon",
		"/window colour blue", "/window stop", "/window close nowhere"} {
		if _, err := h.core.Run(ctx, Owner, bad); err == nil {
			t.Errorf("%s gave no error", bad)
		}
	}
	run("/window length 2h")
	run("/window lookback 10m")
	run("/window wake go, begin")
	run("/window stop halt, cease")
	run("/window start Watching for {length}.")
	out := run("/window end Bye from {name}.")
	for _, want := range []string{"Window length: 2 hours", "Lookback: 10 minutes", "Wake phrases: go, begin",
		"Stop phrases: halt, cease", "Start message: Watching for {length}.", "End message: Bye from {name}."} {
		if !strings.Contains(out, want) {
			t.Errorf("/window = %q, want %q", out, want)
		}
	}
	run("/mode * window")
	h.receive("g@g.us", "Agent start", true)
	h.receive("g@g.us", "Agent, go", true)
	h.expectText("🤖 *Agent*: Watching for 2 hours.")
	if w, _, _ := h.st.Window(ctx, "g@g.us"); w.Until.Sub(w.Opened) != 2*time.Hour || w.Opened.Sub(w.From) != 24*time.Hour {
		t.Errorf("the chat's own lengths were not used: %+v", w)
	}
	h.receive("g@g.us", "agent, cease", true)
	h.expectText("🤖 *Agent*: Bye from Agent.")
	run("/window start default")
	if out := run("/window"); !strings.Contains(out, "Start message: Now monitoring") {
		t.Errorf("/window start default = %q", out)
	}
	h.receive("g@g.us", "Agent, begin once more", true)
	h.expect("Now monitoring")
	if out := run("/mode Team <> Sam all"); out != "Team <> Sam: all." {
		t.Errorf("/mode all = %q", out)
	}
	if _, open, _ := h.st.Window(ctx, "g@g.us"); open {
		t.Error("another mode kept the window")
	}
}

func TestChatAgentLifecycle(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("x@g.us", "Broken")
	_, _ = h.core.Run(ctx, Owner, "/mode * all")
	if err := h.st.TouchSession(ctx, model.Session{Name: "chat-team"}); err != nil {
		t.Fatal(err)
	}
	h.receive("g@g.us", "for the agent", false)
	agent := h.agent("chat-team-2")
	if got := events(t, agent.fakePusher); got[0].Message.Text != "for the agent" {
		t.Fatalf("the agent got %+v", got)
	}
	if err := agent.host.SaveSessionID("abc"); err != nil {
		t.Fatal(err)
	}

	// An idle agent stops, and the next message starts it again on the same harness session.
	deadline := time.Now().Add(3 * time.Second)
	for h.running("chat-team-2") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h.running("chat-team-2") {
		t.Fatal("the idle agent kept running")
	}
	h.receive("g@g.us", "wake up", false)
	if got := events(t, agent.fakePusher); got[0].Message.Text != "wake up" {
		t.Fatalf("the restarted agent got %+v", got)
	}
	h.mu.Lock()
	starts, id := agent.starts, agent.host.SessionID
	h.mu.Unlock()
	if starts != 2 || id != "abc" {
		t.Errorf("starts = %d, session ID = %q", starts, id)
	}

	// A route takes the chat from its agent.
	_, _ = h.core.Run(ctx, Owner, "/route Team billing")
	h.receive("g@g.us", "for billing", false)
	quiet(t, agent.fakePusher)
	if _, err := h.core.ReadChat(ctx, Caller{Session: "chat-team-2"}, "Team", 5); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("the agent read a routed chat: %v", err)
	}

	h.receive("x@g.us", "no harness here", false)
	time.Sleep(50 * time.Millisecond)
	if h.running("chat-broken") {
		t.Error("an agent whose harness failed is running")
	}
}

func (h *harness) running(name string) bool {
	h.core.mu.Lock()
	defer h.core.mu.Unlock()
	_, ok := h.core.agents[name]
	return ok
}

func TestSlugAndRouteFor(t *testing.T) {
	for in, want := range map[string]string{"Team <> Sam": "team-sam", "Ömer & Ayşe": "ömer-ayşe", "!!!": "chat",
		"120363430910285331@g.us": "120363430910285331-g-us", "A very long group name for testing": "a-very-long-group-name-f"} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
	routes := []model.Route{{Chat: "*", Session: "home"}, {Chat: "g@g.us", Session: "billing"}}
	if routeFor(routes, "g@g.us") != "billing" || routeFor(routes, "h@g.us") != "home" || routeFor(nil, "g@g.us") != "" {
		t.Error("routeFor is wrong")
	}
}

func TestHiddenMessagesStayHidden(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	_, _ = h.core.Run(ctx, Owner, "/route Team bot keyword")
	media := model.Message{Chat: "g@g.us", ID: "IMG", Sender: "200@s.whatsapp.net", Time: time.Now(), MediaType: "image",
		MimeType: "image/jpeg", Raw: []byte{1}}
	h.core.OnUpdate(model.Update{Kind: model.New, Message: media}, true)
	bot := Caller{Session: "bot"}
	if _, err := h.core.DownloadMedia(ctx, bot, "Team", "IMG"); err == nil {
		t.Error("the bot downloaded media that its mode hides")
	}
	if _, err := h.core.React(ctx, bot, "Team", "IMG", "👀"); err == nil {
		t.Error("the bot reacted to a message that its mode hides")
	}
	_, _ = h.core.Run(ctx, Owner, "/mode Team all")
	if _, err := h.core.DownloadMedia(ctx, bot, "Team", "IMG"); err != nil {
		t.Errorf("download in the all mode: %v", err)
	}
}
