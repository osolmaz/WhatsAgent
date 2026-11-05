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

// Caller is who calls a tool: the owner, through the CLI or the self-chat, or a checked-in
// session. Only the core sets the unexported fields.
type Caller struct {
	Session string
	owner   bool
	// phone marks a command that the owner wrote in the self-chat, the only source of changes.
	phone bool
	// verified marks a run agent, which Config.Verify recognized by its process. Only a run agent
	// reads chats that are not kept.
	verified bool
}

// Owner is the caller for the owner's own CLI.
var Owner = Caller{Session: "owner", owner: true}

// phoneOwner is the caller for a command that the owner wrote in the self-chat.
var phoneOwner = Caller{Session: "owner", owner: true, phone: true}

// isOwner reports whether caller is the owner in person, through the CLI or the self-chat, and
// not the CLI acting for an agent session.
func (caller Caller) isOwner() bool {
	return caller.owner && caller.Session == Owner.Session
}

// ErrNotAllowed is returned when the send rules or routes keep a session from a chat.
var ErrNotAllowed = errors.New("not allowed")

// readable checks that caller may read chat as far as storage goes: everyone reads kept chats,
// and only a run agent reads the others.
func (c *Core) readable(ctx context.Context, caller Caller, chat string) error {
	if caller.verified {
		return nil
	}
	kept, err := c.st.Kept(ctx, chat)
	if err != nil || kept {
		return err
	}
	return fmt.Errorf("%w: WhatsAgent does not keep %s, and shows its messages only to the agents it runs; "+
		"the owner can keep it with /keep in the self-chat", ErrNotAllowed, chat)
}

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
		return model.Digits(ref) + "@s.whatsapp.net", nil
	}
	return c.chatByName(ctx, ref)
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
// answer in a conversation and write to the self-chat; see mayAnswer.
func (c *Core) permission(ctx context.Context, caller Caller, chat string) (model.Permission, error) {
	if caller.owner {
		return model.Allow, nil
	}
	rules, err := c.st.Rules(ctx)
	if err != nil {
		return "", err
	}
	return model.Decide(rules, caller.Session, chat, c.mayAnswer(ctx, caller.Session, chat)), nil
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
	if err := c.readable(ctx, caller, jid); err != nil {
		return ChatMessages{}, err
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
	if err := c.readable(ctx, caller, chat); err != nil {
		return model.Message{}, err
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

// Search returns messages that contain query, in the chats that caller may read. Only a run agent
// searches chats that are not kept.
func (c *Core) Search(ctx context.Context, caller Caller, query, ref string, limit int) ([]model.Message, error) {
	chat := ""
	if ref != "" {
		jid, err := c.resolveChat(ctx, ref)
		if err != nil {
			return nil, err
		}
		chat = jid
	}
	found, err := c.st.Search(ctx, query, chat, limit, caller.verified)
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

// prefixed puts the agent prefix in front of a message from an agent session. The owner's own
// messages go out as written.
func (c *Core) prefixed(ctx context.Context, caller Caller, jid, text string) string {
	if caller.isOwner() {
		return text
	}
	return withPrefix(c.prefixFor(ctx, caller.Session, jid), text)
}

// proposal is an action on a chat that the send rules decide. action describes act for the
// owner; WhatsAgent writes it from the action's real arguments, never from the agent's words. An
// approval shows media, when it is not nil, with the description.
type proposal struct {
	action string
	media  *model.Media
	act    func(ctx context.Context) (string, error)
}

// gate runs p when the send rules allow caller to act on chat, asks the owner first for ask, and
// refuses for deny.
func (c *Core) gate(ctx context.Context, caller Caller, jid string, p proposal) (string, error) {
	permission, err := c.allowed(ctx, caller, jid)
	if err != nil {
		return "", err
	}
	return c.carry(ctx, caller, permission, p)
}

// allowed returns the permission of the send rules for caller on chat, allow or ask, and an error
// when they deny it.
func (c *Core) allowed(ctx context.Context, caller Caller, jid string) (model.Permission, error) {
	permission, err := c.permission(ctx, caller, jid)
	if err != nil {
		return "", err
	}
	if permission != model.Allow && permission != model.Ask {
		return "", fmt.Errorf("%w: a send rule denies %s for %s", ErrNotAllowed, jid, caller.Session)
	}
	return permission, nil
}

// confirm asks the owner before p runs, whoever the caller is: no send rule, conversation, setting
// or CLI call skips the question. A deny rule for chat still refuses a session; an empty chat
// skips that check.
func (c *Core) confirm(ctx context.Context, caller Caller, chat string, p proposal) (string, error) {
	if chat != "" {
		if _, err := c.allowed(ctx, caller, chat); err != nil {
			return "", err
		}
	}
	return c.carry(ctx, caller, model.Ask, p)
}

// carry runs p at once for allow, and asks the owner first for ask.
func (c *Core) carry(ctx context.Context, caller Caller, permission model.Permission, p proposal) (string, error) {
	if permission == model.Allow {
		return p.act(ctx)
	}
	where := c.approvalChat(ctx, caller.Session)
	code := c.askIn(where, fmt.Sprintf("Approval: %s wants to %s", requester(caller), p.action), p.media,
		func(ctx context.Context, allow bool) string {
			return c.decided(ctx, allow, p.act)
		})
	return fmt.Sprintf("waiting for the owner's yes in %s (code %s); it goes out after a yes",
		c.describeChat(ctx, where), code), nil
}

// requester names who asks for an action in an approval question.
func requester(caller Caller) string {
	if caller.isOwner() {
		return "the whatsagent CLI"
	}
	return "the agent session " + caller.Session
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
	return c.gate(ctx, caller, jid, proposal{
		action: fmt.Sprintf("react %s in %s to this message from %s:\n%s", emoji, c.describeChat(ctx, jid),
			senderLabel(m), m.Body()),
		act: func(ctx context.Context) (string, error) {
			return "reacted", c.wa.React(ctx, jid, sender, id, emoji)
		},
	})
}

// senderLabel names who wrote a message.
func senderLabel(m model.Message) string {
	if m.SenderName != "" {
		return m.SenderName
	}
	return m.Sender
}

// DownloadMedia saves the media of a stored message in the media folder and returns its path. It
// works only in kept chats, because WhatsAgent writes nothing of other chats to disk.
func (c *Core) DownloadMedia(ctx context.Context, caller Caller, ref, id string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	if err := c.readable(ctx, Caller{}, jid); err != nil {
		return "", fmt.Errorf("download_media writes a file, so it works only in kept chats: %w", err)
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
	question := fmt.Sprintf("%s wants the chat %s routed to it in the %s mode.", caller.Session, chat.Label(), m)
	if m == model.ModeOff {
		question += " " + c.offWarning(ctx)
	}
	code := c.ask(question,
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
	return fmt.Sprintf("%s now goes to %s in the %s mode.%s", chatLabel(chat), route.Session, mode,
		c.keptNote(ctx, route)), nil
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

// Export returns every stored message of a kept chat, oldest first. Only the owner may export.
func (c *Core) Export(ctx context.Context, ref string) ([]model.Message, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return nil, err
	}
	if err := c.readable(ctx, Owner, jid); err != nil {
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
