package core

import (
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

func TestMentions(t *testing.T) {
	for text, want := range map[string]bool{
		"Bob, look":         true,
		"thanks @bob":       true,
		"is that bob's?":    true,
		"ask BOB.":          true,
		"Bobby said so":     false,
		"kabob for lunch":   false,
		"bob_two is a name": false,
	} {
		if got := mentions(text, "Bob"); got != want {
			t.Errorf("mentions(%q, Bob) = %v", text, got)
		}
	}
	if !mentions("merhaba ömer!", "Ömer") {
		t.Error("a name with a non-ASCII letter was not found")
	}
	if mentions("anything", "") {
		t.Error("an empty word matched")
	}
}

func TestAgentNameAndPrefix(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	if reply, _ := h.core.Run(ctx, Owner, "/name"); reply != "The agents are called Agent." {
		t.Errorf("/name = %q", reply)
	}
	if reply, _ := h.core.Run(ctx, Owner, "/prefix"); !strings.HasSuffix(reply, "🤖 *Agent*: On it.") {
		t.Errorf("/prefix = %q", reply)
	}
	if _, err := h.core.Run(ctx, Owner, "/name Bob\nSmith"); err == nil {
		t.Error("a name on two lines gave no error")
	}
	if reply, err := h.core.Run(ctx, Owner, "/name Bob"); err != nil || !strings.Contains(reply, "now called Bob") {
		t.Fatalf("/name Bob = %q, %v", reply, err)
	}
	if s, _ := h.core.Status(ctx); s.AgentName != "Bob" {
		t.Errorf("Status.AgentName = %q", s.AgentName)
	}

	// The CLI's --session sends get the prefix; the owner's own sends do not.
	helper := As("helper-team")
	if _, err := h.core.Send(ctx, helper, "Team", "on it"); err != nil {
		t.Fatal(err)
	}
	h.expectText("🤖 *Bob*: on it")
	_, _ = h.core.Send(ctx, helper, "Team", "🤖 *Bob*: typed it anyway")
	h.expectText("🤖 *Bob*: typed it anyway")
	_, _ = h.core.Send(ctx, Owner, "Team", "from me")
	h.expectText("from me")

	// Any text works, with WhatsApp formatting, the session's name and a line break.
	reply, _ := h.core.Run(ctx, Owner, `/prefix > *{name}* (_{session}_)\n`)
	if !strings.HasSuffix(reply, "> *Bob* (_billing_)\nOn it.") {
		t.Errorf("/prefix with a line break = %q", reply)
	}
	_, _ = h.core.Send(ctx, helper, "Team", "done")
	h.expectText("> *Bob* (_helper-team_)\ndone")

	// A route's own prefix wins for that chat.
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot", Prefix: "[{name}/{session}]"}, model.ModeAll)
	h.converse("bot", "g@g.us", model.RoleOther)
	_, _ = h.core.Send(ctx, Caller{Session: "bot"}, "Team", "routed")
	h.expectText("[Bob/bot] routed")

	_, _ = h.core.Run(ctx, Owner, "/prefix off")
	_, _ = h.core.Send(ctx, helper, "self", "plain")
	h.expectText("plain")
}

func TestAutoApproval(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	if reply, _ := h.core.Run(ctx, Owner, "/auto"); !strings.HasPrefix(reply, "Auto approval is on") {
		t.Errorf("/auto = %q", reply)
	}
	_, _ = h.core.Run(ctx, Owner, "/home claude")
	h.core.OnChatName("g@g.us", "Team")
	_, _ = h.core.Run(ctx, Owner, "/mode Team all")
	h.receive("g@g.us", "hello", false)
	agent := h.agent("chat-team")
	if !agent.host.Auto {
		t.Error("a chat agent started without auto approval")
	}
	if reply, err := h.core.Run(ctx, Owner, "/auto off"); err != nil || !strings.HasPrefix(reply, "Auto approval is off") {
		t.Fatalf("/auto off = %q, %v", reply, err)
	}
	if h.running("chat-team") {
		t.Error("/auto off left the chat agent running")
	}
	h.receive("g@g.us", "again", false)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		restarted := agent.starts == 2 && !agent.host.Auto
		h.mu.Unlock()
		if restarted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if agent.starts != 2 || agent.host.Auto {
		t.Errorf("after /auto off: starts = %d, auto = %v", agent.starts, agent.host.Auto)
	}
	if _, err := h.core.Run(ctx, Owner, "/auto maybe"); err == nil {
		t.Error("/auto maybe gave no error")
	}
}

// expectText waits for a sent message and checks that its text is exactly want.
func (h *harness) expectText(want string) {
	h.t.Helper()
	if s := h.expect(want); s.text != want {
		h.t.Errorf("sent %q, want %q", s.text, want)
	}
}
