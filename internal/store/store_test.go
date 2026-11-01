package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "whatsagent.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func msg(chat, id, text string, at int) model.Message {
	return model.Message{Chat: chat, ID: id, Sender: "111@s.whatsapp.net", SenderName: "Maya",
		Time: time.Unix(int64(at), 0).UTC(), Role: model.RoleOther, Text: text}
}

func TestApplyAndQueries(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	m := msg("g@g.us", "m1", "hello world", 100)
	m.Mentions = map[string]string{"1@s.whatsapp.net": "Sam"}
	m.Raw = []byte{1, 2}
	seq, err := s.Apply(ctx, model.Update{Kind: model.New, Message: m}, true)
	if err != nil || seq != 1 {
		t.Fatalf("Apply = %d, %v", seq, err)
	}
	if seq, _ := s.Apply(ctx, model.Update{Kind: model.New, Message: m}, true); seq != 0 {
		t.Errorf("duplicate Apply = %d, want 0", seq)
	}
	if _, err := s.Apply(ctx, model.Update{Kind: model.New, Message: msg("g@g.us", "m2", "second 100%", 200)}, false); err != nil {
		t.Fatal(err)
	}
	if seq, _ := s.Apply(ctx, model.Update{Kind: model.Edit, Message: model.Message{Chat: "g@g.us", ID: "m1", Text: "edited"}}, true); seq != 3 {
		t.Errorf("edit seq = %d", seq)
	}
	if seq, _ := s.Apply(ctx, model.Update{Kind: model.Delete, Message: model.Message{Chat: "g@g.us", ID: "m2"}}, true); seq != 4 {
		t.Errorf("delete seq = %d", seq)
	}
	if seq, _ := s.Apply(ctx, model.Update{Kind: model.Delete, Message: model.Message{Chat: "g@g.us", ID: "m2"}}, true); seq != 0 {
		t.Errorf("second delete seq = %d", seq)
	}
	if _, err := s.Apply(ctx, model.Update{Kind: "bad", Message: m}, true); err == nil {
		t.Error("bad kind gave no error")
	}

	got, found, err := s.Message(ctx, "g@g.us", "m1")
	if err != nil || !found || got.Text != "edited" || !got.Edited || got.Mentions["1@s.whatsapp.net"] != "Sam" || len(got.Raw) != 2 {
		t.Errorf("Message = %+v, %v, %v", got, found, err)
	}
	if _, found, _ := s.Message(ctx, "g@g.us", "nope"); found {
		t.Error("missing message found")
	}
	all, _ := s.Messages(ctx, "g@g.us", 0)
	last, _ := s.Messages(ctx, "g@g.us", 1)
	if len(all) != 2 || len(last) != 1 || last[0].ID != "m2" || !last[0].Deleted {
		t.Errorf("Messages = %+v / %+v", all, last)
	}
	if found, _ := s.Search(ctx, "100%", "", 10); len(found) != 0 {
		t.Errorf("deleted text still found: %+v", found)
	}
	if found, _ := s.Search(ctx, "edit", "g@g.us", 10); len(found) != 1 {
		t.Errorf("Search = %+v", found)
	}

	events, err := s.Events(ctx, 0, []string{"g@g.us"}, 10)
	if err != nil || len(events) != 3 || events[0].Kind != model.New || events[1].Kind != model.Edit {
		t.Errorf("Events = %+v, %v", events, err)
	}
	if every, _ := s.Events(ctx, 1, []string{"*"}, 10); len(every) != 2 {
		t.Errorf("Events(*) = %d", len(every))
	}
	if none, _ := s.Events(ctx, 0, nil, 10); none != nil {
		t.Error("Events(nil chats) returned events")
	}
	chats, next, err := s.ChangedChats(ctx, 2)
	if err != nil || len(chats) != 1 || next != 4 {
		t.Errorf("ChangedChats = %v, %d, %v", chats, next, err)
	}
}

