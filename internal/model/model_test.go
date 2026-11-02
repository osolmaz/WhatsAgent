package model

import (
	"strings"
	"testing"
	"time"
)

func TestMediaDescriptions(t *testing.T) {
	for _, c := range []struct {
		media Media
		want  string
	}{
		{Media{Type: "image", Filename: "a.jpg", Size: 84_000}, "photo a.jpg (84 KB)"},
		{Media{Type: "video", Filename: "v.mp4", Size: 1_250_000}, "video v.mp4 (1.2 MB)"},
		{Media{Type: "audio", Filename: "n.ogg", Size: 2_000_000}, "audio file n.ogg (2 MB)"},
		{Media{Type: "document", Filename: "r.pdf", Size: 999}, "document r.pdf (999 bytes)"},
		{Media{Type: "sticker", Filename: "s.webp", Size: 1_000_000}, "document s.webp (1 MB)"},
	} {
		if got := c.media.Describe(); got != c.want {
			t.Errorf("Describe() = %q, want %q", got, c.want)
		}
	}
	if (Media{Type: "audio"}).Captioned() || !(Media{Type: "image"}).Captioned() {
		t.Error("Captioned is wrong")
	}
}

func TestDecide(t *testing.T) {
	rules := []Rule{
		{Session: "*", Chat: "*", Permission: Deny},
		{Session: "*", Chat: "sam", Permission: Ask},
		{Session: "bot", Chat: "*", Permission: Allow},
		{Session: "bot", Chat: "sam", Permission: Deny},
	}
	cases := []struct {
		session, chat string
		want          Permission
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

func TestParsePermission(t *testing.T) {
	if p, err := ParsePermission("ask"); err != nil || p != Ask {
		t.Errorf("ParsePermission(ask) = %s, %v", p, err)
	}
	if _, err := ParsePermission("maybe"); err == nil {
		t.Error("ParsePermission(maybe) gave no error")
	}
}

func TestParseMode(t *testing.T) {
	if m, err := ParseMode("Window"); err != nil || m != ModeWindow {
		t.Errorf("ParseMode(Window) = %s, %v", m, err)
	}
	if _, err := ParseMode("maybe"); err == nil {
		t.Error("ParseMode(maybe) gave no error")
	}
}

func TestDurations(t *testing.T) {
	for in, want := range map[string]time.Duration{"30m": 30 * time.Minute, "1H": time.Hour, "1d": 24 * time.Hour, "1h30m": 90 * time.Minute} {
		if d, err := ParseDuration(in); err != nil || d != want {
			t.Errorf("ParseDuration(%q) = %v, %v", in, d, err)
		}
	}
	for _, in := range []string{"", "0m", "-1h", "xd", "soon"} {
		if _, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) gave no error", in)
		}
	}
	for d, want := range map[time.Duration]string{time.Minute: "1 minute", 30 * time.Minute: "30 minutes",
		time.Hour: "1 hour", 90 * time.Minute: "90 minutes", 48 * time.Hour: "2 days"} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", d, got, want)
		}
	}
	w := Window{From: time.Unix(100, 0), Until: time.Unix(200, 0)}
	if !w.Covers(time.Unix(100, 0)) || !w.Covers(time.Unix(200, 0)) || w.Covers(time.Unix(201, 0)) || w.Covers(time.Unix(99, 0)) {
		t.Error("Window.Covers is wrong at its edges")
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
