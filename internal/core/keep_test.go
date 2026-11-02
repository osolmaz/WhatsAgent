package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// runAgentKey marks the context of a check-in that the test's Verify accepts as home's run agent.
type runAgentKey struct{}

func verifyHome(ctx context.Context, session string) bool {
	return session == homeName && ctx.Value(runAgentKey{}) != nil
}

// TestOnlyRunAgentsReadChatsThatAreNotKept is the reader boundary: the CLI, the CLI acting for a
// session, and a session that only claims a run agent's name read kept chats only; the run agent
// that Verify recognizes reads the others too.
func TestOnlyRunAgentsReadChatsThatAreNotKept(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.Verify = verifyHome })
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("k@g.us", "Kept")
	h.keep("k@g.us")
	secret := h.receive("g@g.us", "secret plans", false)
	h.receive("k@g.us", "kept plans", false)
	h.core.OnUpdate(model.Update{Kind: model.New, Message: model.Message{Chat: "g@g.us", ID: "IMG", Time: time.Now(),
		MediaType: "image", MimeType: "image/jpeg", Raw: []byte{1}}})
	h.stored("g@g.us", "IMG")

	claimed, _, _ := h.core.CheckIn(ctx, model.Session{Name: homeName})
	impostor, _ := h.core.Caller(claimed)
	for _, caller := range []Caller{Owner, As(homeName), {Session: homeName}, impostor} {
		if _, err := h.core.ReadChat(ctx, caller, "Team", 10); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%+v read a chat that is not kept: %v", caller, err)
		}
		if found, _ := h.core.Search(ctx, caller, "plans", "", 10); texts(found) != "kept plans" {
			t.Errorf("%+v searched a chat that is not kept: %q", caller, texts(found))
		}
		if _, err := h.core.React(ctx, caller, "Team", secret.ID, "👀"); !errors.Is(err, ErrNotAllowed) {
			t.Errorf("%+v reacted in a chat that is not kept: %v", caller, err)
		}
	}
	if _, err := h.core.Export(ctx, "Team"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("Export of a chat that is not kept: %v", err)
	}
	if read, err := h.core.ReadChat(ctx, Owner, "Kept", 10); err != nil || texts(read.Messages) != "kept plans" {
		t.Errorf("the CLI's read of a kept chat = %q, %v", texts(read.Messages), err)
	}
	if got, err := h.core.Export(ctx, "Kept"); err != nil || len(got) != 1 {
		t.Errorf("Export of a kept chat = %d, %v", len(got), err)
	}

	token, _, _ := h.core.CheckIn(context.WithValue(ctx, runAgentKey{}, true), model.Session{Name: homeName})
	home, _ := h.core.Caller(token)
	if read, err := h.core.ReadChat(ctx, home, "Team", 10); err != nil || !strings.Contains(texts(read.Messages), "secret plans") {
		t.Errorf("home's read = %q, %v", texts(read.Messages), err)
	}
	if found, _ := h.core.Search(ctx, home, "plans", "", 10); len(found) != 2 {
		t.Errorf("home's search = %q", texts(found))
	}
	// download_media writes a file, so it refuses a chat that is not kept even for a run agent.
	if _, err := h.core.DownloadMedia(ctx, home, "Team", "IMG"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("download in a chat that is not kept: %v", err)
	}
}

// TestUnverifiedPushesSkipChatsThatAreNotKept checks that `whatsagent watch`, like any session that
// is not a run agent, gets the events of kept chats only, and skips the others for good.
func TestUnverifiedPushesSkipChatsThatAreNotKept(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	h.keep("k@g.us")
	p := newPusher()
	go func() { _ = h.core.Watch(ctx, "watcher", []string{"g@g.us", "k@g.us"}, -1, p) }()
	time.Sleep(50 * time.Millisecond)
	h.receive("g@g.us", "not kept", false)
	h.receive("k@g.us", "kept", false)
	if got := events(t, p); len(got) != 1 || got[0].Message.Text != "kept" {
		t.Fatalf("watch got %+v", got)
	}
	h.keep("g@g.us")
	h.receive("k@g.us", "later", false)
	if got := events(t, p); len(got) != 1 || got[0].Message.Text != "later" {
		t.Errorf("a skipped event came back after the chat was kept: %+v", got)
	}
}

// changing are arguments that make each mutating command change state.
var changing = map[string]string{
	"route": "Team bot", "unroute": "Team", "mode": "Team all", "window": "length 2h", "rename": "bot other",
	"home": "claude", "allow": "bot Team", "ask": "bot Team", "deny": "bot Team", "name": "Zed",
	"browser": "off", "approvals": "self", "auto": "off", "prefix": "x", "keep": "Team on",
}

// state is what the bare commands show, plus home: every piece of state that a command can change.
func (h *harness) state() string {
	h.t.Helper()
	var b strings.Builder
	for _, line := range []string{"/routes", "/rules", "/mode", "/window", "/keep", "/name", "/prefix", "/auto",
		"/approvals", "/browser"} {
		reply, err := h.core.Run(h.ctx, phoneOwner, line)
		if err != nil {
			h.t.Fatalf("%s: %v", line, err)
		}
		b.WriteString(reply + "\n")
	}
	return b.String() + h.core.homeLine()
}

