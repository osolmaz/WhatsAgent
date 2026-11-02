package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/store"
)

const self = "100@s.whatsapp.net"

// sentText is one sent message; file names the file of a media message, whose caption is text.
// kind is "edit" or "revoke" for an edit or a deletion of the message id.
type sentText struct {
	chat, id, text, file, kind string
	out                        model.Outgoing
}

type fakeWA struct {
	mu         sync.Mutex
	n          int
	sent       chan sentText
	reacts     []string
	failSend   error
	uploads    int
	failUpload error
}

// Upload makes an .ogg file audio, a .png file a photo and every other file a document.
func (f *fakeWA) Upload(_ context.Context, file model.File) (model.Media, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failUpload != nil {
		return model.Media{}, f.failUpload
	}
	f.uploads++
	kind := map[string]string{".ogg": "audio", ".png": "image"}[filepath.Ext(file.Name)]
	if kind == "" {
		kind = "document"
	}
	return model.Media{Type: kind, MimeType: "x/" + kind, Filename: file.Name, Size: len(file.Data),
		Raw: []byte("raw " + file.Name)}, nil
}

func (f *fakeWA) Edit(_ context.Context, chat, id, text string) error {
	f.sent <- sentText{chat: chat, id: id, text: text, kind: "edit"}
	return nil
}

func (f *fakeWA) Revoke(_ context.Context, chat, sender, id string) error {
	f.sent <- sentText{chat: chat, id: id, text: sender, kind: "revoke"}
	return nil
}

func (f *fakeWA) SelfChat() string { return self }
func (f *fakeWA) OwnName() string  { return "Owner" }
func (f *fakeWA) NewID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return fmt.Sprintf("S%d", f.n)
}
func (f *fakeWA) Send(_ context.Context, chat, id string, out model.Outgoing) error {
	f.mu.Lock()
	err := f.failSend
	f.mu.Unlock()
	if err != nil {
		return err
	}
	s := sentText{chat: chat, id: id, text: out.Text, out: out}
	if out.Media != nil {
		s.file = out.Media.Filename
	}
	f.sent <- s
	return nil
}
func (f *fakeWA) React(_ context.Context, chat, sender, id, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reacts = append(f.reacts, strings.Join([]string{chat, sender, id, emoji}, "|"))
	return nil
}
func (f *fakeWA) Download(context.Context, []byte) ([]byte, error) { return []byte("data"), nil }
func (f *fakeWA) ChatName(_ context.Context, jid string) string    { return "Name " + jid }

type fakePusher struct {
	events   chan []model.Event
	verdicts chan string
	err      error
}

func newPusher() *fakePusher {
	return &fakePusher{events: make(chan []model.Event, 20), verdicts: make(chan string, 5)}
}

func (p *fakePusher) Push(_ context.Context, events []model.Event) error {
	if events != nil {
		p.events <- events
	}
	return p.err
}

func (p *fakePusher) Verdict(_ context.Context, id string, allow bool) error {
	p.verdicts <- fmt.Sprintf("%s=%v", id, allow)
	return nil
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	core  *Core
	wa    *fakeWA
	st    *store.Store
	homes map[string]*fakePusher
	n     int

	mu     sync.Mutex
	agents map[string]*fakeAgent
}

// fakeAgent is a chat agent that the harness started.
type fakeAgent struct {
	*fakePusher
	host   AgentHost
	starts int
}

// agent returns the chat agent named session once it started.
func (h *harness) agent(session string) *fakeAgent {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		a := h.agents[session]
		h.mu.Unlock()
		if a != nil {
			return a
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("chat agent %s did not start", session)
	return nil
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, func(*Config) {})
}

// newHarnessWith returns a harness whose core config change adjusts before the core starts.
func newHarnessWith(t *testing.T, change func(*Config)) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &harness{t: t, ctx: ctx, wa: &fakeWA{sent: make(chan sentText, 100)}, st: st, homes: map[string]*fakePusher{},
		agents: map[string]*fakeAgent{}}
	factory := func(name string) AgentFactory {
		return func(_ context.Context, host AgentHost) (Pusher, error) {
			if name == "broken" {
				return nil, errors.New("no harness")
			}
			p := newPusher()
			h.homes[name] = p
			_ = host.SaveSessionID("id-" + name)
			return p, nil
		}
	}
	cfg := Config{Store: st, WhatsApp: h.wa, MediaDir: t.TempDir(), Location: time.UTC,
		Homes:       map[string]AgentFactory{"claude": factory("claude"), "codex": factory("codex"), "broken": factory("broken")},
		ChatAgent:   h.startAgent,
		StatusDelay: 10 * time.Millisecond, Heartbeat: 20 * time.Millisecond,
		WindowCheck: 20 * time.Millisecond, AgentIdle: 300 * time.Millisecond}
	change(&cfg)
	h.core = New(cfg)
	h.core.Start(ctx)
	return h
}

