package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/sqlitedb"
)

func TestChatModesWindowsAgents(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if _, ok, err := s.ChatMode(ctx, "g@g.us"); ok || err != nil {
		t.Fatalf("missing ChatMode = %v, %v", ok, err)
	}
	_ = s.SetChatMode(ctx, model.ChatMode{Chat: "*", Mode: model.ModeWindow})
	_ = s.SetChatMode(ctx, model.ChatMode{Chat: "g@g.us", Mode: model.ModeAll})
	_ = s.SetChatMode(ctx, model.ChatMode{Chat: "g@g.us", Mode: model.ModeWindow, Length: 2 * time.Hour, Lookback: time.Hour})
	if m, ok, _ := s.ChatMode(ctx, "g@g.us"); !ok || m.Mode != model.ModeWindow || m.Length != 2*time.Hour || m.Lookback != time.Hour {
		t.Errorf("ChatMode = %+v", m)
	}
	if modes, _ := s.ChatModes(ctx); len(modes) != 2 || modes[0].Chat != "*" {
		t.Errorf("ChatModes = %+v", modes)
	}
	_ = s.DeleteChatMode(ctx, "g@g.us")
	if _, ok, _ := s.ChatMode(ctx, "g@g.us"); ok {
		t.Error("DeleteChatMode left the mode")
	}

	at := time.Unix(1000, 0).UTC()
	w := model.Window{Chat: "g@g.us", Session: "chat-team", Opened: at, From: at.Add(-time.Minute), Until: at.Add(time.Hour)}
	_ = s.SetWindow(ctx, w)
	if got, ok, _ := s.Window(ctx, "g@g.us"); !ok || got != w {
		t.Errorf("Window = %+v, want %+v", got, w)
	}
	if windows, _ := s.Windows(ctx); len(windows) != 1 {
		t.Errorf("Windows = %+v", windows)
	}
	if closed, _ := s.DeleteWindow(ctx, "g@g.us"); !closed {
		t.Error("DeleteWindow found no window")
	}
	if _, ok, _ := s.Window(ctx, "g@g.us"); ok {
		t.Error("the window is still open")
	}

	if name, _ := s.ChatAgent(ctx, "g@g.us"); name != "" {
		t.Errorf("ChatAgent before SetChatAgent = %q", name)
	}
	_ = s.SetChatAgent(ctx, "g@g.us", "chat-team")
	if err := s.SetChatAgent(ctx, "h@g.us", "chat-team"); err == nil {
		t.Error("two chats got the same agent")
	}
	_ = s.SetWindow(ctx, w)
	_ = s.RenameSession(ctx, "chat-team", "bob-team")
	if name, _ := s.ChatAgent(ctx, "g@g.us"); name != "bob-team" {
		t.Errorf("ChatAgent after rename = %q", name)
	}
	if chat, _ := s.AgentChat(ctx, "bob-team"); chat != "g@g.us" {
		t.Errorf("AgentChat = %q", chat)
	}
	if got, _, _ := s.Window(ctx, "g@g.us"); got.Session != "bob-team" {
		t.Errorf("window session after rename = %q", got.Session)
	}

	for i, at := range []int{100, 200, 300} {
		_, _ = s.Apply(ctx, model.Update{Kind: model.New, Message: msg("g@g.us", string(rune('a'+i)), "x", at)}, true)
	}
	got, err := s.MessagesBetween(ctx, "g@g.us", time.Unix(150, 0), time.Unix(300, 0), 0)
	if err != nil || len(got) != 2 || got[0].ID != "b" {
		t.Errorf("MessagesBetween = %+v, %v", got, err)
	}
	if got, _ := s.MessagesBetween(ctx, "g@g.us", time.Unix(0, 0), time.Unix(300, 0), 1); len(got) != 1 || got[0].ID != "c" {
		t.Errorf("MessagesBetween with a limit = %+v", got)
	}
}

func TestMigrateFromFirstSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(ctx, db, 1, schemaV1); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"INSERT INTO routes (chat, session, prefix, addressed) VALUES ('a@g.us', 's', '', 1), ('b@g.us', 's', 'P: ', 0)",
		"INSERT INTO send_rules (session, chat, mode) VALUES ('s', '*', 'deny')",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if m, _, _ := s.ChatMode(ctx, "a@g.us"); m.Mode != model.ModeKeyword {
		t.Errorf("addressed route became %q", m.Mode)
	}
	if m, _, _ := s.ChatMode(ctx, "b@g.us"); m.Mode != model.ModeAll {
		t.Errorf("plain route became %q", m.Mode)
	}
	if routes, _ := s.Routes(ctx); len(routes) != 2 || routes[0].Prefix != "" || routes[1].Prefix != "P: " {
		t.Errorf("Routes = %+v", routes)
	}
	if rules, _ := s.Rules(ctx); len(rules) != 1 || rules[0].Permission != model.Deny {
		t.Errorf("Rules = %+v", rules)
	}
}
