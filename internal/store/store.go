// Package store keeps the WhatsAgent database: chats, messages, the event log, routes, send
// rules, sessions with their cursors, sent message IDs and settings.
//
// Messages live in one of two databases. The file holds the messages of kept chats; the recent
// buffer, an in-memory database with the same tables, holds the messages of every other chat for
// a limited time. Each chat's messages sit in exactly one of them, and the events of both share
// one sequence. See the message storage specification in docs.
package store

import (
	"cmp"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/sqlitedb"
)

// migrations are applied in order; PRAGMA user_version counts the applied ones.
//
//go:embed schema.sql
var schemaV1 string

//go:embed schema_v2.sql
var schemaV2 string

//go:embed schema_v3.sql
var schemaV3 string

var migrations = []string{schemaV1, schemaV2, schemaV3}

// Store is the WhatsAgent database.
type Store struct {
	// db is the file: kept chats' messages and everything else that WhatsAgent keeps.
	db *sql.DB
	// mem is the recent buffer: the messages of chats that are not kept.
	mem *sql.DB

	// mu orders the changes of messages and events, so that a chat's messages move between the
	// databases as a whole and sequence numbers grow. Readers of messages hold it shared.
	mu sync.RWMutex
	// seq is the newest event's sequence number in either database.
	seq int64
}

// Open opens or creates the database at path, applies missing migrations, and opens an empty
// recent buffer.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.open(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) open(ctx context.Context) error {
	if err := migrate(ctx, s.db); err != nil {
		return err
	}
	mem, err := sqlitedb.OpenMemory()
	if err != nil {
		return err
	}
	s.mem = mem
	if err := migrate(ctx, mem); err != nil {
		return err
	}
	// The buffer starts empty, so its events from before are gone, and the sequence continues
	// above every number that a session's cursor may have reached.
	return s.db.QueryRowContext(ctx, `SELECT max(
		(SELECT coalesce(max(seq), 0) FROM events), (SELECT coalesce(max(cursor), 0) FROM sessions))`).Scan(&s.seq)
}

// Close closes both databases.
func (s *Store) Close() error {
	var errs []error
	for _, db := range []*sql.DB{s.mem, s.db} {
		if db != nil {
			errs = append(errs, db.Close())
		}
	}
	return errors.Join(errs...)
}

// holder returns the database that holds chat's messages. The caller holds s.mu.
func (s *Store) holder(ctx context.Context, chat string) (*sql.DB, error) {
	kept, err := s.Kept(ctx, chat)
	if err != nil || !kept {
		return s.mem, err
	}
	return s.db, nil
}

// Kept reports whether chat is kept, so its messages go to the file.
func (s *Store) Kept(ctx context.Context, chat string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM kept WHERE chat = ?", chat).Scan(&n)
	return n > 0, err
}

// KeptChats returns the kept chats.
func (s *Store) KeptChats(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT chat FROM kept ORDER BY chat")
	return collect(rows, err, func(rows *sql.Rows) (string, error) {
		var chat string
		return chat, rows.Scan(&chat)
	})
}

// Keep makes chat kept and moves its messages, events and sent IDs from the recent buffer to the
// file. It returns how many messages moved, and false when chat was kept already.
func (s *Store) Keep(ctx context.Context, chat string) (int, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kept, err := s.Kept(ctx, chat); err != nil || kept {
		return 0, false, err
	}
	n, err := s.moveIn(ctx, chat)
	return n, err == nil, err
}

// moveIn marks chat kept and moves its rows from the recent buffer into the file. The caller
// holds s.mu.
func (s *Store) moveIn(ctx context.Context, chat string) (int, error) {
	rows, err := s.mem.QueryContext(ctx, "SELECT "+messageColumns+" FROM messages m WHERE m.chat = ?", chat)
	msgs, err := collect(rows, err, scanMessageRow)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	if err := s.copyChat(ctx, tx, chat, msgs); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	_, err = deleteChat(ctx, s.mem, chat)
	return len(msgs), err
}

