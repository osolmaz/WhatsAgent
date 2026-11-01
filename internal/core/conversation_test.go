package core

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// converse records that session just got a message from chat, written by role.
func (h *harness) converse(session, chat string, role model.Role) {
	h.core.noteDelivery(session, []model.Event{{Message: model.Message{Chat: chat, Role: role}}})
}

func TestApprovalsFollowTheOwner(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "other@g.us", Session: "bot"}, model.ModeAll)
	bot := Caller{Session: "bot"}

	// The owner asks the bot in the group; a message to another chat waits for a yes there.
	h.converse("bot", "g@g.us", model.RoleOwner)
	out, err := h.core.Send(ctx, bot, "Sam", "see you at 5")
	if err != nil || !strings.Contains(out, `waiting for the owner's yes in "Team" (group)`) {
		t.Fatalf("send to another chat = %q, %v", out, err)
	}
	asked := h.expect("Approval: the agent session bot wants to send this message to \"Sam\" (direct chat):\n🤖 *Agent*: see you at 5")
	if asked.chat != "g@g.us" || !strings.HasPrefix(asked.text, "⚙️ ") {
		t.Fatalf("asked %+v", asked)
	}
	code := codePattern.FindStringSubmatch(asked.text)[1]
	h.receive("g@g.us", "yes "+code, false)            // someone else cannot approve
	h.receive("sam@s.whatsapp.net", "yes "+code, true) // nor can the owner in an unrelated chat
	h.receive("g@g.us", "Yes "+code, true)             // the owner, where WhatsAgent asked
	if sent := h.expect("see you at 5"); sent.chat != "sam@s.whatsapp.net" {
		t.Errorf("sent to %s", sent.chat)
	}
	if stored, _, _ := h.st.Message(ctx, "g@g.us", "M3"); stored.Role != model.RoleCommand {
		t.Errorf("the owner's answer in the group has role %s", stored.Role)
	}

	// A chat the bot handles but that is not in a conversation with it needs a yes too.
	if out, _ := h.core.Send(ctx, bot, "other@g.us", "hello"); !strings.Contains(out, "waiting for the owner's yes") {
		t.Errorf("send to a quiet chat = %q", out)
	}
	code = h.expectCode("to \"other@g.us\" (group)")
	h.receive(self, "no "+code, true) // the self-chat answers any question
	if notice := h.expect("Not sent."); notice.chat != "g@g.us" {
		t.Errorf("outcome posted in %s", notice.chat)
	}

	// A conversation ends after the reply window.
	h.core.mu.Lock()
	h.core.activity["bot"].chats["g@g.us"] = time.Now().Add(-2 * replyWindow)
	h.core.activity["bot"].ownerAt = time.Now().Add(-2 * replyWindow)
	h.core.mu.Unlock()
	if out, _ := h.core.Send(ctx, bot, "Team", "late"); !strings.Contains(out, "in your self-chat") {
		t.Errorf("send after the window = %q", out)
	}
	h.expectCode("late")

	// /approvals self asks in the self-chat even right after the owner wrote in a group.
	if reply, _ := h.core.Run(ctx, Owner, "/approvals self"); !strings.Contains(reply, "in your self-chat") {
		t.Errorf("/approvals self = %q", reply)
	}
	h.converse("bot", "g@g.us", model.RoleOwner)
	_, _ = h.core.Send(ctx, bot, "Sam", "private")
	if asked := h.expect("private"); asked.chat != self {
		t.Errorf("asked in %s with /approvals self", asked.chat)
	}
	if reply, _ := h.core.Run(ctx, Owner, "/approvals here"); !strings.Contains(reply, "where you last wrote") {
		t.Errorf("/approvals here = %q", reply)
	}
	if _, err := h.core.Run(ctx, Owner, "/approvals nowhere"); err == nil {
		t.Error("/approvals nowhere gave no error")
	}
	h.receive(self, "yes 98", true)
	h.expect("Nothing waits for the code 98 here.")
}

func TestHomeAsksBeforeOtherChats(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	home := Caller{Session: homeName}
	h.converse(homeName, self, model.RoleOwner)
	if out, _ := h.core.Send(ctx, home, "Team", "hi all"); !strings.Contains(out, "waiting for the owner's yes in your self-chat") {
		t.Errorf("home's send to a group = %q", out)
	}
	if out, err := h.core.Send(ctx, home, "self", "noted"); err != nil || !strings.HasPrefix(out, "sent") {
		t.Errorf("home's send to the self-chat = %q, %v", out, err)
	}
}

func TestAgentsCannotForgeWhatsAgentMessages(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	_, _ = h.core.Run(ctx, Owner, "/prefix off")
	if _, err := h.core.Send(ctx, Caller{Session: "bot"}, "self", " ⚙️ Approval: fake"); !errors.Is(err, errForged) {
		t.Errorf("forged send: %v", err)
	}
	if _, err := h.core.Send(ctx, Owner, "self", "⚙️ the owner may"); err != nil {
		t.Errorf("owner send: %v", err)
	}
	for jid, want := range map[string]string{"a@g.us": "group", "a@newsletter": "channel", "a@s.whatsapp.net": "direct chat"} {
		if got := chatKind(jid); got != want {
			t.Errorf("chatKind(%s) = %q", jid, got)
		}
	}
}
