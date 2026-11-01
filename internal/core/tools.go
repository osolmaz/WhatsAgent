package core

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// Caller is who calls a tool: the owner, through the CLI, or a checked-in session.
type Caller struct {
	Session string
	owner   bool
}

// Owner is the caller for the owner's own CLI and self-chat commands.
var Owner = Caller{Session: "owner", owner: true}

// ErrNotAllowed is returned when the send rules or routes keep a session from a chat.
var ErrNotAllowed = errors.New("not allowed")

var phonePattern = regexp.MustCompile(`^\+?[0-9][0-9 ()-]{5,}$`)

// resolveChat turns a chat reference into a JID: a JID, a phone number, "self", or a unique
// part of a chat's name.
func (c *Core) resolveChat(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return "", errors.New("no chat given")
	case strings.Contains(ref, "@"):
		return ref, nil
	case strings.EqualFold(ref, "self") || strings.EqualFold(ref, "me"):
		return c.wa.SelfChat(), nil
	case phonePattern.MatchString(ref):
		return strings.Map(digitsOnly, ref) + "@s.whatsapp.net", nil
	}
	return c.chatByName(ctx, ref)
}

func digitsOnly(r rune) rune {
	if r >= '0' && r <= '9' {
		return r
	}
	return -1
}

func (c *Core) chatByName(ctx context.Context, ref string) (string, error) {
	chats, err := c.st.Chats(ctx, ref, 20)
	if err != nil {
		return "", err
	}
	for _, chat := range chats {
		if strings.EqualFold(chat.Name, ref) {
			return chat.JID, nil
		}
	}
	if len(chats) == 1 {
		return chats[0].JID, nil
	}
	if len(chats) == 0 {
		return "", fmt.Errorf("no chat matches %q", ref)
	}
	names := make([]string, 0, 5)
	for i := 0; i < len(chats) && i < 5; i++ {
		names = append(names, chats[i].Label())
	}
	return "", fmt.Errorf("%q matches several chats: %s", ref, strings.Join(names, "; "))
}

// routedTo returns the route that sends chat to session, if any.
func (c *Core) routedTo(ctx context.Context, session, chat string) (model.Route, bool) {
	routes, err := c.st.Routes(ctx)
	if err != nil {
		return model.Route{}, false
	}
	t := c.targets(ctx, session, nil, routes)
	return t.route(chat)
}

// permission applies the send rules to a send from caller to chat. Without a rule, a session may
// write to the self-chat, and to a chat it handles while the chat's mode lets it see the chat.
func (c *Core) permission(ctx context.Context, caller Caller, chat string) (model.Permission, error) {
	if caller.owner {
		return model.Allow, nil
	}
	rules, err := c.st.Rules(ctx)
	if err != nil {
		return "", err
	}
	v, handles := c.viewFor(ctx, caller, chat)
	allowed := chat == c.wa.SelfChat() || (handles && v.talks())
	return model.Decide(rules, caller.Session, chat, allowed), nil
}

// viewFor returns what caller may read of chat, and false when it may read none of it. The owner
// and home read every chat. Another session reads only a chat it handles, as the chat's mode
// shows it, and never the self-chat.
func (c *Core) viewFor(ctx context.Context, caller Caller, chat string) (view, bool) {
	if caller.owner || caller.Session == homeName {
		return fullView, true
	}
	if chat == c.wa.SelfChat() {
		return view{}, false
	}
	if _, ok := c.routedTo(ctx, caller.Session, chat); !ok {
		return view{}, false
	}
	return c.chatView(ctx, chat), true
}

// errHidden says that caller may not read chat.
func errHidden(caller Caller, chat string) error {
	return fmt.Errorf("%w: %s may not read %s; its mode or route hides it", ErrNotAllowed, caller.Session, chat)
}

// ChatMessages is the result of ReadChat.
type ChatMessages struct {
	Chat     model.Chat      `json:"chat"`
	Messages []model.Message `json:"messages"`
}

// ReadChat returns the latest limit messages of a chat that caller may read.
func (c *Core) ReadChat(ctx context.Context, caller Caller, ref string, limit int) (ChatMessages, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return ChatMessages{}, err
	}
	v, ok := c.viewFor(ctx, caller, jid)
	if !ok {
		return ChatMessages{}, errHidden(caller, jid)
	}
	chat, err := c.st.Chat(ctx, jid)
	if err != nil {
		return ChatMessages{}, err
	}
	msgs, err := c.viewMessages(ctx, v, jid, limit)
	return ChatMessages{Chat: chat, Messages: msgs}, err
}

