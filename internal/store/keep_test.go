package store

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/sqlitedb"
)

// keep makes chat kept and fails the test when it cannot.
func keep(t *testing.T, s *Store, chat string) {
	t.Helper()
	if _, _, err := s.Keep(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
}

// onDisk reports which of the markers appear in the bytes of the database file, its
// write-ahead log or its shared-memory file.
func onDisk(t *testing.T, path string, markers ...string) []string {
	t.Helper()
	var found []string
	for _, marker := range markers {
		for _, file := range []string{path, path + "-wal", path + "-shm"} {
			data, err := os.ReadFile(file)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(marker)) {
				found = append(found, marker+" in "+filepath.Base(file))
			}
		}
	}
	return found
}

// TestDiskHoldsOnlyKeptChats is the disk boundary: nothing of a message in a chat that is not
// kept reaches the files, not its text, sender, ID, edit, deletion or sent ID.
func TestDiskHoldsOnlyKeptChats(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "whatsagent.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	keep(t, s, "kept@g.us")
	secret := model.Message{Chat: "other@g.us", ID: "ID-SECRET-1", Sender: "SENDER-SECRET@s.whatsapp.net",
		SenderName: "NAME-SECRET", Time: time.Now(), Role: model.RoleOther, Text: "TEXT-SECRET",
		Raw: []byte("RAW-SECRET")}
	updates := []model.Update{
		{Kind: model.New, Message: secret},
		{Kind: model.Edit, Message: model.Message{Chat: "other@g.us", ID: "ID-SECRET-1", Text: "EDIT-SECRET"}},
		{Kind: model.New, Message: model.Message{Chat: "other@g.us", ID: "ID-SECRET-2", Time: time.Now(), Text: "GONE-SECRET"}},
		{Kind: model.Delete, Message: model.Message{Chat: "other@g.us", ID: "ID-SECRET-2"}},
		{Kind: model.New, Message: msg("kept@g.us", "k1", "kept text", 100)},
	}
	for _, u := range updates {
		if _, err := s.Apply(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordSent(ctx, "SENT-SECRET", "other@g.us", time.Now()); err != nil {
		t.Fatal(err)
	}
	markers := []string{"ID-SECRET", "SENDER-SECRET", "NAME-SECRET", "TEXT-SECRET", "RAW-SECRET",
		"EDIT-SECRET", "GONE-SECRET", "SENT-SECRET"}
	if found := onDisk(t, path, markers...); len(found) > 0 {
		t.Errorf("on disk while open: %v", found)
	}
	// The daemon still has the messages in memory.
	if got, ok, _ := s.Message(ctx, "other@g.us", "ID-SECRET-1"); !ok || got.Text != "EDIT-SECRET" {
		t.Errorf("recent message = %+v, %v", got, ok)
	}
	if sent, _ := s.IsSent(ctx, "SENT-SECRET"); !sent {
		t.Error("the recent sent ID is lost")
	}
	if found := onDisk(t, path, "kept text"); len(found) == 0 {
		t.Error("the kept chat's message is not on disk; the check finds nothing")
	}
	s.Close()
	if found := onDisk(t, path, markers...); len(found) > 0 {
		t.Errorf("on disk after close: %v", found)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok, _ := s.Message(ctx, "other@g.us", "ID-SECRET-1"); ok {
		t.Error("a recent message survived a restart")
	}
}

// TestUnkeepAndPurgeErase checks that the text of a chat that stops being kept is gone from the
// files, not only from the tables.
func TestUnkeepAndPurgeErase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "whatsagent.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, chat := range []string{"a@g.us", "b@g.us"} {
		keep(t, s, chat)
	}
	_, _ = s.Apply(ctx, model.Update{Kind: model.New, Message: msg("a@g.us", "a1", "UNKEPT-MARKER", 100)})
	_, _ = s.Apply(ctx, model.Update{Kind: model.New, Message: msg("b@g.us", "b1", "PURGED-MARKER", 100)})
	_ = s.RecordSent(ctx, "a-sent", "a@g.us", time.Now())
	if found := onDisk(t, path, "UNKEPT-MARKER", "PURGED-MARKER"); len(found) != 2 {
		t.Fatalf("before = %v", found)
	}

	n, was, err := s.Unkeep(ctx, "a@g.us")
	if err != nil || !was || n != 1 {
		t.Fatalf("Unkeep = %d, %v, %v", n, was, err)
	}
	if _, was, _ := s.Unkeep(ctx, "a@g.us"); was {
		t.Error("a second Unkeep found the chat kept")
	}
	if sent, _ := s.IsSent(ctx, "a-sent"); sent {
		t.Error("Unkeep left the chat's sent ID")
	}
	// A change made by hand, or a crash, leaves rows of a chat that is not kept: Purge removes them.
	if _, err := s.db.ExecContext(ctx, "DELETE FROM kept WHERE chat = 'b@g.us'"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Purge(ctx); err != nil || n != 1 {
		t.Fatalf("Purge = %d, %v", n, err)
	}
	if found := onDisk(t, path, "UNKEPT-MARKER", "PURGED-MARKER"); len(found) > 0 {
		t.Errorf("after Unkeep and Purge: %v", found)
	}
	if msgs, _ := s.Messages(ctx, "b@g.us", 0); len(msgs) != 0 {
		t.Errorf("Purge left %+v", msgs)
	}
}

// TestRecentBuffer checks how the buffer and the file work together: one sequence, events in
// order across both, search of the buffer only on request, and the changes log of kept chats.
func TestRecentBuffer(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	keep(t, s, "kept@g.us")
	_ = s.SetChatName(ctx, "recent@g.us", "Recent chat")
	for i, m := range []model.Message{msg("kept@g.us", "k1", "one", 100), msg("recent@g.us", "r1", "two", 200),
		msg("kept@g.us", "k2", "three", 300)} {
		if seq, err := s.Apply(ctx, model.Update{Kind: model.New, Message: m}); err != nil || seq != int64(i+1) {
			t.Fatalf("Apply %d = %d, %v", i, seq, err)
		}
	}
	events, err := s.Events(ctx, 0, []string{"*"}, 10)
	if err != nil || len(events) != 3 || events[1].Message.ID != "r1" || events[1].ChatName != "Recent chat" {
		t.Fatalf("Events = %+v, %v", events, err)
	}
	if two, _ := s.Events(ctx, 0, []string{"*"}, 2); len(two) != 2 || two[1].Seq != 2 {
		t.Errorf("Events with a limit = %+v", two)
	}
	if found, _ := s.Search(ctx, "two", "", 10, false); len(found) != 0 {
		t.Errorf("Search of kept chats found %+v", found)
	}
	if found, _ := s.Search(ctx, "t", "", 10, true); len(found) != 2 || found[0].ID != "k2" || found[1].ID != "r1" {
		t.Errorf("Search of both = %+v", found)
	}
	if chats, last, _ := s.ChangedChats(ctx, 0); len(chats) != 1 || chats[0] != "kept@g.us" || last != 3 {
		t.Errorf("ChangedChats = %v, %d", chats, last)
	}
	if kept, _ := s.KeptChats(ctx); len(kept) != 1 || kept[0] != "kept@g.us" {
		t.Errorf("KeptChats = %v", kept)
	}
	// A new session starts after the newest event in either database.
	_ = s.TouchSession(ctx, model.Session{Name: "n"})
	if cursor, _ := s.Cursor(ctx, "n"); cursor != 3 {
		t.Errorf("new session cursor = %d", cursor)
	}
}

// TestKeepMovesTheBuffer checks that keeping a chat moves its messages, events and sent IDs from
// the buffer into the file, under their own sequence numbers.
func TestKeepMovesTheBuffer(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	_, _ = s.Apply(ctx, model.Update{Kind: model.New, Message: msg("g@g.us", "m1", "hello", 100)})
	_ = s.RecordSent(ctx, "S1", "g@g.us", time.Now())
	n, moved, err := s.Keep(ctx, "g@g.us")
	if err != nil || !moved || n != 1 {
		t.Fatalf("Keep = %d, %v, %v", n, moved, err)
	}
	if _, again, _ := s.Keep(ctx, "g@g.us"); again {
		t.Error("a second Keep moved the chat again")
	}
	if msgs, _ := s.Messages(ctx, "g@g.us", 0); len(msgs) != 1 || msgs[0].Text != "hello" {
		t.Errorf("Messages after Keep = %+v", msgs)
	}
	if chats, last, _ := s.ChangedChats(ctx, 0); len(chats) != 1 || last != 1 {
		t.Errorf("ChangedChats after Keep = %v, %d", chats, last)
	}
	if sent, _ := s.IsSent(ctx, "S1"); !sent {
		t.Error("Keep lost the sent ID")
	}
	var left int
	_ = s.mem.QueryRowContext(ctx, "SELECT (SELECT count(*) FROM messages) + (SELECT count(*) FROM events) + (SELECT count(*) FROM sent)").Scan(&left)
	if left != 0 {
		t.Errorf("Keep left %d rows in the buffer", left)
	}
}

// TestPrune checks that the buffer drops messages older than the cutoff with their events and
// sent IDs, and keeps newer ones.
func TestPrune(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	now := time.Now()
	old := model.Message{Chat: "g@g.us", ID: "old", Time: now.Add(-25 * time.Hour), Text: "old"}
	recent := model.Message{Chat: "g@g.us", ID: "new", Time: now, Text: "new"}
	for _, m := range []model.Message{old, recent} {
		_, _ = s.Apply(ctx, model.Update{Kind: model.New, Message: m})
	}
	_ = s.RecordSent(ctx, "old-sent", "g@g.us", now.Add(-25*time.Hour))
	n, err := s.Prune(ctx, now.Add(-24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d, %v", n, err)
	}
	if msgs, _ := s.Messages(ctx, "g@g.us", 0); len(msgs) != 1 || msgs[0].ID != "new" {
		t.Errorf("Messages after Prune = %+v", msgs)
	}
	if events, _ := s.Events(ctx, 0, []string{"*"}, 10); len(events) != 1 || events[0].Message.ID != "new" {
		t.Errorf("Events after Prune = %+v", events)
	}
	if sent, _ := s.IsSent(ctx, "old-sent"); sent {
		t.Error("Prune kept an old sent ID")
	}
}

// TestSequenceAfterRestart checks that after a restart the sequence continues above every
// session's cursor, which may point at an event of the lost buffer.
func TestSequenceAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "whatsagent.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		_, _ = s.Apply(ctx, model.Update{Kind: model.New, Message: msg("g@g.us", id, id, 100)})
	}
	_ = s.TouchSession(ctx, model.Session{Name: "agent"})
	s.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if seq, _ := s.Apply(ctx, model.Update{Kind: model.New, Message: msg("g@g.us", "d", "d", 100)}); seq != 4 {
		t.Errorf("first sequence after a restart = %d, want 4", seq)
	}
}

// TestMigrationKeepsRoutedChats upgrades a version 2 database: the routed chats become kept, and
// the history events go.
func TestMigrationKeepsRoutedChats(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "whatsagent.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	old, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"DROP TABLE kept",
		"ALTER TABLE events ADD COLUMN live INTEGER NOT NULL DEFAULT 1",
		"PRAGMA user_version = 2",
		"INSERT INTO routes (chat, session) VALUES ('g@g.us', 'a'), ('*', 'b')",
		"INSERT INTO events (seq, chat, message_id, kind, live) VALUES (1, 'g@g.us', 'h', 'new', 0), (2, 'g@g.us', 'l', 'new', 1)",
	} {
		if _, err := old.ExecContext(ctx, query); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	old.Close()
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if kept, _ := s.KeptChats(ctx); len(kept) != 1 || kept[0] != "g@g.us" {
		t.Errorf("kept after the upgrade = %v", kept)
	}
	var events int
	_ = s.db.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&events)
	if events != 1 {
		t.Errorf("events after the upgrade = %d, want only the live one", events)
	}
}
