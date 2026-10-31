package model

import (
	"strings"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	rules := []Rule{
		{Session: "*", Chat: "*", Mode: Deny},
		{Session: "*", Chat: "sam", Mode: Ask},
		{Session: "bot", Chat: "*", Mode: Allow},
		{Session: "bot", Chat: "sam", Mode: Deny},
	}
	cases := []struct {
		session, chat string
		want          Mode
	}{
		{"bot", "sam", Deny},
		{"bot", "ann", Allow},
		{"other", "sam", Ask},
		{"other", "ann", Deny},
	}
	for _, c := range cases {
		if got := Decide(rules, c.session, c.chat, false); got != c.want {
			t.Errorf("Decide(%s, %s) = %s, want %s", c.session, c.chat, got, c.want)
		}
	}
	if got := Decide(nil, "a", "b", true); got != Allow {
		t.Errorf("routed default = %s, want allow", got)
	}
	if got := Decide(nil, "a", "b", false); got != Ask {
		t.Errorf("unrouted default = %s, want ask", got)
	}
}

func TestParseMode(t *testing.T) {
	if m, err := ParseMode("ask"); err != nil || m != Ask {
		t.Errorf("ParseMode(ask) = %s, %v", m, err)
	}
	if _, err := ParseMode("maybe"); err == nil {
		t.Error("ParseMode(maybe) gave no error")
	}
}

func TestTrust(t *testing.T) {
	for role, want := range map[Role]string{RoleOwner: "owner", RoleCommand: "owner", RoleOther: "other", RoleAgent: "other"} {
		if got := role.Trust(); got != want {
			t.Errorf("%s.Trust() = %s, want %s", role, got, want)
		}
	}
}

func TestBodyAndLine(t *testing.T) {
	at := time.Date(2026, 11, 1, 6, 3, 0, 0, time.UTC)
	m := Message{ID: "A1", Sender: "111@s.whatsapp.net", SenderName: "Maya", Time: at,
		Text: "hi @15551234567 and @999999", Mentions: map[string]string{"15551234567@s.whatsapp.net": "Sam", "999999@lid": ""},
		Quote: "an earlier\nmessage", Edited: true}
	line := Line(m, "Team", time.UTC)
	for _, want := range []string{"2026-11-01 06:03 [Team] Maya: hi @Sam and @999999", `(reply to "an earlier message")`, "(edited)", "[id=A1 from=111@s.whatsapp.net]"} {
		if !strings.Contains(line, want) {
			t.Errorf("Line() = %q, missing %q", line, want)
		}
	}
	cases := map[string]Message{
		"[deleted]":          {Deleted: true, Text: "x"},
		"[reaction 👍 to B2]": {Reaction: "👍", ReactionTo: "B2"},
		"[image] caption":    {MediaType: "image", Text: "caption"},
		"[audio]":            {MediaType: "audio"},
		"plain":              {Text: "plain"},
	}
	for want, m := range cases {
		if got := m.Body(); got != want {
			t.Errorf("Body() = %q, want %q", got, want)
		}
	}
	if got := senderLabel(Message{Role: RoleAgent}); got != "WhatsAgent" {
		t.Errorf("agent label = %q", got)
	}
	if got := senderLabel(Message{FromMe: true}); got != "owner" {
		t.Errorf("own label = %q", got)
	}
	if got := senderLabel(Message{Sender: "x@s"}); got != "x@s" {
		t.Errorf("bare label = %q", got)
	}
	if got := clip(strings.Repeat("a", 90), 80); len([]rune(got)) != 81 {
		t.Errorf("clip length = %d", len([]rune(got)))
	}
}

func TestChatLabelAndSortRoutes(t *testing.T) {
	if (Chat{JID: "j"}).Label() != "j" || (Chat{JID: "j", Name: "n"}).Label() != "n" {
		t.Error("Label() is wrong")
	}
	routes := []Route{{Chat: "b", Session: "y"}, {Chat: "c", Session: "x"}, {Chat: "a", Session: "y"}}
	SortRoutes(routes)
	if routes[0].Chat != "c" || routes[1].Chat != "a" || routes[2].Chat != "b" {
		t.Errorf("SortRoutes = %+v", routes)
	}
}