// copyChat writes chat's kept mark, its messages, and its events and sent IDs from the recent
// buffer into tx on the file.
func (s *Store) copyChat(ctx context.Context, tx *sql.Tx, chat string, msgs []model.Message) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO kept (chat) VALUES (?)", chat); err != nil {
		return err
	}
	for _, m := range msgs {
		if _, err := insertMessage(ctx, tx, m); err != nil {
			return err
		}
	}
	if err := copyRows(ctx, s.mem, tx, chat, "events", "seq, chat, message_id, kind"); err != nil {
		return err
	}
	return copyRows(ctx, s.mem, tx, chat, "sent", "id, chat, time")
}

// copyRows copies the rows of chat in a table of the recent buffer into the file. table and
// columns are constants of this package.
func copyRows(ctx context.Context, from *sql.DB, to *sql.Tx, chat, table, columns string) error {
	n := len(strings.Split(columns, ","))
	rows, err := from.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s WHERE chat = ?", columns, table), chat)
	batch, err := collect(rows, err, func(rows *sql.Rows) ([]any, error) {
		values, pointers := make([]any, n), make([]any, n)
		for i := range values {
			pointers[i] = &values[i]
		}
		return values, rows.Scan(pointers...)
	})
	if err != nil {
		return err
	}
	insert := fmt.Sprintf("INSERT OR IGNORE INTO %s (%s) VALUES (%s)", table, columns,
		strings.TrimSuffix(strings.Repeat("?, ", n), ", "))
	for _, values := range batch {
		if _, err := to.ExecContext(ctx, insert, values...); err != nil {
			return err
		}
	}
	return nil
}

// deleteChat deletes chat's messages, events and sent IDs from db and returns how many messages
// it deleted.
func deleteChat(ctx context.Context, db *sql.DB, chat string) (int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	res, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE chat = ?", chat)
	if err != nil {
		return 0, err
	}
	for _, table := range []string{"events", "sent"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE chat = ?", chat); err != nil {
			return 0, err
		}
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// Unkeep stops keeping chat and deletes its messages, events and sent IDs from the file. It
// returns how many messages it deleted, and false when chat was not kept.
func (s *Store) Unkeep(ctx context.Context, chat string) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	removed, err := s.exec(ctx, "DELETE FROM kept WHERE chat = ?", chat)
	if err != nil || !removed {
		return 0, false, err
	}
	n, err := deleteChat(ctx, s.db, chat)
	if err != nil {
		return n, true, err
	}
	return n, true, s.flush(ctx)
}

// Purge deletes the messages, events and sent IDs of every chat that is not kept from the file,
// so the file holds kept chats only even after a crash or a change made by hand. It returns how
// many messages it deleted.
func (s *Store) Purge(ctx context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	res, err := tx.ExecContext(ctx, "DELETE FROM messages WHERE chat NOT IN (SELECT chat FROM kept)")
	if err != nil {
		return 0, err
	}
	for _, table := range []string{"events", "sent"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE chat NOT IN (SELECT chat FROM kept)"); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, s.flush(ctx)
}

// flush rebuilds the database file from its live rows and empties the write-ahead log, so that no
// deleted text stays behind. Secure delete zeroes the rows that a deletion removes, but a database
// that ran without it, or rows that SQLite moved before, can leave old copies in free space and free
// pages; VACUUM writes a new file without them, and the checkpoint moves it out of the log.
func (s *Store) flush(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
		return err
	}
	var busy, log, done int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &log, &done); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("the database was busy, so deleted text may stay in the write-ahead log until the next checkpoint")
	}
	return nil
}

// Prune deletes the messages in the recent buffer that were sent before cutoff, with their events
// and the sent IDs from before cutoff. It returns how many messages it deleted.
func (s *Store) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.mem.ExecContext(ctx, "DELETE FROM messages WHERE time < ?", millis(cutoff))
	if err != nil {
		return 0, err
	}
	if _, err := s.mem.ExecContext(ctx, `DELETE FROM events WHERE NOT EXISTS
		(SELECT 1 FROM messages m WHERE m.chat = events.chat AND m.id = events.message_id)`); err != nil {
		return 0, err
	}
	for _, table := range []string{"sent WHERE time", "chats WHERE last_time"} {
		if _, err := s.mem.ExecContext(ctx, "DELETE FROM "+table+" < ?", millis(cutoff)); err != nil {
			return 0, err
		}
	}
	return res.RowsAffected()
}