// keep makes chats kept, so callers that are not run agents may read them.
func (h *harness) keep(chats ...string) {
	h.t.Helper()
	for _, chat := range chats {
		if _, _, err := h.st.Keep(h.ctx, chat); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) startAgent(_ context.Context, host AgentHost) (Pusher, error) {
	if host.ChatName == "Broken" {
		return nil, errors.New("no harness")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.agents[host.Session]
	if a == nil {
		a = &fakeAgent{fakePusher: newPusher()}
		h.agents[host.Session] = a
	}
	a.host = host
	a.starts++
	return a.fakePusher, nil
}

// receive delivers a live message as WhatsApp would.
func (h *harness) receive(chat, text string, fromMe bool) model.Message {
	h.n++
	m := model.Message{Chat: chat, ID: fmt.Sprintf("M%d", h.n), Sender: "200@s.whatsapp.net", SenderName: "Sam",
		Time: time.Now().UTC(), FromMe: fromMe, Text: text}
	h.core.OnUpdate(model.Update{Kind: model.New, Message: m})
	return m
}

// expect waits for a sent message that contains want.
func (h *harness) expect(want string) sentText {
	h.t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case s := <-h.wa.sent:
			if strings.Contains(s.text, want) {
				return s
			}
		case <-timeout:
			h.t.Fatalf("no message containing %q was sent", want)
		}
	}
}

var codePattern = regexp.MustCompile(`yes (\d{2,3})`)

func (h *harness) expectCode(want string) string {
	h.t.Helper()
	s := h.expect(want)
	m := codePattern.FindStringSubmatch(s.text)
	if m == nil {
		h.t.Fatalf("no code in %q", s.text)
	}
	return m[1]
}

func events(t *testing.T, p *fakePusher) []model.Event {
	t.Helper()
	select {
	case e := <-p.events:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("no events were pushed")
	}
	return nil
}

func TestWelcomeCommandsAndStatus(t *testing.T) {
	h := newHarness(t)
	h.core.OnState("connected")
	h.expect("Welcome to WhatsAgent")
	h.receive(self, "/help", true)
	h.expect("/route <chat> <session>")
	h.receive(self, "/nope", true)
	h.expect("unknown command /nope")
	h.receive(self, "Yes 77", true)
	h.expect("Nothing waits for the code 77")
	h.core.OnChatName("g@g.us", "Team <> Sam")
	h.core.OnChatName("x@g.us", "")
	h.receive("g@g.us", "hello", false)
	h.receive(self, "/chats Team", true)
	h.expect("Team <> Sam: g@g.us")
	h.receive(self, "/chats zzz", true)
	h.expect(`No chat matches "zzz"`)
	h.receive(self, "/status", true)
	h.expect("WhatsApp: connected")

	// A second start after the welcome posts a status line instead.
	h.core.state = "starting"
	h.core.OnState("connected")
	h.expect("WhatsAgent is running")

	msgs, _ := h.st.Messages(h.ctx, self, 0)
	for _, m := range msgs {
		if strings.Contains(m.Text, "Welcome to WhatsAgent") && m.Role != model.RoleAgent {
			t.Errorf("own sent message role = %s", m.Role)
		}
	}
	for text, want := range map[string]bool{"no 12": true, "y12": true, " YES 99 ": true, "no abcde": false, "yes 1": false, "no thanks": false} {
		if got := isCommand(text); got != want {
			t.Errorf("isCommand(%q) = %v", text, got)
		}
	}
}

