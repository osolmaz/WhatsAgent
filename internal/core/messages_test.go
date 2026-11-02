package core

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

func TestRepliesAndMentions(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	h.keep("g@g.us", "sam@s.whatsapp.net")
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	bot := Caller{Session: "bot"}
	question := h.receive("g@g.us", "who has the keys?", false)

	// The owner's reply quotes the question, and a mention by phone number becomes a mention.
	if _, err := h.core.SendMessage(ctx, Owner, "Team", Draft{Text: "@15551234567 has them", ReplyTo: question.ID}); err != nil {
		t.Fatal(err)
	}
	sent := h.expect("has them")
	if sent.out.ReplyTo == nil || sent.out.ReplyTo.ID != question.ID || len(sent.out.Mentions) != 1 ||
		sent.out.Mentions[0] != "15551234567@s.whatsapp.net" {
		t.Errorf("sent %+v", sent.out)
	}
	if stored, _, _ := h.st.Message(ctx, "g@g.us", sent.id); stored.ReplyTo != question.ID || stored.Quote != "who has the keys?" {
		t.Errorf("stored reply %+v", stored)
	}

	// An agent's reply to another chat waits for a yes, and the question names the quoted message.
	h.converse("bot", "g@g.us", model.RoleOwner)
	if _, err := h.core.SendMessage(ctx, bot, "Team", Draft{Text: "noted", ReplyTo: "missing"}); err == nil {
		t.Error("a reply to a missing message was sent")
	}
	other := h.receive("sam@s.whatsapp.net", "ping", false)
	if _, err := h.core.SendMessage(ctx, Owner, "Sam", Draft{Text: "pong", ReplyTo: other.ID}); err != nil {
		t.Fatal(err)
	}
	h.expect("pong")
	if _, err := h.core.SendMessage(ctx, Caller{Session: homeName}, "Sam", Draft{Text: "pong again", ReplyTo: other.ID}); err != nil {
		t.Fatal(err)
	}
	h.expect(`send this message to "Sam" (direct chat), in reply to Sam's message "ping":`)

	// Audio has no caption, so the reply and the mentions go with its text.
	_, _ = h.core.SendMessage(ctx, Owner, "Team", Draft{Text: "listen @15551234567", ReplyTo: question.ID,
		File: &model.File{Name: "n.ogg", Data: []byte("ogg")}})
	if text := h.expect("listen"); text.out.ReplyTo == nil || len(text.out.Mentions) != 1 {
		t.Errorf("caption of audio = %+v", text.out)
	}
	if voice := h.expectFile("n.ogg"); voice.out.ReplyTo != nil || voice.text != "" {
		t.Errorf("audio = %+v", voice.out)
	}
}

// changeScene is a kept group "Team", routed to bot, with a message that WhatsAgent sent for bot,
// one from the owner and one from someone else, and a kept direct chat "Sam".
type changeScene struct {
	h              *harness
	bot            Caller
	mine           sentText
	owners, theirs model.Message
}