func TestChatsRoutesRulesSessions(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_ = s.SetChatName(ctx, "g@g.us", "Team <> Sam")
	_, _ = s.Apply(ctx, model.Update{Kind: model.New, Message: msg("g@g.us", "m1", "x", 100)}, true)
	if c, _ := s.Chat(ctx, "g@g.us"); c.Name != "Team <> Sam" || c.LastTime.IsZero() {
		t.Errorf("Chat = %+v", c)
	}
	if c, _ := s.Chat(ctx, "none@g.us"); c.Name != "" {
		t.Errorf("unknown Chat = %+v", c)
	}
	if found, _ := s.Chats(ctx, "<>", 5); len(found) != 1 {
		t.Errorf("Chats = %+v", found)
	}

	_ = s.SetRoute(ctx, model.Route{Chat: "g@g.us", Session: "a", Prefix: "P: "})
	_ = s.SetRoute(ctx, model.Route{Chat: "g@g.us", Session: "b"})
	routes, _ := s.Routes(ctx)
	if len(routes) != 1 || routes[0].Session != "b" || routes[0].Prefix != "" {
		t.Errorf("Routes = %+v", routes)
	}
	_ = s.SetRule(ctx, model.Rule{Session: "b", Chat: "*", Permission: model.Allow})
	_ = s.SetRule(ctx, model.Rule{Session: "b", Chat: "*", Permission: model.Deny})
	if rules, _ := s.Rules(ctx); len(rules) != 1 || rules[0].Permission != model.Deny {
		t.Errorf("Rules = %+v", rules)
	}

	if err := s.TouchSession(ctx, model.Session{Name: "b", Harness: "claude", Folder: "/x", LastSeen: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if cursor, _ := s.Cursor(ctx, "b"); cursor != 1 {
		t.Errorf("new session cursor = %d, want the newest event", cursor)
	}
	_ = s.SetCursor(ctx, "b", 0)
	if cursor, _ := s.Cursor(ctx, "b"); cursor != 0 {
		t.Errorf("cursor after SetCursor = %d", cursor)
	}
	if _, err := s.Cursor(ctx, "nobody"); err == nil {
		t.Error("unknown session cursor gave no error")
	}
	if err := s.RenameSession(ctx, "b", "c"); err != nil {
		t.Fatal(err)
	}
	routes, _ = s.Routes(ctx)
	sessions, _ := s.Sessions(ctx)
	rules, _ := s.Rules(ctx)
	if routes[0].Session != "c" || sessions[0].Name != "c" || rules[0].Session != "c" {
		t.Errorf("rename left %+v %+v %+v", routes, sessions, rules)
	}
	if removed, _ := s.DeleteRoute(ctx, "g@g.us"); !removed {
		t.Error("DeleteRoute found no route")
	}
	if removed, _ := s.DeleteRoute(ctx, "g@g.us"); removed {
		t.Error("second DeleteRoute removed a route")
	}
}

func TestSentAndSettings(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_ = s.RecordSent(ctx, "S1", "g@g.us", time.Now())
	if sent, _ := s.IsSent(ctx, "S1"); !sent {
		t.Error("IsSent(S1) = false")
	}
	if sent, _ := s.IsSent(ctx, "S2"); sent {
		t.Error("IsSent(S2) = true")
	}
	if v, _ := s.Setting(ctx, "home"); v != "" {
		t.Errorf("unset setting = %q", v)
	}
	_ = s.SetSetting(ctx, "home", "claude")
	_ = s.SetSetting(ctx, "home", "codex")
	if v, _ := s.Setting(ctx, "home"); v != "codex" {
		t.Errorf("setting = %q", v)
	}
}

func TestReopenKeepsData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "w.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.SetSetting(ctx, "k", "v")
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.Setting(ctx, "k"); v != "v" {
		t.Errorf("after reopen = %q", v)
	}
	if _, err := Open(ctx, "relative.db"); err == nil {
		t.Error("relative path gave no error")
	}
	if millis(time.Time{}) != 0 || !fromMillis(0).IsZero() {
		t.Error("zero time conversion is wrong")
	}
}
