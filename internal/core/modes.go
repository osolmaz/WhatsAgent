package core

import (
	"context"
	"strings"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// A chat's mode decides what the session that handles it receives and may read; see the
// "Chat modes" specification in docs. A trigger is a message that the owner typed in a chat
// other than the self-chat that names the agents or answers an agent message. In the window
// mode, a trigger with a wake phrase opens the chat's window, any trigger extends an open window,
// and a trigger with a stop phrase closes it. In the all mode, a trigger with a stop phrase puts
// the chat in the window mode, so that a wake phrase can open a window there again.
const (
	everyChat       = "*"
	settingLength   = "window.length"
	settingLookback = "window.lookback"
	settingWake     = "window.wake"
	settingStop     = "window.stop"
	settingStart    = "window.start"
	settingEnd      = "window.end"
	defaultLength   = time.Hour
	defaultLookback = 30 * time.Minute
	defaultWake     = "start, wake up, monitor, watch, turn on"
	defaultStop     = "stop watching, stop monitoring, turn off"
	defaultStart    = "Now monitoring this chat until {until}. I can read the messages of the last {lookback} " +
		"and everything new. Mention {name} to extend it by {length}, or say \"{name} stop watching\" to end it."
	defaultEnd = "I am done here. Say \"{name} {wake}\" when you need me again."
)

// chatMode returns the mode of chat with its window lengths filled in: the chat's own mode, else
// the mode of "*", else the window mode. A store error hides the chat.
func (c *Core) chatMode(ctx context.Context, chat string) model.ChatMode {
	m, err := c.storedMode(ctx, chat)
	if err != nil {
		c.log.Warn("read chat mode", "chat", chat, "err", err)
		m = model.ChatMode{Mode: model.ModeOff}
	}
	m.Chat = chat
	if m.Length <= 0 {
		m.Length = c.duration(ctx, settingLength, defaultLength)
	}
	if m.Lookback <= 0 {
		m.Lookback = c.duration(ctx, settingLookback, defaultLookback)
	}
	return m
}

func (c *Core) storedMode(ctx context.Context, chat string) (model.ChatMode, error) {
	for _, key := range []string{chat, everyChat} {
		if m, ok, err := c.st.ChatMode(ctx, key); err != nil || ok {
			return m, err
		}
	}
	return model.ChatMode{Mode: model.ModeWindow}, nil
}

// duration returns a duration setting, or fallback when it is unset or unreadable.
func (c *Core) duration(ctx context.Context, key string, fallback time.Duration) time.Duration {
	if d, err := model.ParseDuration(c.setting(ctx, key, "")); err == nil {
		return d
	}
	return fallback
}

// view is what the handler of a chat may see of it right now.
type view struct {
	mode   model.Mode
	window model.Window
	open   bool
}

var fullView = view{mode: model.ModeAll}

// chatView returns what the handler of chat may see of it.
func (c *Core) chatView(ctx context.Context, chat string) view {
	v := view{mode: c.chatMode(ctx, chat).Mode}
	if v.mode == model.ModeWindow {
		w, open, err := c.st.Window(ctx, chat)
		v.window, v.open = w, open && err == nil
	}
	return v
}

// sees reports whether a view shows message m.
func (c *Core) sees(ctx context.Context, v view, m model.Message) bool {
	switch v.mode {
	case model.ModeAll:
		return true
	case model.ModeKeyword:
		return m.Role == model.RoleAgent || c.trigger(ctx, m)
	case model.ModeWindow:
		return v.open && v.window.Covers(m.Time)
	}
	return false
}

// talks reports whether a view lets the handler write to the chat without a send rule.
func (v view) talks() bool {
	return v.mode == model.ModeAll || v.mode == model.ModeKeyword || v.open
}

// viewMessages returns the latest limit messages of chat that view v shows.
func (c *Core) viewMessages(ctx context.Context, v view, chat string, limit int) ([]model.Message, error) {
	switch v.mode {
	case model.ModeAll:
		return c.st.Messages(ctx, chat, limit)
	case model.ModeWindow:
		if !v.open {
			return nil, nil
		}
		return c.st.MessagesBetween(ctx, chat, v.window.From, v.window.Until, limit)
	case model.ModeKeyword:
		all, err := c.st.Messages(ctx, chat, 0)
		return c.latestSeen(ctx, v, all, limit), err
	}
	return nil, nil
}

// latestSeen keeps the latest limit messages that v shows; limit 0 keeps all of them.
func (c *Core) latestSeen(ctx context.Context, v view, msgs []model.Message, limit int) []model.Message {
	var seen []model.Message
	for _, m := range msgs {
		if c.sees(ctx, v, m) {
			seen = append(seen, m)
		}
	}
	if limit > 0 && len(seen) > limit {
		seen = seen[len(seen)-limit:]
	}
	return seen
}

// trigger reports whether m is a trigger.
func (c *Core) trigger(ctx context.Context, m model.Message) bool {
	if m.Role != model.RoleOwner || m.Chat == c.wa.SelfChat() {
		return false
	}
	if mentions(m.Text, c.AgentName(ctx)) {
		return true
	}
	sent, _ := c.st.IsSent(ctx, m.ReplyTo)
	return m.ReplyTo != "" && sent
}

// phrases returns the comma-separated phrases of a setting: the wake phrases that open a window,
// or the stop phrases that close it.
func (c *Core) phrases(ctx context.Context, key, fallback string) []string {
	var phrases []string
	for _, p := range strings.Split(c.setting(ctx, key, fallback), ",") {
		if p = strings.TrimSpace(p); p != "" {
			phrases = append(phrases, p)
		}
	}
	return phrases
}

// says reports whether text contains one of the phrases of a setting, as whole words.
func (c *Core) says(ctx context.Context, text, key, fallback string) bool {
	for _, phrase := range c.phrases(ctx, key, fallback) {
		if mentions(text, phrase) {
			return true
		}
	}
	return false
}

// wakePhrase returns the first wake phrase, which the window messages name.
func (c *Core) wakePhrase(ctx context.Context) string {
	if phrases := c.phrases(ctx, settingWake, defaultWake); len(phrases) > 0 {
		return phrases[0]
	}
	return ""
}

// runTrigger runs the chat's mode for a new live message that is a trigger. In the window mode, a
// stop phrase closes the chat's window, any trigger extends an open window, and a wake phrase
// opens a closed one. In the all mode, a stop phrase puts the chat in the window mode. A message
// that is already stored, which WhatsApp can send twice, runs nothing.
func (c *Core) runTrigger(ctx context.Context, m model.Message) {
	if !c.trigger(ctx, m) {
		return
	}
	if _, stored, err := c.st.Message(ctx, m.Chat, m.ID); err != nil || stored {
		return
	}
	switch mode := c.chatMode(ctx, m.Chat); mode.Mode {
	case model.ModeWindow:
		c.moveWindow(ctx, mode, m.Text)
	case model.ModeAll:
		if c.says(ctx, m.Text, settingStop, defaultStop) {
			c.stopWatching(ctx, m.Chat)
		}
	}
}

// stopWatching puts a chat that is in the all mode in the window mode, with no window open, and
// sends the end message there. Its handler then sees nothing more of the chat, as after a window,
// and the next wake phrase opens a window. The chat keeps its own window lengths, if it has any.
func (c *Core) stopWatching(ctx context.Context, chat string) {
	mode := model.ChatMode{Chat: chat, Mode: model.ModeWindow}
	if own, ok, err := c.st.ChatMode(ctx, chat); err == nil && ok {
		mode.Length, mode.Lookback = own.Length, own.Lookback
	}
	handler, err := c.handler(ctx, chat)
	if err == nil {
		err = c.st.SetChatMode(ctx, mode)
	}
	if err != nil {
		c.log.Warn("stop watching", "chat", chat, "err", err)
		return
	}
	w := model.Window{Chat: chat, Session: handler, Until: time.Now().UTC()}
	full := c.chatMode(ctx, chat)
	c.spawn(func() { c.announce(w, full, settingEnd, defaultEnd) })
}

// moveWindow closes, extends or opens the chat's window for a trigger with text.
func (c *Core) moveWindow(ctx context.Context, mode model.ChatMode, text string) {
	if c.says(ctx, text, settingStop, defaultStop) {
		c.closeWindow(ctx, mode.Chat)
		return
	}
	if _, open, err := c.st.Window(ctx, mode.Chat); err == nil && (open || c.says(ctx, text, settingWake, defaultWake)) {
		c.openWindow(ctx, mode)
	}
}

// openWindow opens the chat's window, or moves its end to the window length from now.
func (c *Core) openWindow(ctx context.Context, mode model.ChatMode) {
	w, open, err := c.windowFor(ctx, mode)
	if err != nil {
		c.log.Warn("open window", "chat", mode.Chat, "err", err)
		return
	}
	if until := time.Now().UTC().Add(mode.Length); until.After(w.Until) {
		w.Until = until
	}
	if err := c.st.SetWindow(ctx, w); err != nil {
		c.log.Warn("store window", "chat", mode.Chat, "err", err)
		return
	}
	if !open {
		c.spawn(func() { c.announce(w, mode, settingStart, defaultStart) })
	}
}

// windowFor returns the chat's open window, or a new one that starts now for the chat's handler.
func (c *Core) windowFor(ctx context.Context, mode model.ChatMode) (model.Window, bool, error) {
	w, open, err := c.st.Window(ctx, mode.Chat)
	if err != nil || open {
		return w, open, err
	}
	handler, err := c.handler(ctx, mode.Chat)
	now := time.Now().UTC()
	return model.Window{Chat: mode.Chat, Session: handler, Opened: now, From: now.Add(-mode.Lookback)}, false, err
}

// closeWindow closes the chat's window and says so in the chat.
func (c *Core) closeWindow(ctx context.Context, chat string) bool {
	w, open, err := c.st.Window(ctx, chat)
	if err != nil || !open {
		return false
	}
	if closed, err := c.st.DeleteWindow(ctx, chat); err != nil || !closed {
		return false
	}
	mode := c.chatMode(ctx, chat)
	c.spawn(func() { c.announce(w, mode, settingEnd, defaultEnd) })
	return true
}

// announce sends a window message to the window's chat, with the handler's prefix.
func (c *Core) announce(w model.Window, mode model.ChatMode, key, fallback string) {
	ctx := c.ctx
	text := strings.NewReplacer("{name}", c.AgentName(ctx), "{length}", model.FormatDuration(mode.Length),
		"{lookback}", model.FormatDuration(mode.Lookback), "{until}", c.when(w.Until), "{wake}", c.wakePhrase(ctx),
	).Replace(c.setting(ctx, key, fallback))
	if _, err := c.sendText(ctx, w.Chat, withPrefix(c.prefixFor(ctx, w.Session, w.Chat), text)); err != nil {
		c.log.Warn("send window message", "chat", w.Chat, "err", err)
	}
}

// runWindows closes windows at their end and stops idle chat agents, until ctx ends.
func (c *Core) runWindows(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.WindowCheck)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.expireWindows(ctx)
			c.stopIdleAgents(ctx)
		}
	}
}

func (c *Core) expireWindows(ctx context.Context) {
	windows, err := c.st.Windows(ctx)
	if err != nil {
		c.log.Warn("read windows", "err", err)
		return
	}
	now := time.Now()
	for _, w := range windows {
		if w.Until.Before(now) {
			c.closeWindow(ctx, w.Chat)
		}
	}
}