func newChangeScene(t *testing.T) changeScene {
	h := newHarness(t)
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	h.keep("g@g.us", "sam@s.whatsapp.net")
	_, _ = h.core.setRoute(h.ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	bot := Caller{Session: "bot"}
	h.converse("bot", "g@g.us", model.RoleOwner)
	_, _ = h.core.Send(h.ctx, bot, "Team", "draft")
	mine := h.expect("draft")
	return changeScene{h: h, bot: bot, mine: mine, owners: h.receive("g@g.us", "the owner's words", true),
		theirs: h.receive("g@g.us", "someone else's words", false)}
}

// asked checks that a change only asked the owner, and returns the code of the question that
// contains want.
func (h *harness) asked(want string) func(string, error) string {
	return func(out string, err error) string {
		h.t.Helper()
		if err != nil || !strings.Contains(out, "waiting for the owner's yes") {
			h.t.Fatalf("the change did not ask the owner: %q, %v", out, err)
		}
		return h.expectCode(want)
	}
}

// waiting reports whether the question with code still waits for an answer.
func (h *harness) waiting(code string) bool {
	h.core.mu.Lock()
	defer h.core.mu.Unlock()
	return h.core.pending[code] != nil
}

func TestEditsWaitForTheOwner(t *testing.T) {
	s := newChangeScene(t)
	h, ctx, bot := s.h, s.h.ctx, s.bot

	// An agent's edit waits for the owner's yes in its own conversation and under an allow rule, and
	// a "yes" that WhatsAgent sends approves nothing. The edit keeps the prefix, and the store follows.
	_ = h.st.SetRule(ctx, model.Rule{Session: "bot", Chat: "g@g.us", Permission: model.Allow})
	code := h.asked("Approval: the agent session bot wants to edit this message in \"Team\" (group):\n" +
		"🤖 *Agent*: draft\nto read:\n🤖 *Agent*: final")(h.core.Edit(ctx, bot, "Team", s.mine.id, "final"))
	_, _ = h.core.Send(ctx, Owner, "Team", "yes "+code)
	echo := h.expect("yes " + code)
	h.core.OnUpdate(model.Update{Kind: model.New, Message: model.Message{Chat: "g@g.us", ID: echo.id, FromMe: true,
		Text: "yes " + code, Time: time.Now().UTC()}})
	if !h.waiting(code) {
		t.Fatal("a yes that WhatsAgent sent approved the edit")
	}
	h.receive("g@g.us", "yes "+code, true)
	if edit := h.expectKind("edit"); edit.id != s.mine.id || edit.text != "🤖 *Agent*: final" {
		t.Errorf("edit %+v", edit)
	}
	if stored, _, _ := h.st.Message(ctx, "g@g.us", s.mine.id); stored.Text != "🤖 *Agent*: final" || !stored.Edited {
		t.Errorf("stored edit %+v", stored)
	}

	// The owner's edit through the CLI asks in the self-chat too.
	code = h.asked("Approval: the whatsagent CLI wants to edit this message")(
		h.core.Edit(ctx, Owner, "Team", s.owners.ID, "better words"))
	h.receive(self, "yes "+code, true)
	if edit := h.expectKind("edit"); edit.text != "better words" {
		t.Errorf("owner edit %+v", edit)
	}

	// After the yes, the edit checks the message again: here it was deleted while the question waited.
	code = h.asked("wants to edit")(h.core.Edit(ctx, bot, "Team", s.mine.id, "later"))
	h.core.OnUpdate(model.Update{Kind: model.Delete, Message: model.Message{Chat: "g@g.us", ID: s.mine.id}})
	h.receive("g@g.us", "yes "+code, true)
	h.expect("Failed: ")

	old := model.Message{Chat: "g@g.us", ID: "OLD", FromMe: true, Role: model.RoleOwner, Text: "old",
		Time: time.Now().Add(-time.Hour)}
	photo := model.Message{Chat: "g@g.us", ID: "IMG", FromMe: true, Role: model.RoleOwner, MediaType: "image",
		Time: time.Now()}
	for _, m := range []model.Message{old, photo} {
		_, _ = h.st.Apply(ctx, model.Update{Kind: model.New, Message: m})
		if _, err := h.core.Edit(ctx, Owner, "Team", m.ID, "new"); err == nil || !strings.Contains(err.Error(), "15m0s") {
			t.Errorf("edit of %s = %v", m.ID, err)
		}
	}
	_, _ = h.core.Send(ctx, bot, "Team", "spare")
	spare := h.expect("spare")
	_ = h.st.SetRule(ctx, model.Rule{Session: "bot", Chat: "g@g.us", Permission: model.Deny})
	if _, err := h.core.Edit(ctx, bot, "Team", spare.id, "x"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("edit in a chat that a rule denies = %v", err)
	}
	for name, err := range map[string]error{
		"the owner's message":    first(h.core.Edit(ctx, bot, "Team", s.owners.ID, "x")),
		"someone else's message": first(h.core.Edit(ctx, Owner, "Team", s.theirs.ID, "x")),
		"an empty text":          first(h.core.Edit(ctx, bot, "Team", s.mine.id, " ")),
		"a deleted message":      first(h.core.Edit(ctx, Owner, "Team", s.mine.id, "x")),
		"a message of no chat":   first(h.core.Edit(ctx, Owner, "", "x", "x")),
		"a missing message":      first(h.core.Edit(ctx, Owner, "Team", "missing", "x")),
	} {
		if err == nil {
			t.Errorf("an edit of %s gave no error", name)
		}
	}
}

func TestDeletesWaitForTheOwner(t *testing.T) {
	s := newChangeScene(t)
	h, ctx, bot := s.h, s.h.ctx, s.bot

	// An agent asks twice to delete its own message. The first yes deletes it, and the second fails,
	// because the deletion checks the message again after the yes.
	ask := h.asked("Approval: the agent session bot wants to delete this message")
	firstCode := ask(h.core.Delete(ctx, bot, "Team", s.mine.id))
	secondCode := ask(h.core.Delete(ctx, bot, "Team", s.mine.id))
	h.receive("g@g.us", "yes "+firstCode, true)
	if revoke := h.expectKind("revoke"); revoke.id != s.mine.id || revoke.text != "" {
		t.Errorf("revoke %+v", revoke)
	}
	if stored, _, _ := h.st.Message(ctx, "g@g.us", s.mine.id); !stored.Deleted {
		t.Error("the deletion was not stored")
	}
	h.receive("g@g.us", "yes "+secondCode, true)
	h.expect("Failed: message " + s.mine.id + " is deleted")

	// The owner's CLI deletes someone else's message in a group after a yes in the self-chat.
	code := h.asked("Approval: the whatsagent CLI wants to delete this message from Sam")(
		h.core.Delete(ctx, Owner, "Team", s.theirs.ID))
	h.receive(self, "yes "+code, true)
	if revoke := h.expectKind("revoke"); revoke.text != "200@s.whatsapp.net" {
		t.Errorf("the owner's revoke names %q as the sender", revoke.text)
	}

	// No agent changes one of WhatsAgent's own messages, such as an approval, and nobody deletes
	// someone else's message in a direct chat.
	_, _ = h.st.Apply(ctx, model.Update{Kind: model.New, Message: model.Message{Chat: "g@g.us", ID: "GEAR",
		FromMe: true, Role: model.RoleAgent, Text: "⚙️ Approval: ...", Time: time.Now()}})
	if _, err := h.core.Delete(ctx, bot, "Team", "GEAR"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("delete of WhatsAgent's own message = %v", err)
	}
	direct := h.receive("sam@s.whatsapp.net", "hi", false)
	if _, err := h.core.Delete(ctx, Owner, "Sam", direct.ID); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("delete of someone else's direct message = %v", err)
	}
	for name, err := range map[string]error{
		"someone else's message": first(h.core.Delete(ctx, bot, "Team", s.owners.ID)),
		"a missing message":      first(h.core.Delete(ctx, bot, "Team", "missing")),
		"a message of no chat":   first(h.core.Delete(ctx, Owner, "", "x")),
	} {
		if err == nil {
			t.Errorf("a delete of %s gave no error", name)
		}
	}
}

