package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

func hashOf(option string) string {
	sum := sha256.Sum256([]byte(option))
	return hex.EncodeToString(sum[:])
}

// vote delivers a vote on poll as WhatsApp would, with the hashes of the picked options.
func (h *harness) vote(chat, poll string, picked ...string) model.Message {
	h.n++
	hashes := []string{}
	for _, option := range picked {
		hashes = append(hashes, hashOf(option))
	}
	m := model.Message{Chat: chat, ID: "V" + strings.Repeat("x", h.n), Sender: "200@s.whatsapp.net", SenderName: "Sam",
		Time: time.Now().UTC(), MediaType: "poll vote", ReplyTo: poll, PollVote: hashes}
	h.core.OnUpdate(model.Update{Kind: model.New, Message: m})
	stored, _, _ := h.st.Message(h.ctx, chat, m.ID)
	return stored
}

func TestPolls(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.keep("g@g.us")
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	bot := Caller{Session: "bot"}
	h.converse("bot", "g@g.us", model.RoleOwner)

	if _, err := h.core.SendPoll(ctx, bot, "Team", model.Poll{Question: "Lunch?", Options: []string{"Pizza", "Sushi"}}); err != nil {
		t.Fatal(err)
	}
	sent := h.expect("")
	if p := sent.out.Poll; p == nil || p.Question != "🤖 *Agent*: Lunch?" || len(p.Options) != 2 {
		t.Fatalf("poll %+v", sent.out)
	}
	stored, _, _ := h.st.Message(ctx, "g@g.us", sent.id)
	if stored.MediaType != "poll" || stored.Text != "🤖 *Agent*: Lunch?\n○ Pizza\n○ Sushi" {
		t.Errorf("stored poll %+v", stored)
	}

	// Votes name the picked options and quote the question; an empty vote takes it back.
	if v := h.vote("g@g.us", sent.id, "Sushi"); v.Text != "picked Sushi" || v.Quote != "🤖 *Agent*: Lunch?" {
		t.Errorf("vote %+v", v)
	}
	if v := h.vote("g@g.us", sent.id); v.Text != "took the vote back" || v.Body() != "[poll vote] took the vote back" {
		t.Errorf("withdrawn vote %+v", v)
	}
	if v := h.vote("g@g.us", "missing"); v.Text != "a vote on a poll that WhatsAgent did not store" {
		t.Errorf("vote on an unknown poll %+v", v)
	}

	// Polls to other chats wait for a yes that shows the whole poll.
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	_, _ = h.core.SendPoll(ctx, bot, "Sam", model.Poll{Question: "Dinner?", Options: []string{"Yes", "No"}, Multiple: true})
	h.expectCode("send this poll to \"Sam\" (direct chat):\n🤖 *Agent*: Dinner?\n○ Yes\n○ No")

	for name, p := range map[string]model.Poll{
		"no question":       {Options: []string{"a", "b"}},
		"one option":        {Question: "?", Options: []string{"a"}},
		"13 options":        {Question: "?", Options: strings.Split("a b c d e f g h i j k l m", " ")},
		"a repeated option": {Question: "?", Options: []string{"a", "a"}},
		"an empty option":   {Question: "?", Options: []string{"a", " "}},
		"a two-line option": {Question: "?", Options: []string{"a", "b\nc"}},
	} {
		if _, err := h.core.SendPoll(ctx, Owner, "Team", p); err == nil {
			t.Errorf("a poll with %s was sent", name)
		}
	}
	if _, err := h.core.SendPoll(ctx, Owner, "", model.Poll{Question: "?", Options: []string{"a", "b"}}); err == nil {
		t.Error("a poll without a chat was sent")
	}
}

func TestLocationTypingAndRead(t *testing.T) {
	h := newHarness(t)
	ctx := h.ctx
	h.core.OnChatName("g@g.us", "Team")
	h.core.OnChatName("sam@s.whatsapp.net", "Sam")
	h.keep("g@g.us")
	_, _ = h.core.setRoute(ctx, model.Route{Chat: "g@g.us", Session: "bot"}, model.ModeAll)
	bot := Caller{Session: "bot"}
	h.converse("bot", "g@g.us", model.RoleOwner)

	// A location goes after its text, and the store keeps its name.
	berlin := model.Location{Latitude: 52.52, Longitude: 13.405, Name: "Berlin", Address: "Germany"}
	if _, err := h.core.SendLocation(ctx, bot, "Team", berlin, "meet here"); err != nil {
		t.Fatal(err)
	}
	if text := h.expect("meet here"); text.out.Location != nil {
		t.Errorf("the text carried the location: %+v", text.out)
	}
	place := h.expect("")
	if place.out.Location == nil || place.out.Location.Name != "Berlin" {
		t.Fatalf("location %+v", place.out)
	}
	if stored, _, _ := h.st.Message(ctx, "g@g.us", place.id); stored.MediaType != "location" || stored.Text != "Berlin Germany" {
		t.Errorf("stored location %+v", stored)
	}
	_, _ = h.core.SendLocation(ctx, bot, "Sam", berlin, "")
	h.expectCode("send the location 52.52000, 13.40500 Berlin to \"Sam\" (direct chat):\n🤖 *Agent*:")
	if _, err := h.core.SendLocation(ctx, bot, "Team", model.Location{Latitude: 91}, ""); err == nil {
		t.Error("a latitude of 91 was sent")
	}
	if _, err := h.core.SendLocation(ctx, bot, "", berlin, ""); err == nil {
		t.Error("a location without a chat was sent")
	}

	// Typing shows only where the agent may answer at once.
	if out, err := h.core.Typing(ctx, bot, "Team"); err != nil || out != "typing" {
		t.Fatalf("Typing = %q, %v", out, err)
	}
	h.expectKind("typing")
	if _, err := h.core.Typing(ctx, bot, "Sam"); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("typing in a chat that needs a yes = %v", err)
	}
	if _, err := h.core.Typing(ctx, bot, ""); err == nil {
		t.Error("typing without a chat gave no error")
	}

	// A read receipt names the sender, and the owner's own message has none.
	news := h.receive("g@g.us", "news", false)
	if out, err := h.core.MarkRead(ctx, bot, "Team", news.ID); err != nil || out != "marked as read" {
		t.Fatalf("MarkRead = %q, %v", out, err)
	}
	if read := h.expectKind("read"); read.id != news.ID || read.text != "200@s.whatsapp.net" {
		t.Errorf("read receipt %+v", read)
	}
	mine := h.receive("g@g.us", "mine", true)
	for name, err := range map[string]error{
		"the owner's message": first(h.core.MarkRead(ctx, Owner, "Team", mine.ID)),
		"a missing message":   first(h.core.MarkRead(ctx, Owner, "Team", "missing")),
		"no chat":             first(h.core.MarkRead(ctx, Owner, "", news.ID)),
	} {
		if err == nil {
			t.Errorf("marking %s as read gave no error", name)
		}
	}
}