func TestRoutesPushAndAddressed(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.keep("g@g.us")
	token, name, err := h.core.CheckIn(ctx, model.Session{Folder: "/repos/billing api", Harness: "claude"})
	if err != nil || name != "billing-api" {
		t.Fatalf("CheckIn = %q, %v", name, err)
	}
	p := newPusher()
	if reply, err := h.core.Run(ctx, phoneOwner, "/route Team billing-api"); err != nil || !strings.Contains(reply, "now goes to billing-api") {
		t.Fatalf("/route = %q, %v", reply, err)
	}
	go func() { _ = h.core.Subscribe(ctx, token, "channel", p) }()
	h.expect("billing-api is online")
	h.receive("g@g.us", "please fix it", false)
	got := events(t, p)
	if len(got) != 1 || got[0].Message.Text != "please fix it" || got[0].ChatName != "Team" {
		t.Fatalf("pushed %+v", got)
	}
	h.receive(self, "a note to self", true)
	h.receive("other@g.us", "not routed", false)
	time.Sleep(100 * time.Millisecond) // let the push session pass these two before the route changes

	if _, err := h.core.Run(ctx, phoneOwner, "/mode Team keyword"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.core.Run(ctx, phoneOwner, "/name Bob"); err != nil {
		t.Fatal(err)
	}
	h.receive("g@g.us", "chatter", false)
	h.receive("g@g.us", "Bob, someone else may not call you", false)
	h.receive("g@g.us", "Bobby is not the agent", true)
	h.receive("g@g.us", "hey BOB, look", true)
	if got := events(t, p); got[0].Message.Text != "hey BOB, look" {
		t.Fatalf("keyword mode pushed %+v", got)
	}

	sessions, _ := h.core.Sessions(ctx)
	if len(sessions) != 1 || !sessions[0].Online || sessions[0].Push != "channel" {
		t.Errorf("Sessions = %+v", sessions)
	}
	if reply, _ := h.core.Run(ctx, phoneOwner, "/sessions"); !strings.Contains(reply, "push: channel") {
		t.Errorf("/sessions = %q", reply)
	}
	if reply, _ := h.core.Run(ctx, phoneOwner, "/routes"); !strings.Contains(reply, "Team -> billing-api") {
		t.Errorf("/routes = %q", reply)
	}
	if reply, _ := h.core.Run(ctx, phoneOwner, "/unroute Team"); !strings.Contains(reply, "no longer goes") {
		t.Errorf("/unroute = %q", reply)
	}
	if reply, _ := h.core.Run(ctx, phoneOwner, "/unroute Team"); !strings.Contains(reply, "had no route") {
		t.Errorf("second /unroute = %q", reply)
	}
	if reply, _ := h.core.Run(ctx, phoneOwner, "/routes"); !strings.Contains(reply, "No routes") {
		t.Errorf("empty /routes = %q", reply)
	}
	h.core.CheckOut(token)
	if _, err := h.core.Caller(token); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("Caller after CheckOut: %v", err)
	}
	if err := h.core.Subscribe(ctx, token, "channel", p); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("Subscribe after CheckOut: %v", err)
	}
}

func TestEveryChatRouteAndWatch(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "*", Session: "all"}, model.ModeAll)
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "mine@g.us", Session: "someone-else"}, model.ModeAll)
	h.keep("any@g.us", "mine@g.us", "extra@g.us")
	p := newPusher()
	go func() { _ = h.core.Watch(ctx, "all", []string{"extra@g.us"}, 0, p) }()
	time.Sleep(50 * time.Millisecond)
	h.receive("any@g.us", "one", false)
	if got := events(t, p); got[0].Message.Text != "one" {
		t.Fatalf("pushed %+v", got)
	}
	h.receive("mine@g.us", "elsewhere", false)
	h.receive("extra@g.us", "two", false)
	if got := events(t, p); got[0].Message.Text != "two" {
		t.Fatalf("pushed %+v", got)
	}
	if err := h.core.Watch(ctx, "bad", []string{"no such chat"}, -1, p); err == nil {
		t.Error("Watch with an unknown chat gave no error")
	}
}

