package wa

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// ErrNoAccount is returned when WhatsAgent's session store holds no linked device.
var ErrNoAccount = errors.New("no linked WhatsApp account: run `whatsagent link` first")

// Handler receives what happens on the WhatsApp connection.
type Handler interface {
	// OnUpdate receives a live message, edit or deletion.
	OnUpdate(u model.Update)
	// OnChatName receives the display name of a chat.
	OnChatName(jid, name string)
	// OnState receives connection changes, such as "connected" or "logged out".
	OnState(state string)
}

// Run connects to WhatsApp, passes events to h until ctx ends, and then disconnects. whatsmeow
// reconnects by itself after a dropped connection.
func (c *Client) Run(ctx context.Context, h Handler) error {
	if !c.Linked() {
		return ErrNoAccount
	}
	c.cli.AddEventHandler(func(evt any) { c.dispatch(ctx, h, evt) })
	if err := c.cli.ConnectContext(ctx); err != nil {
		return fmt.Errorf("connect to WhatsApp: %w", err)
	}
	<-ctx.Done()
	c.cli.Disconnect()
	return nil
}

// states maps whatsmeow connection events to the state names that Handler.OnState receives.
var states = map[string]string{
	"*events.Connected":        "connected",
	"*events.Disconnected":     "disconnected",
	"*events.LoggedOut":        "logged out",
	"*events.StreamReplaced":   "replaced by another client",
	"*events.TemporaryBan":     "temporarily banned",
	"*events.ClientOutdated":   "client outdated",
	"*events.KeepAliveTimeout": "keepalive timeout",
	// whatsmeow sends KeepAliveRestored when the pings work again after a timeout.
	"*events.KeepAliveRestored": "connected",
}

func (c *Client) dispatch(ctx context.Context, h Handler, evt any) {
	switch e := evt.(type) {
	case *events.Message:
		c.onMessage(ctx, h, e)
	case *events.HistorySync:
		c.onHistory(ctx, h, e)
	case *events.GroupInfo:
		if e.Name != nil {
			h.OnChatName(e.JID.String(), e.Name.Name)
		}
	case *events.Connected:
		h.OnState("connected")
		go c.loadGroupNames(ctx, h)
	default:
		if state, ok := states[fmt.Sprintf("%T", evt)]; ok {
			h.OnState(state)
		}
	}
}

func (c *Client) onMessage(ctx context.Context, h Handler, evt *events.Message) {
	u, ok := parse(evt)
	if !ok {
		u, ok = c.pollVote(ctx, evt)
	}
	if !ok {
		return
	}
	m := &u.Message
	m.Chat = c.canonical(ctx, evt.Info.Chat).String()
	m.Sender = c.canonical(ctx, evt.Info.Sender).String()
	for jid := range m.Mentions {
		m.Mentions[jid] = c.name(ctx, jid)
	}
	if m.FromMe {
		m.SenderName = c.OwnName()
	} else if m.SenderName == "" {
		m.SenderName = c.name(ctx, m.Sender)
	}
	h.OnUpdate(u)
}

// pollVote decrypts a vote on a poll. It reports false for a message that is no vote, and for a
// vote that whatsmeow cannot decrypt, because the poll came before WhatsAgent linked.
func (c *Client) pollVote(ctx context.Context, evt *events.Message) (model.Update, bool) {
	if evt.Message.GetPollUpdateMessage() == nil {
		return model.Update{}, false
	}
	vote, err := c.cli.DecryptPollVote(ctx, evt)
	if err != nil {
		return model.Update{}, false
	}
	return voteUpdate(evt, vote), true
}

// onHistory takes the chat names from the history that WhatsApp sends when an account is linked.
// WhatsAgent never stores the history's messages; see the message storage specification.
func (c *Client) onHistory(ctx context.Context, h Handler, evt *events.HistorySync) {
	for _, conv := range evt.Data.GetConversations() {
		chat, err := types.ParseJID(conv.GetID())
		if err != nil {
			continue
		}
		if name := conv.GetName(); name != "" {
			h.OnChatName(c.canonical(ctx, chat).String(), name)
		}
	}
}

func (c *Client) loadGroupNames(ctx context.Context, h Handler) {
	groups, err := c.cli.GetJoinedGroups(ctx)
	if err != nil {
		return
	}
	for _, g := range groups {
		h.OnChatName(g.JID.String(), g.Name)
	}
}

// canonical returns the phone-number form of a LID address when the session store knows it,
// so routes and send rules can name a person by phone number.
func (c *Client) canonical(ctx context.Context, jid types.JID) types.JID {
	jid = jid.ToNonAD()
	if jid.Server != types.HiddenUserServer {
		return jid
	}
	pn, err := c.cli.Store.LIDs.GetPNForLID(ctx, jid)
	if err != nil || pn.IsEmpty() {
		return jid
	}
	return pn.ToNonAD()
}

// name returns the best known name of a person: their contact name, push name or number.
func (c *Client) name(ctx context.Context, jid string) string {
	parsed, err := types.ParseJID(jid)
	if err != nil {
		return jid
	}
	parsed = c.canonical(ctx, parsed)
	info, err := c.cli.Store.Contacts.GetContact(ctx, parsed)
	if err == nil {
		for _, name := range []string{info.FullName, info.FirstName, info.PushName, info.BusinessName} {
			if name != "" {
				return name
			}
		}
	}
	return parsed.User
}

// ChatName returns the name of a person's direct chat.
func (c *Client) ChatName(ctx context.Context, jid string) string {
	return c.name(ctx, jid)
}

// SelfChat returns the owner's own chat, the "Message Yourself" chat.
func (c *Client) SelfChat() string {
	return c.OwnJID()
}

// OwnName returns the owner's WhatsApp push name.
func (c *Client) OwnName() string {
	return c.cli.Store.PushName
}

// NewID returns a fresh message ID, so the caller can record it before sending.
func (c *Client) NewID() string {
	return c.cli.GenerateMessageID()
}

func parseChat(chat string) (types.JID, error) {
	jid, err := types.ParseJID(chat)
	if err != nil || !strings.Contains(chat, "@") {
		return types.JID{}, fmt.Errorf("bad chat address %q", chat)
	}
	return jid, nil
}

// React sends a reaction to the message id from sender in chat. An empty emoji removes it.
func (c *Client) React(ctx context.Context, chat, sender, id, emoji string) error {
	to, err := parseChat(chat)
	if err != nil {
		return err
	}
	from, _ := types.ParseJID(sender)
	_, err = c.cli.SendMessage(ctx, to, c.cli.BuildReaction(to, from, id, emoji))
	return err
}

// Download fetches and decrypts the media of an encoded message.
func (c *Client) Download(ctx context.Context, raw []byte) ([]byte, error) {
	msg, err := decode(raw)
	if err != nil {
		return nil, err
	}
	media := downloadable(msg)
	if media == nil {
		return nil, errors.New("the message has no media")
	}
	return c.cli.Download(ctx, media)
}

// downloadable returns the media part of a message, or nil.
func downloadable(msg *waE2E.Message) whatsmeow.DownloadableMessage {
	for _, part := range []whatsmeow.DownloadableMessage{msg.GetImageMessage(), msg.GetVideoMessage(),
		msg.GetAudioMessage(), msg.GetDocumentMessage(), msg.GetStickerMessage()} {
		if !reflect.ValueOf(part).IsNil() {
			return part
		}
	}
	return nil
}