func migrate(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for i := version; i < len(migrations); i++ {
		if err := apply(ctx, db, i+1, migrations[i]); err != nil {
			return fmt.Errorf("migrate to version %d: %w", i+1, err)
		}
	}
	return nil
}

func apply(ctx context.Context, db *sql.DB, version int, script string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	if _, err := tx.ExecContext(ctx, script); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// Apply stores one update, in the file for a kept chat and in the recent buffer otherwise, and
// appends it to the event log. It returns the event's sequence number, or 0 when the update
// changed nothing, such as a message that is already stored.
func (s *Store) Apply(ctx context.Context, u model.Update) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.holder(ctx, u.Message.Chat)
	if err != nil {
		return 0, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	changed, err := change(ctx, tx, u)
	if err != nil || !changed {
		return 0, err
	}
	seq := s.seq + 1
	if _, err := tx.ExecContext(ctx, "INSERT INTO events (seq, chat, message_id, kind) VALUES (?, ?, ?, ?)",
		seq, u.Message.Chat, u.Message.ID, string(u.Kind)); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	s.seq = seq
	return seq, nil
}

func change(ctx context.Context, tx *sql.Tx, u model.Update) (bool, error) {
	m := u.Message
	var res sql.Result
	var err error
	switch u.Kind {
	case model.New:
		res, err = insertMessage(ctx, tx, m)
	case model.Edit:
		res, err = tx.ExecContext(ctx,
			"UPDATE messages SET text = ?, edited = 1 WHERE chat = ? AND id = ?", m.Text, m.Chat, m.ID)
	case model.Delete:
		res, err = tx.ExecContext(ctx,
			"UPDATE messages SET text = '', deleted = 1 WHERE chat = ? AND id = ? AND deleted = 0", m.Chat, m.ID)
	default:
		return false, fmt.Errorf("unknown update kind %q", u.Kind)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func insertMessage(ctx context.Context, tx *sql.Tx, m model.Message) (sql.Result, error) {
	mentions := ""
	if len(m.Mentions) > 0 {
		data, err := json.Marshal(m.Mentions)
		if err != nil {
			return nil, err
		}
		mentions = string(data)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chats (jid, last_time) VALUES (?, ?)
		ON CONFLICT (jid) DO UPDATE SET last_time = max(last_time, excluded.last_time)`,
		m.Chat, millis(m.Time)); err != nil {
		return nil, err
	}
	return tx.ExecContext(ctx, `INSERT INTO messages (chat, id, sender, raw_sender, sender_name, time,
		from_me, role, text, reply_to, quote, mentions, media_type, mime_type, filename, reaction_to,
		reaction, forwarded, edited, deleted, raw)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (chat, id) DO NOTHING`,
		m.Chat, m.ID, m.Sender, m.RawSender, m.SenderName, millis(m.Time), m.FromMe, string(m.Role),
		m.Text, m.ReplyTo, m.Quote, mentions, m.MediaType, m.MimeType, m.Filename, m.ReactionTo,
		m.Reaction, m.Forwarded, m.Edited, m.Deleted, m.Raw)
}

const messageColumns = `m.chat, m.id, m.sender, m.raw_sender, m.sender_name, m.time, m.from_me,
	m.role, m.text, m.reply_to, m.quote, m.mentions, m.media_type, m.mime_type, m.filename,
	m.reaction_to, m.reaction, m.forwarded, m.edited, m.deleted, m.raw`

type scanner interface {
	Scan(dest ...any) error
}

func scanMessage(row scanner, extra ...any) (model.Message, error) {
	var m model.Message
	var ms int64
	var role, mentions string
	dest := []any{&m.Chat, &m.ID, &m.Sender, &m.RawSender, &m.SenderName, &ms, &m.FromMe, &role,
		&m.Text, &m.ReplyTo, &m.Quote, &mentions, &m.MediaType, &m.MimeType, &m.Filename,
		&m.ReactionTo, &m.Reaction, &m.Forwarded, &m.Edited, &m.Deleted, &m.Raw}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return m, err
	}
	m.Time = fromMillis(ms)
	m.Role = model.Role(role)
	if mentions != "" {
		if err := json.Unmarshal([]byte(mentions), &m.Mentions); err != nil {
			return m, fmt.Errorf("message %s mentions: %w", m.ID, err)
		}
	}
	return m, nil
}

func collect[T any](rows *sql.Rows, err error, scan func(*sql.Rows) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		item, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanMessageRow(rows *sql.Rows) (model.Message, error) {
	return scanMessage(rows)
}

// Message returns one message of chat from the database that holds the chat.
func (s *Store) Message(ctx context.Context, chat, id string) (model.Message, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	db, err := s.holder(ctx, chat)
	if err != nil {
		return model.Message{}, false, err
	}
	row := db.QueryRowContext(ctx,
		"SELECT "+messageColumns+" FROM messages m WHERE m.chat = ? AND m.id = ?", chat, id)
	m, err := scanMessage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

// Messages returns the latest limit messages of chat, oldest first. limit 0 returns all of them.
func (s *Store) Messages(ctx context.Context, chat string, limit int) ([]model.Message, error) {
	return s.MessagesBetween(ctx, chat, time.UnixMilli(0), time.UnixMilli(1<<62), limit)
}

// MessagesBetween returns the latest limit messages of chat sent from start to end, oldest
// first, from the database that holds the chat. limit 0 returns all of them.
func (s *Store) MessagesBetween(ctx context.Context, chat string, start, end time.Time, limit int) ([]model.Message, error) {
	if limit <= 0 {
		limit = -1
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	db, err := s.holder(ctx, chat)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT * FROM (SELECT `+messageColumns+`
		FROM messages m WHERE m.chat = ? AND m.time BETWEEN ? AND ? ORDER BY m.time DESC, m.id DESC LIMIT ?)
		ORDER BY time, id`, chat, start.UnixMilli(), end.UnixMilli(), limit)
	return collect(rows, err, scanMessageRow)
}

// Search returns up to limit messages whose text contains query, newest first, in chat or in
// every chat when chat is "". It searches the kept chats, and the recent buffer too when recent
// is true.
func (s *Store) Search(ctx context.Context, query, chat string, limit int, recent bool) ([]model.Message, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var found []model.Message
	for _, db := range s.databases(recent) {
		rows, err := db.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages m
			WHERE m.text LIKE '%' || ? || '%' ESCAPE '\' AND (? = '' OR m.chat = ?)
			ORDER BY m.time DESC LIMIT ?`, escapeLike(query), chat, chat, limit)
		part, err := collect(rows, err, scanMessageRow)
		if err != nil {
			return nil, err
		}
		found = append(found, part...)
	}
	slices.SortStableFunc(found, func(a, b model.Message) int { return b.Time.Compare(a.Time) })
	return found[:min(limit, len(found))], nil
}

// databases returns the file, and the recent buffer after it when recent is true.
func (s *Store) databases(recent bool) []*sql.DB {
	if recent {
		return []*sql.DB{s.db, s.mem}
	}
	return []*sql.DB{s.db}
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Events returns up to limit events after seq, in the given chats, with their messages, from the
// file and the recent buffer in sequence order. The chat "*" stands for every chat.
func (s *Store) Events(ctx context.Context, after int64, chats []string, limit int) ([]model.Event, error) {
	if len(chats) == 0 {
		return nil, nil
	}
	list, err := json.Marshal(chats)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var all []model.Event
	for _, db := range s.databases(true) {
		rows, err := db.QueryContext(ctx, `SELECT `+messageColumns+`, e.seq, e.kind,
			coalesce(c.name, '') FROM events e
			JOIN messages m ON m.chat = e.chat AND m.id = e.message_id
			LEFT JOIN chats c ON c.jid = e.chat
			WHERE e.seq > ?
			AND (e.chat IN (SELECT value FROM json_each(?2)) OR '*' IN (SELECT value FROM json_each(?2)))
			ORDER BY e.seq LIMIT ?3`, after, string(list), limit)
		part, err := collect(rows, err, scanEvent)
		if err != nil {
			return nil, err
		}
		all = append(all, part...)
	}
	slices.SortFunc(all, func(a, b model.Event) int { return cmp.Compare(a.Seq, b.Seq) })
	all = all[:min(limit, len(all))]
	return all, s.nameChats(ctx, all)
}

func scanEvent(rows *sql.Rows) (model.Event, error) {
	var e model.Event
	var kind string
	m, err := scanMessage(rows, &e.Seq, &kind, &e.ChatName)
	e.Message, e.Kind = m, model.UpdateKind(kind)
	return e, err
}

// nameChats fills in the chat names of events from the recent buffer, which keeps no names.
func (s *Store) nameChats(ctx context.Context, events []model.Event) error {
	names := map[string]string{}
	for i, e := range events {
		if e.ChatName != "" {
			continue
		}
		name, ok := names[e.Message.Chat]
		if !ok {
			chat, err := s.Chat(ctx, e.Message.Chat)
			if err != nil {
				return err
			}
			name = chat.Name
			names[e.Message.Chat] = name
		}
		events[i].ChatName = name
	}
	return nil
}

// LastSeq returns the sequence number of the newest event in the file, or 0.
func (s *Store) LastSeq(ctx context.Context) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, "SELECT coalesce(max(seq), 0) FROM events").Scan(&seq)
	return seq, err
}

// ChangedChats returns the kept chats with any event after seq, and the newest sequence number in
// the file.
func (s *Store) ChangedChats(ctx context.Context, after int64) ([]string, int64, error) {
	last, err := s.LastSeq(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT DISTINCT chat FROM events WHERE seq > ? AND seq <= ? ORDER BY chat", after, last)
	chats, err := collect(rows, err, func(rows *sql.Rows) (string, error) {
		var chat string
		return chat, rows.Scan(&chat)
	})
	return chats, last, err
}

// write runs one statement that changes the database.
func (s *Store) write(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// upsert sets one column of a row that a key column names, creating the row when it is missing.
// The table and column names are constants of this package.
func (s *Store) upsert(ctx context.Context, table, keyColumn, column, key, value string) error {
	return s.write(ctx, fmt.Sprintf(`INSERT INTO %[1]s (%[2]s, %[3]s) VALUES (?, ?)
		ON CONFLICT (%[2]s) DO UPDATE SET %[3]s = excluded.%[3]s`, table, keyColumn, column), key, value)
}

// SetChatName records the display name of a chat.
func (s *Store) SetChatName(ctx context.Context, jid, name string) error {
	return s.upsert(ctx, "chats", "jid", "name", jid, name)
}

// Chats returns up to limit chats whose name or JID contains query, most recently active first.
func (s *Store) Chats(ctx context.Context, query string, limit int) ([]model.Chat, error) {
	q := escapeLike(query)
	rows, err := s.db.QueryContext(ctx, `SELECT jid, name, last_time FROM chats
		WHERE name LIKE '%' || ? || '%' ESCAPE '\' OR jid LIKE '%' || ? || '%' ESCAPE '\'
		ORDER BY last_time DESC, name LIMIT ?`, q, q, limit)
	return collect(rows, err, scanChat)
}

func scanChat(rows *sql.Rows) (model.Chat, error) {
	var c model.Chat
	var ms int64
	err := rows.Scan(&c.JID, &c.Name, &ms)
	c.LastTime = fromMillis(ms)
	return c, err
}

// Chat returns one chat, with an empty name when the chat is unknown.
func (s *Store) Chat(ctx context.Context, jid string) (model.Chat, error) {
	c := model.Chat{JID: jid}
	var ms int64
	err := s.db.QueryRowContext(ctx, "SELECT name, last_time FROM chats WHERE jid = ?", jid).
		Scan(&c.Name, &ms)
	if errors.Is(err, sql.ErrNoRows) {
		return c, nil
	}
	c.LastTime = fromMillis(ms)
	return c, err
}

// Routes returns every route.
func (s *Store) Routes(ctx context.Context) ([]model.Route, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT chat, session, prefix FROM routes ORDER BY session, chat")
	return collect(rows, err, func(rows *sql.Rows) (model.Route, error) {
		var r model.Route
		return r, rows.Scan(&r.Chat, &r.Session, &r.Prefix)
	})
}

// SetRoute creates or replaces the route of r.Chat.
func (s *Store) SetRoute(ctx context.Context, r model.Route) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO routes (chat, session, prefix) VALUES (?, ?, ?)
		ON CONFLICT (chat) DO UPDATE SET session = excluded.session, prefix = excluded.prefix`,
		r.Chat, r.Session, r.Prefix)
	return err
}

// DeleteRoute removes the route of chat and reports whether there was one.
func (s *Store) DeleteRoute(ctx context.Context, chat string) (bool, error) {
	return s.exec(ctx, "DELETE FROM routes WHERE chat = ?", chat)
}

func (s *Store) exec(ctx context.Context, query string, args ...any) (bool, error) {
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RenameSession moves the routes, send rules, cursor and record of session old to name.
func (s *Store) RenameSession(ctx context.Context, old, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	for _, query := range []string{
		"UPDATE routes SET session = ? WHERE session = ?",
		"UPDATE windows SET session = ? WHERE session = ?",
		"UPDATE OR REPLACE chat_agents SET session = ? WHERE session = ?",
		"UPDATE OR REPLACE send_rules SET session = ? WHERE session = ?",
		"UPDATE OR REPLACE sessions SET name = ? WHERE name = ?",
	} {
		if _, err := tx.ExecContext(ctx, query, name, old); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Rules returns every send rule.
func (s *Store) Rules(ctx context.Context) ([]model.Rule, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT session, chat, permission FROM send_rules ORDER BY session, chat")
	return collect(rows, err, func(rows *sql.Rows) (model.Rule, error) {
		var r model.Rule
		var permission string
		err := rows.Scan(&r.Session, &r.Chat, &permission)
		r.Permission = model.Permission(permission)
		return r, err
	})
}

// SetRule creates or replaces a send rule.
func (s *Store) SetRule(ctx context.Context, r model.Rule) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO send_rules (session, chat, permission) VALUES (?, ?, ?)
		ON CONFLICT (session, chat) DO UPDATE SET permission = excluded.permission`,
		r.Session, r.Chat, string(r.Permission))
	return err
}

// TouchSession records that a session checked in. A new session's cursor starts at the newest
// event, so it receives only messages that arrive from now on.
func (s *Store) TouchSession(ctx context.Context, info model.Session) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (name, harness, folder, last_seen, cursor)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET harness = excluded.harness, folder = excluded.folder,
		last_seen = excluded.last_seen`,
		info.Name, info.Harness, info.Folder, millis(info.LastSeen), s.seq)
	return err
}

// Sessions returns every known session, without online state.
func (s *Store) Sessions(ctx context.Context) ([]model.Session, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT name, harness, folder, last_seen FROM sessions ORDER BY last_seen DESC")
	return collect(rows, err, func(rows *sql.Rows) (model.Session, error) {
		var info model.Session
		var ms int64
		err := rows.Scan(&info.Name, &info.Harness, &info.Folder, &ms)
		info.LastSeen = fromMillis(ms)
		return info, err
	})
}

// Cursor returns the sequence number of the last event delivered to a session.
func (s *Store) Cursor(ctx context.Context, name string) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, "SELECT cursor FROM sessions WHERE name = ?", name).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("unknown session %q", name)
	}
	return seq, err
}

// SetCursor records the last event delivered to a session.
func (s *Store) SetCursor(ctx context.Context, name string, seq int64) error {
	return s.write(ctx, "UPDATE sessions SET cursor = ? WHERE name = ?", seq, name)
}

// RecordSent remembers the ID of a message that WhatsAgent sends, next to the chat's messages.
func (s *Store) RecordSent(ctx context.Context, id, chat string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	db, err := s.holder(ctx, chat)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx,
		"INSERT OR IGNORE INTO sent (id, chat, time) VALUES (?, ?, ?)", id, chat, millis(at))
	return err
}

// IsSent reports whether WhatsAgent sent the message with this ID.
func (s *Store) IsSent(ctx context.Context, id string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, db := range s.databases(true) {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sent WHERE id = ?", id).Scan(&n); err != nil || n > 0 {
			return n > 0, err
		}
	}
	return false, nil
}

// Setting returns a stored setting, or "" when it is not set.
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	return s.lookup(ctx, "SELECT value FROM settings WHERE key = ?", key)
}

// SetSetting stores a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	return s.upsert(ctx, "settings", "key", "value", key, value)
}