func TestForward(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	h.keep("g@g.us", "sam@s.whatsapp.net")
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	news := h.receive("g@g.us", "the venue moved", false)

	// The owner's forward goes after the comment, and the store marks it forwarded.
	if out, err := h.core.Forward(ctx, Owner, "Team", news.ID, "Sam", "fyi"); err != nil || !strings.HasPrefix(out, "forwarded") {
		t.Fatalf("Forward = %q, %v", out, err)
	}
	if comment := h.expect("fyi"); comment.chat != "sam@s.whatsapp.net" || comment.out.Forward != nil {
		t.Errorf("comment %+v", comment)
	}
	fwd := h.expect("")
	if fwd.out.Forward == nil || fwd.out.Forward.ID != news.ID {
		t.Fatalf("forward %+v", fwd)
	}
	if stored, _, _ := h.st.Message(ctx, "sam@s.whatsapp.net", fwd.id); !stored.Forwarded || stored.Text != "the venue moved" {
		t.Errorf("stored forward %+v", stored)
	}

	// An agent's forward to a chat outside its conversation waits for a yes, and its prefix goes
	// first even without a comment.
	bot := Caller{Session: "bot"}
	h.converse("bot", "g@g.us", model.RoleOwner)
	if out, _ := h.core.Forward(ctx, bot, "Team", news.ID, "Sam", ""); !strings.Contains(out, "waiting for the owner's yes") {
		t.Fatalf("agent Forward = %q", out)
	}
	code := h.expectCode("forward this message from Sam in \"Team\" (group) to \"Sam\" (direct chat):\nthe venue moved\n" +
		"after this message:\n🤖 *Agent*:")
	h.receive("g@g.us", "yes "+code, true)
	if prefix := h.expect("🤖 *Agent*:"); prefix.chat != "sam@s.whatsapp.net" {
		t.Errorf("prefix went to %s", prefix.chat)
	}
	if fwd := h.expect(""); fwd.out.Forward == nil {
		t.Errorf("no forward after the prefix: %+v", fwd)
	}

	_, _ = h.st.Apply(ctx, model.Update{Kind: model.New, Message: model.Message{Chat: "g@g.us", ID: "R1",
		ReactionTo: news.ID, Reaction: "👍", Time: time.Now()}})
	for name, err := range map[string]error{
		"a reaction":        first(h.core.Forward(ctx, Owner, "Team", "R1", "Sam", "")),
		"a missing message": first(h.core.Forward(ctx, Owner, "Team", "missing", "Sam", "")),
		"to no chat":        first(h.core.Forward(ctx, Owner, "Team", news.ID, "", "")),
		"from no chat":      first(h.core.Forward(ctx, Owner, "", news.ID, "Sam", "")),
	} {
		if err == nil {
			t.Errorf("forwarding %s gave no error", name)
		}
	}
}

// first returns the error of a call that returns a text and an error.
func first(_ string, err error) error { return err }

// expectKind waits for an edit or a deletion.
func (h *harness) expectKind(kind string) sentText {
	h.t.Helper()
	for {
		if s := h.expect(""); s.kind == kind {
			return s
		}
	}
}
