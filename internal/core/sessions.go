package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// ErrUnknownToken is returned for a session token that WhatsAgent did not issue.
var ErrUnknownToken = errors.New("unknown session token: check in again")

// conn is one checked-in session process, such as one WhatsAgent MCP server.
type conn struct {
	token string
	info  model.Session
}

// subscriber is the one push target of a session name.
type subscriber struct {
	name   string
	push   string
	extra  []string
	pusher Pusher
	wake   chan struct{}
	cancel context.CancelFunc
}

// CheckIn registers a session process and returns its token and session name. The name
// defaults to the folder's base name.
func (c *Core) CheckIn(ctx context.Context, info model.Session) (string, string, error) {
	info.Name = sessionName(info)
	info.LastSeen = time.Now().UTC()
	if err := c.st.TouchSession(ctx, info); err != nil {
		return "", "", err
	}
	token := randomToken()
	c.mu.Lock()
	c.conns[token] = &conn{token: token, info: info}
	c.mu.Unlock()
	return token, info.Name, nil
}

func sessionName(info model.Session) string {
	name := strings.TrimSpace(info.Name)
	if name == "" && info.Folder != "" {
		name = filepath.Base(info.Folder)
	}
	if name == "" || name == "/" || name == "." {
		name = "session"
	}
	return strings.Join(strings.Fields(name), "-")
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CheckOut removes a session process when it ends.
func (c *Core) CheckOut(token string) {
	c.mu.Lock()
	cn := c.conns[token]
	delete(c.conns, token)
	c.mu.Unlock()
	if cn != nil {
		cn.info.LastSeen = time.Now().UTC()
		_ = c.st.TouchSession(c.ctx, cn.info)
	}
}

// Caller returns the session name for a token.
func (c *Core) Caller(token string) (Caller, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cn, ok := c.conns[token]
	if !ok {
		return Caller{}, ErrUnknownToken
	}
	return Caller{Session: cn.info.Name}, nil
}

// Subscribe makes the session of token a push session and delivers its routed events to pusher
// until ctx ends or a push fails. A newer push target for the same session replaces this one.
func (c *Core) Subscribe(ctx context.Context, token, push string, pusher Pusher) error {
	caller, err := c.Caller(token)
	if err != nil {
		return err
	}
	return c.serve(ctx, &subscriber{name: caller.Session, push: push, pusher: pusher})
}

// Watch delivers the events of the named session's routes, plus the extra chats, to pusher until
// ctx ends. It is the push path of `whatsagent watch`. A since of 0 or more moves the session's
// cursor there first, so the watch starts after that event.
func (c *Core) Watch(ctx context.Context, name string, extra []string, since int64, pusher Pusher) error {
	token, name, err := c.CheckIn(ctx, model.Session{Name: name, Harness: "watch"})
	if err != nil {
		return err
	}
	defer c.CheckOut(token)
	if since >= 0 {
		if err := c.st.SetCursor(ctx, name, since); err != nil {
			return err
		}
	}
	resolved := make([]string, 0, len(extra))
	for _, ref := range extra {
		jid, err := c.resolveChat(ctx, ref)
		if err != nil {
			return err
		}
		resolved = append(resolved, jid)
	}
	return c.serve(ctx, &subscriber{name: name, push: "watch", extra: resolved, pusher: pusher})
}

// Attach makes a session that an adapter found by itself, such as a Codex thread, a push
// session until ctx ends or a push fails.
func (c *Core) Attach(ctx context.Context, info model.Session, push string, pusher Pusher) error {
	info.Name = sessionName(info)
	info.LastSeen = time.Now().UTC()
	if err := c.st.TouchSession(ctx, info); err != nil {
		return err
	}
	return c.serve(ctx, &subscriber{name: info.Name, push: push, pusher: pusher})
}

func (c *Core) serve(ctx context.Context, sub *subscriber) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sub.wake, sub.cancel = make(chan struct{}, 1), cancel
	c.register(sub)
	defer c.unregister(sub)
	ticker := time.NewTicker(c.cfg.Heartbeat)
	defer ticker.Stop()
	for {
		if err := c.deliver(ctx, sub); err != nil {
			return ended(ctx, err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-sub.wake:
		case <-ticker.C:
			if err := sub.pusher.Push(ctx, nil); err != nil {
				return ended(ctx, err)
			}
		}
	}
}

// ended returns nil when a push session ended because its context ended, such as when a newer
// push target replaced it, and err otherwise.
func ended(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (c *Core) register(sub *subscriber) {
	c.mu.Lock()
	old := c.subs[sub.name]
	c.subs[sub.name] = sub
	c.mu.Unlock()
	if old != nil {
		old.cancel()
	}
	if c.announced(sub.name) {
		c.statusLine(fmt.Sprintf("%s is online (%s).", sub.name, sub.push))
	}
}

func (c *Core) unregister(sub *subscriber) {
	c.mu.Lock()
	current := c.subs[sub.name] == sub
	if current {
		delete(c.subs, sub.name)
	}
	c.mu.Unlock()
	if current && c.ctx.Err() == nil && c.announced(sub.name) {
		c.statusLine(fmt.Sprintf("%s is offline. Its messages wait until it is back.", sub.name))
	}
}

// announced reports whether a session's push state changes get a status line: only sessions
// with a route, so the many tools-only sessions do not fill the self-chat.
func (c *Core) announced(name string) bool {
	if name == homeName {
		return false
	}
	routes, err := c.st.Routes(c.ctx)
	if err != nil {
		return false
	}
	for _, r := range routes {
		if r.Session == name {
			return true
		}
	}
	return false
}

// wakeAll tells every push session to look for new events.
func (c *Core) wakeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, sub := range c.subs {
		wake(sub)
	}
}

// deliver pushes a batch of events after the session's cursor and moves the cursor past them.
// After a full batch it wakes the session again for the next one.
func (c *Core) deliver(ctx context.Context, sub *subscriber) error {
	events, err := c.waiting(ctx, sub.name, sub.extra, eventBatch)
	if err != nil || len(events.all) == 0 {
		return err
	}
	if len(events.keep) > 0 {
		if err := sub.pusher.Push(ctx, events.keep); err != nil {
			return err
		}
		c.mu.Lock()
		c.delivered[sub.name] = time.Now()
		c.mu.Unlock()
	}
	if len(events.all) == eventBatch {
		wake(sub)
	}
	return c.st.SetCursor(ctx, sub.name, events.all[len(events.all)-1].Seq)
}

// wake tells a push session to look for new events, unless it already has a wake-up waiting.
func wake(sub *subscriber) {
	select {
	case sub.wake <- struct{}{}:
	default:
	}
}

// batch is a page of events after a session's cursor: all of them, and the ones to push.
type batch struct {
	all  []model.Event
	keep []model.Event
}

func (c *Core) waiting(ctx context.Context, name string, extra []string, limit int) (batch, error) {
	cursor, err := c.st.Cursor(ctx, name)
	if err != nil {
		return batch{}, err
	}
	routes, err := c.st.Routes(ctx)
	if err != nil {
		return batch{}, err
	}
	all, err := c.st.Events(ctx, cursor, c.targets(ctx, name, extra, routes).query(), limit)
	if err != nil {
		return batch{}, err
	}
	// Read the routes again, so a route change that happened before an event was stored applies
	// to that event.
	if routes, err = c.st.Routes(ctx); err != nil {
		return batch{}, err
	}
	t := c.targets(ctx, name, extra, routes)
	b := batch{all: all}
	views := map[string]view{}
	for _, e := range all {
		if c.wanted(ctx, t, views, e) {
			b.keep = append(b.keep, e)
		}
	}
	return b, nil
}

// targets is the set of chats that a session handles. Chats in full come with every message:
// the extra chats of the owner's own watch, and home's self-chat. The chats' modes filter the
// rest.
type targets struct {
	chats     map[string]model.Route
	everyChat *model.Route
	elsewhere map[string]bool
	full      map[string]bool
}

func (c *Core) targets(ctx context.Context, name string, extra []string, routes []model.Route) targets {
	t := targets{chats: map[string]model.Route{}, elsewhere: map[string]bool{}, full: map[string]bool{}}
	t.addAgentChat(c.agentChat(ctx, name), name, routes)
	for _, r := range routes {
		switch {
		case r.Session != name:
			t.elsewhere[r.Chat] = true
		case r.Chat == "*":
			route := r
			t.everyChat = &route
		default:
			t.chats[r.Chat] = r
		}
	}
	for _, chat := range extra {
		t.chats[chat] = model.Route{Chat: chat, Session: name}
		t.full[chat] = true
	}
	// Only home gets the self-chat, also through a route for every chat.
	if name == homeName {
		t.chats[c.wa.SelfChat()] = model.Route{Chat: c.wa.SelfChat(), Session: name}
		t.full[c.wa.SelfChat()] = true
	} else {
		t.elsewhere[c.wa.SelfChat()] = true
	}
	return t
}

// agentChat returns the chat whose own agent is session, or "".
func (c *Core) agentChat(ctx context.Context, session string) string {
	chat, err := c.st.AgentChat(ctx, session)
	if err != nil {
		c.log.Warn("read chat agent", "session", session, "err", err)
	}
	return chat
}

// addAgentChat adds a chat agent's own chat, unless a route sends that chat, or every chat,
// elsewhere.
func (t targets) addAgentChat(chat, name string, routes []model.Route) {
	if chat == "" || routeFor(routes, chat) != "" {
		return
	}
	t.chats[chat] = model.Route{Chat: chat, Session: name}
}

func (t targets) query() []string {
	if t.everyChat != nil {
		return []string{"*"}
	}
	chats := make([]string, 0, len(t.chats))
	for chat := range t.chats {
		chats = append(chats, chat)
	}
	return chats
}

// route returns the route that brings chat to the session, if any.
func (t targets) route(chat string) (model.Route, bool) {
	if r, ok := t.chats[chat]; ok {
		return r, true
	}
	if t.everyChat != nil && !t.elsewhere[chat] {
		return *t.everyChat, true
	}
	return model.Route{}, false
}

// wanted reports whether event e goes to the session: a new message from a person, in a chat
// that the session handles, that the chat's mode shows. views caches each chat's view for one
// batch.
func (c *Core) wanted(ctx context.Context, t targets, views map[string]view, e model.Event) bool {
	chat := e.Message.Chat
	if _, ok := t.route(chat); !ok || !pushable(e) {
		return false
	}
	v, ok := views[chat]
	if !ok {
		v = fullView
		if !t.full[chat] {
			v = c.chatView(ctx, chat)
		}
		views[chat] = v
	}
	return c.sees(ctx, v, e.Message)
}

// pushable reports whether an event is a new message that a person wrote.
func pushable(e model.Event) bool {
	return e.Kind == model.New && (e.Message.Role == model.RoleOwner || e.Message.Role == model.RoleOther)
}

// Sessions lists every known session with its online state and waiting messages.
func (c *Core) Sessions(ctx context.Context) ([]model.Session, error) {
	known, err := c.st.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	online := map[string]bool{}
	for _, cn := range c.conns {
		online[cn.info.Name] = true
	}
	push := map[string]string{}
	for name, sub := range c.subs {
		push[name] = sub.push
	}
	c.mu.Unlock()
	for i := range known {
		s := &known[i]
		s.Online, s.Push = online[s.Name] || push[s.Name] != "", push[s.Name]
		if b, err := c.waiting(ctx, s.Name, nil, 1000); err == nil {
			s.Waiting = len(b.keep)
		}
	}
	return known, nil
}