// TestCommandsChangeStateOnlyFromThePhone is the command boundary: through the CLI, every
// command that changes state is refused, the bare form of every command changes nothing, and
// /web is refused; an agent's change waits for the owner.
func TestCommandsChangeStateOnlyFromThePhone(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.PageURL = "http://page/?token=secret" })
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	_, _ = h.core.Run(ctx, phoneOwner, "/route Team bot")
	before := h.state()
	for _, cmd := range commands {
		if cmd.mutates {
			args, ok := changing[cmd.name]
			if !ok {
				t.Errorf("add arguments for /%s to changing", cmd.name)
				continue
			}
			if _, err := h.core.Run(ctx, Owner, "/"+cmd.name+" "+args); !errors.Is(err, ErrNotAllowed) {
				t.Errorf("/%s %s through the CLI = %v", cmd.name, args, err)
			}
		}
		if cmd.name != "web" {
			_, _ = h.core.Run(ctx, Owner, "/"+cmd.name)
		}
		if after := h.state(); after != before {
			t.Fatalf("/%s through the CLI changed the state:\n%s\nto\n%s", cmd.name, before, after)
		}
	}
	for _, caller := range []Caller{Owner, {Session: "bot"}} {
		if reply, err := h.core.Run(ctx, caller, "/web"); !errors.Is(err, ErrNotAllowed) || strings.Contains(reply, "secret") {
			t.Errorf("/web for %s = %q, %v", caller.Session, reply, err)
		}
	}
	if reply, _ := h.core.Run(ctx, phoneOwner, "/web"); !strings.Contains(reply, "token=secret") {
		t.Errorf("/web from the phone = %q", reply)
	}
	if reply, _ := h.core.Run(ctx, Caller{Session: "bot"}, "/name Zed"); !strings.Contains(reply, "waiting for the owner") {
		t.Errorf("an agent's change = %q", reply)
	}
	// The phone changes state, so the comparison above would have seen a change.
	_, _ = h.core.Run(ctx, phoneOwner, "/name Zed")
	if h.state() == before {
		t.Error("the state check does not see a change")
	}
}

// TestWebLinkIsNotStored checks that /web from the phone sends the page's link to the phone, but
// stores it nowhere, because the self-chat is kept and the CLI reads it.
func TestWebLinkIsNotStored(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.PageURL = "http://page/?token=PAGE-SECRET" })
	h.receive(self, "/web", true)
	h.expect("token=PAGE-SECRET")
	h.receive(self, "/name", true)
	h.expect("The agents are called")
	if found, _ := h.st.Search(h.ctx, "PAGE-SECRET", "", 10, true); len(found) != 0 {
		t.Errorf("the page's link was stored: %+v", found)
	}
	if found, _ := h.st.Search(h.ctx, "The agents are called", "", 10, false); len(found) != 1 {
		t.Errorf("an ordinary reply was not stored: %+v", found)
	}
}

// TestKeepCommand checks /keep: the list, keeping, stopping, and the self-chat that stays kept.
func TestKeepCommand(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.PageURL = "" })
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	if kept, _ := h.st.Kept(ctx, self); !kept {
		t.Fatal("Start did not keep the self-chat")
	}
	h.receive("g@g.us", "before keeping", false)
	steps := []struct{ line, want string }{
		{"/keep", "- the self-chat"},
		{"/keep Team on", "stored its 1 recent ones"},
		{"/keep Team on", "kept already"},
		{"/keep", "- Team"},
		{"/keep Team off", "deleted its 1 stored messages"},
		{"/keep Team off", "is not kept"},
	}
	for _, step := range steps {
		if reply, err := h.core.Run(ctx, phoneOwner, step.line); err != nil || !strings.Contains(reply, step.want) {
			t.Errorf("%s = %q, %v", step.line, reply, err)
		}
	}
	for _, line := range []string{"/keep self off", "/keep Team maybe", "/keep Team", "/keep Nobody on"} {
		if _, err := h.core.Run(ctx, phoneOwner, line); err == nil {
			t.Errorf("%s gave no error", line)
		}
	}
	if _, err := h.core.Run(ctx, phoneOwner, "/web"); err == nil {
		t.Error("/web without a page gave no error")
	}
}

// TestRouteSaysWhenTheChatIsNotKept checks the note on a route to a session that WhatsAgent does
// not run.
func TestRouteSaysWhenTheChatIsNotKept(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	for line, want := range map[string]string{
		"/route Team bot":  "gets nothing from it until you /keep it.",
		"/route * bot":     "gets messages only from kept chats.",
		"/route Team home": "now goes to home in the all mode.",
	} {
		if reply, err := h.core.Run(ctx, phoneOwner, line); err != nil || !strings.HasSuffix(reply, want) {
			t.Errorf("%s = %q, %v", line, reply, err)
		}
	}
	h.keep("g@g.us")
	if reply, _ := h.core.Run(ctx, phoneOwner, "/route Team bot"); strings.Contains(reply, "kept") {
		t.Errorf("/route of a kept chat = %q", reply)
	}
}

// TestRecentMessagesExpire checks that the buffer drops messages after Config.Recent.
func TestRecentMessagesExpire(t *testing.T) {
	h := newHarnessWith(t, func(c *Config) { c.Recent = 200 * time.Millisecond })
	h.keep("k@g.us")
	recent := h.receive("g@g.us", "short-lived", false)
	kept := h.receive("k@g.us", "kept", false)
	h.stored("g@g.us", recent.ID)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, found, _ := h.st.Message(h.ctx, "g@g.us", recent.ID); !found {
			if _, found, _ := h.st.Message(h.ctx, "k@g.us", kept.ID); !found {
				t.Error("the kept message expired too")
			}
			return
		}
	}
	t.Error("the recent message did not expire")
}
