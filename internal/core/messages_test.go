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

func TestEditAndDelete(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	h.keep("g@g.us", "sam@s.whatsapp.net")
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	bot := Caller{Session: "bot"}
	h.converse("bot", "g@g.us", model.RoleOwner)
	_, _ = h.core.Send(ctx, bot, "Team", "draft")
	mine := h.expect("draft")
	owners := h.receive("g@g.us", "the owner's words", true)
	theirs := h.receive("g@g.us", "someone else's words", false)

	// An agent edits WhatsAgent's message, which keeps the prefix, and the store follows.
	if out, err := h.core.Edit(ctx, bot, "Team", mine.id, "final"); err != nil || out != "edited" {
		t.Fatalf("Edit = %q, %v", out, err)
	}
	if edit := h.expect("final"); edit.kind != "edit" || edit.id != mine.id || edit.text != "🤖 *Agent*: final" {
		t.Errorf("edit %+v", edit)
	}
	if stored, _, _ := h.st.Message(ctx, "g@g.us", mine.id); stored.Text != "🤖 *Agent*: final" || !stored.Edited {
		t.Errorf("stored edit %+v", stored)
	}
	for name, err := range map[string]error{
		"the owner's message":    first(h.core.Edit(ctx, bot, "Team", owners.ID, "x")),
		"someone else's message": first(h.core.Delete(ctx, bot, "Team", theirs.ID)),
		"an empty text":          first(h.core.Edit(ctx, bot, "Team", mine.id, " ")),
		"a missing message":      first(h.core.Delete(ctx, bot, "Team", "missing")),
	} {
		if err == nil {
			t.Errorf("an agent changed %s", name)
		}
	}

	// The owner edits their own message, but not after 15 minutes and not a photo.
	if _, err := h.core.Edit(ctx, Owner, "Team", owners.ID, "better words"); err != nil {
		t.Fatal(err)
	}
	h.expect("better words")
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

	// Deleting: the agent its own message, the owner someone else's message in a group but not in a
	// direct chat, and nothing twice.
	if _, err := h.core.Delete(ctx, bot, "Team", mine.id); err != nil {
		t.Fatal(err)
	}
	if revoke := h.expectKind("revoke"); revoke.id != mine.id || revoke.text != "" {
		t.Errorf("revoke %+v", revoke)
	}
	if stored, _, _ := h.st.Message(ctx, "g@g.us", mine.id); !stored.Deleted {
		t.Error("the deletion was not stored")
	}
	if _, err := h.core.Edit(ctx, bot, "Team", mine.id, "again"); err == nil {
		t.Error("a deleted message was edited")
	}
	if _, err := h.core.Delete(ctx, Owner, "Team", theirs.ID); err != nil {
		t.Fatal(err)
	}
	if revoke := h.expectKind("revoke"); revoke.text != "200@s.whatsapp.net" {
		t.Errorf("the owner's revoke names %q as the sender", revoke.text)
	}
	direct := h.receive("sam@s.whatsapp.net", "hi", false)
	if _, err := h.core.Delete(ctx, Owner, "Sam", direct.ID); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("delete of someone else's direct message = %v", err)
	}
	if _, err := h.core.Delete(ctx, Owner, "", "x"); err == nil {
		t.Error("a delete without a chat gave no error")
	}

	// No agent changes one of WhatsAgent's own messages, such as an approval.
	_, _ = h.st.Apply(ctx, model.Update{Kind: model.New, Message: model.Message{Chat: "g@g.us", ID: "GEAR",
		FromMe: true, Role: model.RoleAgent, Text: "⚙️ Approval: ...", Time: time.Now()}})
	if _, err := h.core.Delete(ctx, bot, "Team", "GEAR"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("delete of WhatsAgent's own message = %v", err)
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