func TestHome(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.receive(self, "/home claude", true)
	h.expect("Home runs on claude")
	h.receive(self, "what did Sam say?", true)
	p := h.homes["claude"]
	if got := events(t, p); got[0].Message.Text != "what did Sam say?" {
		t.Fatalf("home got %+v", got)
	}
	h.core.PostHome("Sam said hi.")
	h.expect("🏠 Sam said hi.")
	h.core.PostHome("NO_REPLY")
	if id, _ := h.st.Setting(ctx, "home.claude.session"); id != "id-claude" {
		t.Errorf("home session = %q", id)
	}
	if reply, _ := h.core.Run(ctx, phoneOwner, "/home new"); !strings.Contains(reply, "fresh home session") {
		t.Errorf("/home new = %q", reply)
	}
	if _, err := h.core.Run(ctx, phoneOwner, "/home vim"); err == nil {
		t.Error("/home vim gave no error")
	}
	if _, err := h.core.Run(ctx, phoneOwner, "/home broken"); err == nil {
		t.Error("/home broken gave no error")
	}
	if reply, _ := h.core.Run(ctx, phoneOwner, "/home off"); !strings.Contains(reply, "Home is off") {
		t.Errorf("/home off = %q", reply)
	}
	if _, err := h.core.Run(ctx, phoneOwner, "/home new"); err == nil {
		t.Error("/home new while off gave no error")
	}
	if err := h.core.startHome(ctx, "none"); err == nil {
		t.Error("startHome(none) gave no error")
	}
	// A restart picks home up from the stored setting.
	_ = h.st.SetSetting(ctx, "home", "codex")
	restarted := New(h.core.cfg)
	restarted.Start(ctx)
	if restarted.homeLine() != "Home: codex" {
		t.Errorf("home after Start = %q", restarted.homeLine())
	}
	restarted.stopHome()
	_ = h.st.SetSetting(ctx, "home", "broken")
	New(h.core.cfg).Start(ctx)
}

func TestSendRulesAndApprovals(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	if _, err := h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot", Prefix: "Claude 🤖: "}, model.ModeAll); err != nil {
		t.Fatal(err)
	}
	bot := Caller{Session: "bot"}
	h.converse("bot", "g@g.us", model.RoleOther)

	if out, err := h.core.Send(ctx, bot, "Team", "on it"); err != nil || !strings.Contains(out, "sent") {
		t.Fatalf("routed send = %q, %v", out, err)
	}
	if s := h.expect("on it"); s.text != "Claude 🤖: on it" || s.chat != "g@g.us" {
		t.Errorf("sent %+v", s)
	}
	_, _ = h.core.Send(ctx, bot, "self", "done")
	h.expect("🤖 *Agent*: done")

	out, err := h.core.Send(ctx, bot, "Sam", "hello Sam")
	if err != nil || !strings.Contains(out, "waiting for the owner") {
		t.Fatalf("unrouted send = %q, %v", out, err)
	}
	code := h.expectCode("session bot wants to send this message to \"Sam\" (direct chat):\n🤖 *Agent*: hello Sam")
	h.receive(self, "YES "+code, true)
	h.expect("hello Sam")

	_, _ = h.core.Send(ctx, bot, "Sam", "second")
	code = h.expectCode("second")
	h.receive(self, "no "+code, true)
	h.expect("Not sent.")

	_ = h.st.SetRule(ctx, model.Rule{Session: "bot", Chat: "sam@s.whatsapp.net", Permission: model.Deny})
	if _, err := h.core.Send(ctx, bot, "Sam", "x"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("denied send: %v", err)
	}
	if _, err := h.core.Send(ctx, Owner, "+1 (555) 000-1111", "owner send"); err != nil {
		t.Errorf("owner send: %v", err)
	}
	if s := h.expect("owner send"); s.chat != "15550001111@s.whatsapp.net" {
		t.Errorf("phone number resolved to %q", s.chat)
	}
	if _, err := h.core.Send(ctx, bot, "", "x"); err == nil {
		t.Error("send without a chat gave no error")
	}

	// A failing send is reported to the owner after a yes.
	_ = h.st.SetRule(ctx, model.Rule{Session: "bot", Chat: "sam@s.whatsapp.net", Permission: model.Ask})
	_, _ = h.core.Send(ctx, bot, "Sam", "will fail")
	code = h.expectCode("will fail")
	h.wa.mu.Lock()
	h.wa.failSend = errors.New("offline")
	h.wa.mu.Unlock()
	h.core.answer(ctx, self, code, true)
	h.wa.mu.Lock()
	h.wa.failSend = nil
	h.wa.mu.Unlock()
	h.expect("Failed: offline")
}

