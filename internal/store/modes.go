package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// rows returns every row of query, scanned.
func rows[T any](ctx context.Context, db *sql.DB, query string, scan func(scanner) (T, error)) ([]T, error) {
	found, err := db.QueryContext(ctx, query)
	return collect(found, err, func(r *sql.Rows) (T, error) { return scan(r) })
}

// row returns the row that query finds for arg, and whether there is one.
func row[T any](ctx context.Context, db *sql.DB, query, arg string, scan func(scanner) (T, error)) (T, bool, error) {
	v, err := scan(db.QueryRowContext(ctx, query, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

const chatModeQuery = "SELECT chat, mode, length, lookback FROM chat_modes"

// ChatModes returns every stored chat mode, "*" included.
func (s *Store) ChatModes(ctx context.Context) ([]model.ChatMode, error) {
	return rows(ctx, s.db, chatModeQuery+" ORDER BY chat", scanChatMode)
}

// ChatMode returns the stored mode of chat, and whether there is one.
func (s *Store) ChatMode(ctx context.Context, chat string) (model.ChatMode, bool, error) {
	return row(ctx, s.db, chatModeQuery+" WHERE chat = ?", chat, scanChatMode)
}

func scanChatMode(row scanner) (model.ChatMode, error) {
	var m model.ChatMode
	var mode string
	var length, lookback int64
	err := row.Scan(&m.Chat, &mode, &length, &lookback)
	m.Mode, m.Length, m.Lookback = model.Mode(mode), time.Duration(length)*time.Millisecond,
		time.Duration(lookback)*time.Millisecond
	return m, err
}

// SetChatMode creates or replaces the mode of m.Chat.
func (s *Store) SetChatMode(ctx context.Context, m model.ChatMode) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO chat_modes (chat, mode, length, lookback) VALUES (?, ?, ?, ?)
		ON CONFLICT (chat) DO UPDATE SET mode = excluded.mode, length = excluded.length,
		lookback = excluded.lookback`,
		m.Chat, string(m.Mode), m.Length.Milliseconds(), m.Lookback.Milliseconds())
	return err
}

// DeleteChatMode removes the mode of chat, so the mode of "*" applies to it again.
func (s *Store) DeleteChatMode(ctx context.Context, chat string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM chat_modes WHERE chat = ?", chat)
	return err
}

const windowQuery = "SELECT chat, session, opened, from_time, until_time FROM windows"

// Windows returns every open window.
func (s *Store) Windows(ctx context.Context) ([]model.Window, error) {
	return rows(ctx, s.db, windowQuery+" ORDER BY until_time", scanWindow)
}

// Window returns the open window of chat, and whether there is one.
func (s *Store) Window(ctx context.Context, chat string) (model.Window, bool, error) {
	return row(ctx, s.db, windowQuery+" WHERE chat = ?", chat, scanWindow)
}

func scanWindow(row scanner) (model.Window, error) {
	var w model.Window
	var opened, from, until int64
	err := row.Scan(&w.Chat, &w.Session, &opened, &from, &until)
	w.Opened, w.From, w.Until = fromMillis(opened), fromMillis(from), fromMillis(until)
	return w, err
}

// SetWindow opens or replaces the window of w.Chat.
func (s *Store) SetWindow(ctx context.Context, w model.Window) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO windows (chat, session, opened, from_time, until_time)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT (chat) DO UPDATE SET session = excluded.session,
		opened = excluded.opened, from_time = excluded.from_time, until_time = excluded.until_time`,
		w.Chat, w.Session, millis(w.Opened), millis(w.From), millis(w.Until))
	return err
}

// DeleteWindow closes the window of chat and reports whether one was open.
func (s *Store) DeleteWindow(ctx context.Context, chat string) (bool, error) {
	return s.exec(ctx, "DELETE FROM windows WHERE chat = ?", chat)
}

// ChatAgent returns the session name of chat's own agent, or "" when it has none yet.
func (s *Store) ChatAgent(ctx context.Context, chat string) (string, error) {
	return s.lookup(ctx, "SELECT session FROM chat_agents WHERE chat = ?", chat)
}

// AgentChat returns the chat whose own agent is session, or "" when session is no chat agent.
func (s *Store) AgentChat(ctx context.Context, session string) (string, error) {
	return s.lookup(ctx, "SELECT chat FROM chat_agents WHERE session = ?", session)
}

func (s *Store) lookup(ctx context.Context, query, arg string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx, query, arg).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// SetChatAgent records session as chat's own agent.
func (s *Store) SetChatAgent(ctx context.Context, chat, session string) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO chat_agents (chat, session) VALUES (?, ?)", chat, session)
	return err
}