// visibleMessage returns a stored message that caller may read.
func (c *Core) visibleMessage(ctx context.Context, caller Caller, chat, id string) (model.Message, error) {
	v, ok := c.viewFor(ctx, caller, chat)
	if !ok {
		return model.Message{}, errHidden(caller, chat)
	}
	m, found, err := c.st.Message(ctx, chat, id)
	if err != nil {
		return m, err
	}
	if !found || !c.sees(ctx, v, m) {
		return m, fmt.Errorf("no message %s that %s may read in %s", id, caller.Session, chat)
	}
	return m, nil
}

// Search returns messages that contain query, in the chats that caller may read.
func (c *Core) Search(ctx context.Context, caller Caller, query, ref string, limit int) ([]model.Message, error) {
	chat := ""
	if ref != "" {
		jid, err := c.resolveChat(ctx, ref)
		if err != nil {
			return nil, err
		}
		chat = jid
	}
	found, err := c.st.Search(ctx, query, chat, limit)
	if err != nil {
		return nil, err
	}
	readable := found[:0]
	views := map[string]*view{}
	for _, m := range found {
		if v := c.cachedView(ctx, caller, m.Chat, views); v != nil && c.sees(ctx, *v, m) {
			readable = append(readable, m)
		}
	}
	return readable, nil
}

// cachedView returns what caller may read of chat, or nil, and keeps it in views.
func (c *Core) cachedView(ctx context.Context, caller Caller, chat string, views map[string]*view) *view {
	if v, ok := views[chat]; ok {
		return v
	}
	var v *view
	if seen, ok := c.viewFor(ctx, caller, chat); ok {
		v = &seen
	}
	views[chat] = v
	return v
}

// Send sends text to a chat for caller, following the send rules. With the permission ask it
// returns at once, and the message goes out when the owner answers yes.
func (c *Core) Send(ctx context.Context, caller Caller, ref, text string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	text = c.prefixed(ctx, caller, jid, text)
	return c.gate(ctx, caller, jid, "send to", text, func(ctx context.Context) (string, error) {
		id, err := c.sendText(ctx, jid, text)
		return "sent, id " + id, err
	})
}

// prefixed puts the agent prefix in front of a message from an agent session. The owner's own
// messages go out as written.
func (c *Core) prefixed(ctx context.Context, caller Caller, jid, text string) string {
	if caller == Owner {
		return text
	}
	return withPrefix(c.prefixFor(ctx, caller.Session, jid), text)
}

// gate runs act when the send rules allow caller to act on chat, asks the owner first for ask,
// and refuses for deny.
func (c *Core) gate(ctx context.Context, caller Caller, jid, what, detail string,
	act func(ctx context.Context) (string, error)) (string, error) {
	permission, err := c.permission(ctx, caller, jid)
	if err != nil {
		return "", err
	}
	switch permission {
	case model.Allow:
		return act(ctx)
	case model.Ask:
		chat, _ := c.st.Chat(ctx, jid)
		code := c.ask("", fmt.Sprintf("%s wants to %s %s:\n%s", caller.Session, what, chat.Label(), detail),
			func(ctx context.Context, allow bool) string {
				return c.decided(ctx, allow, act)
			})
		return fmt.Sprintf("waiting for the owner's approval in the self-chat (code %s); it goes out after a yes", code), nil
	}
	return "", fmt.Errorf("%w: a send rule denies %s for %s", ErrNotAllowed, jid, caller.Session)
}

func (c *Core) decided(ctx context.Context, allow bool, act func(ctx context.Context) (string, error)) string {
	if !allow {
		return "Not sent."
	}
	if _, err := act(ctx); err != nil {
		return "Failed: " + err.Error()
	}
	return ""
}

// React reacts to a stored message for caller, following the send rules.
func (c *Core) React(ctx context.Context, caller Caller, ref, id, emoji string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	m, err := c.visibleMessage(ctx, caller, jid, id)
	if err != nil {
		return "", err
	}
	sender := m.RawSender
	if sender == "" {
		sender = m.Sender
	}
	return c.gate(ctx, caller, jid, "react "+emoji+" in", m.Body(), func(ctx context.Context) (string, error) {
		return "reacted", c.wa.React(ctx, jid, sender, id, emoji)
	})
}