func TestReactMediaReadSearch(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.keep("g@g.us")
	m := h.receive("g@g.us", "photo caption", false)
	media := model.Message{Chat: "g@g.us", ID: "IMG", Sender: "200@s.whatsapp.net", RawSender: "200@lid",
		Time: time.Now(), MediaType: "image", MimeType: "image/jpeg", Raw: []byte{1}}
	h.core.OnUpdate(model.Update{Kind: model.New, Message: media})
	doc := model.Message{Chat: "g@g.us", ID: "DOC", Time: time.Now(), MediaType: "document", Filename: "a b.pdf", Raw: []byte{1}}
	h.core.OnUpdate(model.Update{Kind: model.New, Message: doc})

	if out, err := h.core.React(ctx, Owner, "g@g.us", "IMG", "👀"); err != nil || out != "reacted" {
		t.Fatalf("React = %q, %v", out, err)
	}
	if h.wa.reacts[0] != "g@g.us|200@lid|IMG|👀" {
		t.Errorf("react = %q", h.wa.reacts[0])
	}
	if _, err := h.core.React(ctx, Owner, "g@g.us", m.ID, "👍"); err != nil {
		t.Errorf("React on a text message: %v", err)
	}
	if _, err := h.core.React(ctx, Owner, "g@g.us", "none", "👀"); err == nil {
		t.Error("React on a missing message gave no error")
	}
	path, err := h.core.DownloadMedia(ctx, Owner, "g@g.us", "IMG")
	if err != nil || !strings.HasSuffix(path, "IMG.jpg") {
		t.Fatalf("DownloadMedia = %q, %v", path, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "data" {
		t.Errorf("saved %q", data)
	}
	if path, _ := h.core.DownloadMedia(ctx, Owner, "g@g.us", "DOC"); !strings.HasSuffix(path, "DOC.pdf") {
		t.Errorf("document saved as %q", path)
	}
	if _, err := h.core.DownloadMedia(ctx, Owner, "g@g.us", m.ID); err == nil {
		t.Error("DownloadMedia of a text message gave no error")
	}
	bot := Caller{Session: "bot"}
	if _, err := h.core.DownloadMedia(ctx, bot, "g@g.us", "IMG"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("unrouted download: %v", err)
	}
	if _, err := h.core.ReadChat(ctx, bot, "g@g.us", 10); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("unrouted read: %v", err)
	}
	read, err := h.core.ReadChat(ctx, Caller{Session: "home"}, "g@g.us", 10)
	if err != nil || len(read.Messages) != 3 {
		t.Errorf("home read = %d, %v", len(read.Messages), err)
	}
	// A send rule lets a session write to a chat; only handling the chat lets it read.
	_ = h.st.SetRule(ctx, model.Rule{Session: "bot", Chat: "g@g.us", Permission: model.Allow})
	if found, _ := h.core.Search(ctx, bot, "caption", "", 10); len(found) != 0 {
		t.Errorf("Search with only a send rule = %d", len(found))
	}
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	if found, _ := h.core.Search(ctx, bot, "caption", "", 10); len(found) != 1 {
		t.Errorf("Search = %d", len(found))
	}
	if found, _ := h.core.Search(ctx, Caller{Session: "x"}, "caption", "g@g.us", 10); len(found) != 0 {
		t.Errorf("Search for another session = %d", len(found))
	}
	if _, err := h.core.Search(ctx, bot, "x", "no match at all", 10); err == nil {
		t.Error("Search in an unknown chat gave no error")
	}
	exported, _ := h.core.Export(ctx, "g@g.us")
	changes, _ := h.core.Changes(ctx, 0)
	if len(exported) != 3 || len(changes.Chats) != 1 || changes.Cursor == 0 {
		t.Errorf("Export = %d, Changes = %+v", len(exported), changes)
	}
	if empty, _ := h.core.Changes(ctx, changes.Cursor); empty.Chats == nil || len(empty.Chats) != 0 {
		t.Errorf("Changes after the cursor = %+v", empty)
	}
	if chats, _ := h.core.Chats(ctx, "", 10); len(chats) != 1 {
		t.Errorf("Chats = %+v", chats)
	}
	if extension(model.Message{MimeType: "x/unknown"}) != ".bin" {
		t.Error("unknown type extension")
	}
}

func TestSessionCommandsWaitForOwner(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	bot := Caller{Session: "bot"}
	out, _ := h.core.Run(ctx, bot, "/allow bot Team")
	if !strings.Contains(out, "waiting for the owner") {
		t.Fatalf("session /allow = %q", out)
	}
	code := h.expectCode("bot wants to run: /allow bot Team")
	h.receive(self, "y "+code, true)
	h.expect("Rule: allow bot -> g@g.us")
	_, _ = h.core.Run(ctx, bot, "/deny bot *")
	code = h.expectCode("/deny bot *")
	h.receive(self, "n "+code, true)
	h.expect("Not run.")
	_, _ = h.core.Run(ctx, bot, "/route nowhere bot")
	code = h.expectCode("/route nowhere bot")
	h.core.answer(ctx, self, code, true)
	h.expect("Error: no chat matches")
	if out, _ := h.core.Run(ctx, bot, "/rules"); !strings.Contains(out, "allow bot -> g@g.us") {
		t.Errorf("session /rules = %q", out)
	}
	for _, line := range []string{"/ask", "/route Team", "/rename bot"} {
		if _, err := h.core.Run(ctx, phoneOwner, line); err == nil {
			t.Errorf("%s gave no error", line)
		}
	}
	if out, _ := h.core.Run(ctx, phoneOwner, "/ask * *"); !strings.Contains(out, "Rule: ask") {
		t.Errorf("/ask = %q", out)
	}
	if _, err := h.core.Run(ctx, phoneOwner, "/allow bot no-such-chat"); err == nil {
		t.Error("/allow with an unknown chat gave no error")
	}
	if out, _ := h.core.Run(ctx, phoneOwner, "/rename bot helper"); !strings.Contains(out, "bot is now helper") {
		t.Errorf("/rename = %q", out)
	}
	_ = h.st.SetRoute(ctx, model.Route{Chat: "*", Session: "x"})
	if _, err := h.core.Run(ctx, phoneOwner, "/unroute *"); err != nil {
		t.Errorf("/unroute *: %v", err)
	}

	out, _ = h.core.RouteHere(ctx, bot, "Team", "P: ", "keyword")
	if !strings.Contains(out, "waiting") {
		t.Fatalf("RouteHere = %q", out)
	}
	code = h.expectCode("bot wants the chat Team routed to it in the keyword mode")
	h.receive(self, "yes "+code, true)
	h.expect("Team now goes to bot in the keyword mode")
	_, _ = h.core.RouteHere(ctx, bot, "Team", "", "")
	code = h.expectCode("routed to it in the all mode")
	h.receive(self, "no "+code, true)
	h.expect("Not routed.")
	if out, err := h.core.RouteHere(ctx, Owner, "Team", "", ""); err != nil || !strings.Contains(out, "in the all mode") {
		t.Errorf("owner RouteHere = %q, %v", out, err)
	}
	if _, err := h.core.RouteHere(ctx, bot, "", "", ""); err == nil {
		t.Error("RouteHere without a chat gave no error")
	}
	if _, err := h.core.RouteHere(ctx, bot, "Team", "", "sometimes"); err == nil {
		t.Error("RouteHere with an unknown mode gave no error")
	}
}

func TestApproveAndRelay(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	answer := make(chan bool, 1)
	go func() {
		answer <- h.core.Approve(ctx, Caller{Session: "home"}, "Bash", `{"command":"ls"}`, time.Minute)
	}()
	code := h.expectCode("session home wants to use the tool Bash with this input:\n{\"command\":\"ls\"}")
	h.receive(self, "yes "+code, true)
	if !<-answer {
		t.Error("Approve after yes = false")
	}
	if h.core.Approve(ctx, Caller{Session: "home"}, "Bash", "rm", 30*time.Millisecond) {
		t.Error("Approve without an answer = true")
	}
	h.expect("so Bash was denied")

	token, name, _ := h.core.CheckIn(ctx, model.Session{Name: "dev"})
	p := newPusher()
	go func() { _ = h.core.Subscribe(ctx, token, "channel", p) }()
	time.Sleep(50 * time.Millisecond)
	h.core.RelayPermission(Caller{Session: name}, "qwert", "Write", "write a file", "{}")
	code = h.expectCode("session dev wants to use the tool Write with this input:\n{}\nThe agent's own description: write a file")
	h.receive(self, "yes "+code, true)
	select {
	case v := <-p.verdicts:
		if v != "qwert=true" {
			t.Errorf("verdict %q", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no verdict")
	}
	h.core.RelayPermission(Caller{Session: "gone"}, "asdfg", "Write", "", "")
	code = h.expectCode("session gone wants to use the tool Write")
	h.receive(self, "no"+code, true)
	h.expect("gone is offline")
	if clip("  abcdef ", 3) != "abc…" {
		t.Error("clip is wrong")
	}
}

func TestAttachHeartbeatAndStatusMerge(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(h.ctx)
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "codex-a"}, model.ModeAll)
	h.keep("g@g.us")
	p := newPusher()
	done := make(chan error, 1)
	go func() { done <- h.core.Attach(ctx, model.Session{Name: "codex-a", Harness: "codex"}, "codex", p) }()
	h.expect("codex-a is online (codex)")
	h.receive("g@g.us", "for codex", false)
	if got := events(t, p); got[0].Message.Text != "for codex" {
		t.Fatalf("pushed %+v", got)
	}
	// A newer push target replaces the older one.
	p2 := newPusher()
	go func() { _ = h.core.Attach(ctx, model.Session{Name: "codex-a"}, "codex", p2) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("replaced Attach returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("older push target was not replaced")
	}
	cancel()

	// A failing heartbeat ends the push session.
	failing := newPusher()
	failing.err = errors.New("gone")
	if err := h.core.Attach(h.ctx, model.Session{Name: "dead"}, "codex", failing); err == nil {
		t.Error("Attach with a failing pusher returned no error")
	}
	h.core.statusLine("one")
	h.core.statusLine("two")
	h.expect("one\ntwo")
	status, err := h.core.Status(h.ctx)
	if err != nil || status.OwnerName != "Owner" || status.Routes != 1 {
		t.Errorf("Status = %+v, %v", status, err)
	}
	s := Status{}
	s.addSession(model.Session{Name: "w", Waiting: 2})
	if len(s.Waiting) != 1 {
		t.Errorf("waiting = %v", s.Waiting)
	}
	if sessionName(model.Session{Folder: "/"}) != "session" {
		t.Error("sessionName(/) is wrong")
	}
}

func TestResolveChat(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	for jid, name := range map[string]string{"a@g.us": "Team", "b@g.us": "Team Berlin", "c@g.us": "Team Rome", "d@g.us": "Family"} {
		h.core.OnChatName(jid, name)
	}
	cases := map[string]string{"team": "a@g.us", "Fam": "d@g.us", "me": self, "x@lid": "x@lid"}
	for ref, want := range cases {
		if got, err := h.core.resolveChat(ctx, ref); err != nil || got != want {
			t.Errorf("resolveChat(%q) = %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := h.core.resolveChat(ctx, "Team "); err != nil {
		t.Errorf("trailing space: %v", err)
	}
	_, err := h.core.resolveChat(ctx, "Tea")
	if err == nil || !strings.Contains(err.Error(), "matches several chats") {
		t.Errorf("ambiguous = %v", err)
	}
	if _, err := h.core.resolveChat(ctx, "nobody"); err == nil {
		t.Error("no match gave no error")
	}
}

func TestFullBatchWakesAgain(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bulk"}, model.ModeAll)
	h.keep("g@g.us")
	if err := h.st.TouchSession(ctx, model.Session{Name: "bulk"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < eventBatch+5; i++ {
		h.receive("g@g.us", fmt.Sprintf("m%d", i), false)
	}
	p := newPusher()
	go func() { _ = h.core.Attach(ctx, model.Session{Name: "bulk"}, "codex", p) }()
	total := 0
	for total < eventBatch+5 {
		total += len(events(t, p))
	}
	if total != eventBatch+5 {
		t.Errorf("pushed %d events", total)
	}
}

func TestOwnSendFailsAndOutboxFull(t *testing.T) {
	h := newHarness(t)
	h.wa.failSend = errors.New("down")
	if _, err := h.core.Send(h.ctx, Owner, "g@g.us", "x"); err == nil {
		t.Error("failing send gave no error")
	}
	full := New(Config{Store: h.st, WhatsApp: h.wa})
	for i := 0; i < 70; i++ {
		full.post("x")
	}
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	if _, err := h.core.sendText(ctx, "g@g.us", "x"); err == nil {
		t.Error("sendText on a cancelled context gave no error")
	}
	h.core.OnUpdate(model.Update{Kind: "bad", Message: model.Message{Chat: "g@g.us", ID: "z"}})
}
