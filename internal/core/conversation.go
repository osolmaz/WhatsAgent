package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// An agent answers in a conversation without asking: a chat that it handles, whose mode lets it
// see the chat, and that sent it a message within replyWindow. Any other message from an agent is
// an action on the owner's behalf, and waits for the owner's yes. WhatsAgent asks in the chat
// where the owner last wrote to that agent, so the owner approves where they asked, unless the
// owner chose the self-chat with /approvals self. WhatsAgent writes the question itself from the
// real action, so an agent cannot misrepresent it.
const (
	replyWindow       = 15 * time.Minute
	settingApprovals  = "approvals.where"
	approvalsHere     = "here"
	approvalsSelfChat = "self"
)

// activity is what a session got recently: when each chat last sent it messages, and where the
// owner last wrote to it.
type activity struct {
	chats     map[string]time.Time
	ownerChat string
	ownerAt   time.Time
}

// noteDelivery records the events that a session got.
func (c *Core) noteDelivery(session string, events []model.Event) {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.activity[session]
	if a == nil {
		a = &activity{chats: map[string]time.Time{}}
		c.activity[session] = a
	}
	for _, e := range events {
		a.chats[e.Message.Chat] = now
		if e.Message.Role == model.RoleOwner {
			a.ownerChat, a.ownerAt = e.Message.Chat, now
		}
	}
}

// inConversation reports whether chat sent session a message within replyWindow.
func (c *Core) inConversation(session, chat string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.activity[session]
	return a != nil && time.Since(a.chats[chat]) < replyWindow
}

// lastActive is when session last got messages, or since when it runs.
func (c *Core) lastActive(session string, since time.Time) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	last := since
	if a := c.activity[session]; a != nil {
		for _, t := range a.chats {
			if t.After(last) {
				last = t
			}
		}
	}
	return last
}

// approvalChat returns where WhatsAgent asks the owner about an action of session: the chat
// where the owner last wrote to session within replyWindow, or else the self-chat.
func (c *Core) approvalChat(ctx context.Context, session string) string {
	self := c.wa.SelfChat()
	if c.setting(ctx, settingApprovals, approvalsHere) == approvalsSelfChat {
		return self
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if a := c.activity[session]; a != nil && a.ownerChat != "" && time.Since(a.ownerAt) < replyWindow {
		return a.ownerChat
	}
	return self
}

// mayAnswer reports whether session may write to chat without a send rule: the self-chat
// always, and a chat it handles while the chat's mode lets it see the chat and the two are in a
// conversation.
func (c *Core) mayAnswer(ctx context.Context, session, chat string) bool {
	if chat == c.wa.SelfChat() {
		return true
	}
	if _, ok := c.routedTo(ctx, session, chat); !ok {
		return false
	}
	return c.chatView(ctx, chat).talks() && c.inConversation(session, chat)
}

// isGroup reports whether a JID names a group.
func isGroup(jid string) bool {
	return strings.HasSuffix(jid, "@g.us")
}

// when names a time for the owner: its clock time on the same day, and its weekday too on another
// day.
func (c *Core) when(t time.Time) string {
	t, now := t.In(c.cfg.Location), time.Now().In(c.cfg.Location)
	if t.Format(time.DateOnly) == now.Format(time.DateOnly) {
		return t.Format("15:04")
	}
	return t.Format("Mon 15:04")
}

// chatKind names what kind of chat a JID is, for the owner.
func chatKind(jid string) string {
	switch {
	case isGroup(jid):
		return "group"
	case strings.HasSuffix(jid, "@newsletter"):
		return "channel"
	}
	return "direct chat"
}

// describeChat names a chat and its kind, such as `"Team" (group)`.
func (c *Core) describeChat(ctx context.Context, jid string) string {
	if jid == c.wa.SelfChat() {
		return "your self-chat"
	}
	chat, _ := c.st.Chat(ctx, jid)
	return fmt.Sprintf("%q (%s)", chat.Label(), chatKind(jid))
}

// errForged is returned when an agent's message would look like one of WhatsAgent's own.
var errForged = errors.New("an agent's message may not start with ⚙️, which marks WhatsAgent's own messages")

// forged reports whether text would pass for one of WhatsAgent's own messages.
func forged(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), strings.TrimSpace(prefixWhatsAgent))
}

func cmdApprovals(ctx context.Context, c *Core, args string) (string, error) {
	switch arg := strings.ToLower(args); arg {
	case "":
	case approvalsHere, approvalsSelfChat:
		if err := c.st.SetSetting(ctx, settingApprovals, arg); err != nil {
			return "", err
		}
	default:
		return "", errors.New("use /approvals here or /approvals self")
	}
	if c.setting(ctx, settingApprovals, approvalsHere) == approvalsSelfChat {
		return "WhatsAgent asks you about an agent's messages to other chats here, in your self-chat.", nil
	}
	return "WhatsAgent asks you about an agent's messages to other chats in the chat where you last wrote to " +
		"that agent, or here when you did not write to it in the last 15 minutes.", nil
}