// DownloadMedia saves the media of a stored message in the media folder and returns its path.
func (c *Core) DownloadMedia(ctx context.Context, caller Caller, ref, id string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	m, err := c.visibleMessage(ctx, caller, jid, id)
	if err != nil {
		return "", err
	}
	if len(m.Raw) == 0 {
		return "", fmt.Errorf("message %s in %s has no media", id, jid)
	}
	data, err := c.wa.Download(ctx, m.Raw)
	if err != nil {
		return "", err
	}
	return c.saveMedia(m, data)
}

func (c *Core) saveMedia(m model.Message, data []byte) (string, error) {
	dir := filepath.Join(c.cfg.MediaDir, strings.Map(safeRune, m.Chat))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := strings.Map(safeRune, m.ID) + extension(m)
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, data, 0o600)
}

// extensions are the usual file extensions of WhatsApp's media types.
var extensions = map[string]string{
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "video/mp4": ".mp4",
	"audio/ogg": ".ogg", "audio/mpeg": ".mp3", "application/pdf": ".pdf",
}

func extension(m model.Message) string {
	if ext := filepath.Ext(m.Filename); ext != "" {
		return strings.Map(safeRune, ext)
	}
	mediaType, _, _ := strings.Cut(m.MimeType, ";")
	if ext, ok := extensions[mediaType]; ok {
		return ext
	}
	if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

// safeRune keeps ASCII letters, digits and ".-_@" in a file name and replaces everything else.
func safeRune(r rune) rune {
	if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(".-_@", r)) {
		return r
	}
	return '_'
}

// RouteHere asks the owner to route a chat to the calling session in mode, which defaults to all.
func (c *Core) RouteHere(ctx context.Context, caller Caller, ref, prefix, mode string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	m, err := routeMode(mode)
	if err != nil {
		return "", err
	}
	route := model.Route{Chat: jid, Session: caller.Session, Prefix: prefix}
	if caller.owner {
		return c.setRoute(ctx, route, m)
	}
	chat, _ := c.st.Chat(ctx, jid)
	code := c.ask("", fmt.Sprintf("%s wants the chat %s routed to it in the %s mode.", caller.Session, chat.Label(), m),
		func(ctx context.Context, allow bool) string {
			if !allow {
				return "Not routed."
			}
			reply, err := c.setRoute(ctx, route, m)
			if err != nil {
				return "Failed: " + err.Error()
			}
			return reply
		})
	return fmt.Sprintf("waiting for the owner's approval in the self-chat (code %s)", code), nil
}

// routeMode reads the mode of a new route, which defaults to all.
func routeMode(s string) (model.Mode, error) {
	if s == "" {
		return model.ModeAll, nil
	}
	return model.ParseMode(s)
}

// setRoute stores a route with its chat's mode.
func (c *Core) setRoute(ctx context.Context, route model.Route, mode model.Mode) (string, error) {
	if err := c.st.SetRoute(ctx, route); err != nil {
		return "", err
	}
	if err := c.st.SetChatMode(ctx, model.ChatMode{Chat: route.Chat, Mode: mode}); err != nil {
		return "", err
	}
	c.wakeAll()
	chat, _ := c.st.Chat(ctx, route.Chat)
	return fmt.Sprintf("%s now goes to %s in the %s mode.", chatLabel(chat), route.Session, mode), nil
}

// chatLabel names a chat for the owner, with "every chat" for "*".
func chatLabel(chat model.Chat) string {
	if chat.JID == everyChat {
		return "Every chat"
	}
	return chat.Label()
}

// Chats lists chats whose name or JID contains query.
func (c *Core) Chats(ctx context.Context, query string, limit int) ([]model.Chat, error) {
	return c.st.Chats(ctx, query, limit)
}

// Export returns every stored message of a chat, oldest first. Only the owner may export.
func (c *Core) Export(ctx context.Context, ref string) ([]model.Message, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return nil, err
	}
	return c.st.Messages(ctx, jid, 0)
}

// Changes is the result of Changes: the chats with any change after a cursor, and the cursor to
// pass next time.
type Changes struct {
	Chats  []string `json:"chats"`
	Cursor int64    `json:"cursor"`
}

// Changes returns the chats with stored changes after cursor.
func (c *Core) Changes(ctx context.Context, cursor int64) (Changes, error) {
	chats, last, err := c.st.ChangedChats(ctx, cursor)
	if chats == nil {
		chats = []string{}
	}
	return Changes{Chats: chats, Cursor: last}, err
}
