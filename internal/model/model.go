// Package model holds the types that the WhatsApp wrapper, the store, the daemon core and the
// clients share. It depends on nothing else in WhatsAgent.
package model

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Role says who wrote a stored message, from WhatsAgent's point of view.
type Role string

const (
	// RoleOwner is a message that the owner wrote, in any chat.
	RoleOwner Role = "owner"
	// RoleOther is a message that someone other than the owner wrote.
	RoleOther Role = "other"
	// RoleAgent is a message that WhatsAgent sent, for a session or for itself.
	RoleAgent Role = "agent"
	// RoleCommand is a self-chat message for WhatsAgent itself: a command or an approval answer.
	RoleCommand Role = "command"
)

// Trust returns "owner" for the owner's own messages and "other" for everything else. Only
// owner messages count as instructions; the rest is data.
func (r Role) Trust() string {
	if r == RoleOwner || r == RoleCommand {
		return "owner"
	}
	return "other"
}

// Message is one stored WhatsApp message.
type Message struct {
	Chat       string            `json:"chat"`
	ID         string            `json:"id"`
	Sender     string            `json:"sender"`
	SenderName string            `json:"sender_name"`
	Time       time.Time         `json:"time"`
	FromMe     bool              `json:"from_me"`
	Role       Role              `json:"role"`
	Text       string            `json:"text"`
	ReplyTo    string            `json:"reply_to,omitempty"`
	Quote      string            `json:"quote,omitempty"`
	Mentions   map[string]string `json:"mentions,omitempty"`
	MediaType  string            `json:"media_type,omitempty"`
	MimeType   string            `json:"mime_type,omitempty"`
	Filename   string            `json:"filename,omitempty"`
	ReactionTo string            `json:"reaction_to,omitempty"`
	Reaction   string            `json:"reaction,omitempty"`
	Forwarded  bool              `json:"forwarded"`
	Edited     bool              `json:"edited"`
	Deleted    bool              `json:"deleted"`
	// RawSender is the sender address as WhatsApp sent it, which reactions need.
	RawSender string `json:"-"`
	// Raw is the encoded message, which media downloads need.
	Raw []byte `json:"-"`
}

// UpdateKind says what an Update does to the store.
type UpdateKind string

const (
	// New adds a message.
	New UpdateKind = "new"
	// Edit replaces the text of a stored message.
	Edit UpdateKind = "edit"
	// Delete marks a stored message as deleted.
	Delete UpdateKind = "delete"
)

// Update is one change that WhatsApp reports.
type Update struct {
	Kind    UpdateKind
	Message Message
}

// Event is one entry of the event log, with the message it points at.
type Event struct {
	Seq      int64      `json:"seq"`
	Kind     UpdateKind `json:"kind"`
	Live     bool       `json:"live"`
	ChatName string     `json:"chat_name"`
	Message  Message    `json:"message"`
}

// Chat is one WhatsApp chat.
type Chat struct {
	JID      string    `json:"jid"`
	Name     string    `json:"name"`
	LastTime time.Time `json:"last_time"`
}

// Label returns the chat's name, or its JID when the name is unknown.
func (c Chat) Label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.JID
}

// Route links one chat to one session name.
type Route struct {
	Chat    string `json:"chat"`
	Session string `json:"session"`
	// Prefix is added to every message that a session sends on this route.
	Prefix string `json:"prefix,omitempty"`
	// Addressed limits pushes to messages that name the agent or answer an agent message.
	Addressed bool `json:"addressed,omitempty"`
}

// Mode is the outcome of a send rule.
type Mode string

const (
	// Allow sends at once.
	Allow Mode = "allow"
	// Ask sends after the owner answers yes in the self-chat.
	Ask Mode = "ask"
	// Deny refuses the send.
	Deny Mode = "deny"
)

// ParseMode returns the Mode named s.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case Allow, Ask, Deny:
		return m, nil
	}
	return "", fmt.Errorf("unknown send rule mode %q: use allow, ask or deny", s)
}

// Rule is one send rule. Session and Chat can be "*".
type Rule struct {
	Session string `json:"session"`
	Chat    string `json:"chat"`
	Mode    Mode   `json:"mode"`
}

// Decide returns the mode for a send from session to chat. The most specific rule wins: session
// and chat, session and "*", "*" and chat, then "*" and "*". Without a rule, a session may send
// to its routed chats and to the self-chat, and every other send asks the owner.
func Decide(rules []Rule, session, chat string, routedOrSelf bool) Mode {
	best, bestRank := Mode(""), 0
	for _, r := range rules {
		if rank := r.rank(session, chat); rank > bestRank {
			best, bestRank = r.Mode, rank
		}
	}
	if best != "" {
		return best
	}
	if routedOrSelf {
		return Allow
	}
	return Ask
}

// rank scores how specifically r matches a send, with 0 for no match.
func (r Rule) rank(session, chat string) int {
	sessionMatch := r.Session == session || r.Session == "*"
	chatMatch := r.Chat == chat || r.Chat == "*"
	if !sessionMatch || !chatMatch {
		return 0
	}
	rank := 1
	if r.Chat != "*" {
		rank++
	}
	if r.Session != "*" {
		rank += 2
	}
	return rank
}

// Session is what WhatsAgent knows about one session name.
type Session struct {
	Name     string    `json:"name"`
	Harness  string    `json:"harness"`
	Folder   string    `json:"folder"`
	LastSeen time.Time `json:"last_seen"`
	Online   bool      `json:"online"`
	Push     string    `json:"push,omitempty"`
	Waiting  int       `json:"waiting"`
}

var mentionPattern = regexp.MustCompile(`@(\d{5,})`)

// ResolveMentions replaces "@<number>" in text with "@<name>" for every mention that m names.
func (m Message) ResolveMentions() string {
	if len(m.Mentions) == 0 {
		return m.Text
	}
	return mentionPattern.ReplaceAllStringFunc(m.Text, func(found string) string {
		number := found[1:]
		for jid, name := range m.Mentions {
			if strings.HasPrefix(jid, number+"@") && name != "" {
				return "@" + name
			}
		}
		return found
	})
}

// Body returns the text of m as a reader sees it, with the media type, reaction or deletion in
// brackets.
func (m Message) Body() string {
	text := m.ResolveMentions()
	switch {
	case m.Deleted:
		return "[deleted]"
	case m.Reaction != "" || m.ReactionTo != "":
		return fmt.Sprintf("[reaction %s to %s]", m.Reaction, m.ReactionTo)
	case m.MediaType != "":
		return strings.TrimSpace(fmt.Sprintf("[%s] %s", m.MediaType, text))
	}
	return text
}

// Line formats m as one line for an agent: time, sender, text, reply context and the IDs that
// the reply and react tools need.
func Line(m Message, chatName string, loc *time.Location) string {
	var b strings.Builder
	b.WriteString(m.Time.In(loc).Format("2006-01-02 15:04"))
	if chatName != "" {
		fmt.Fprintf(&b, " [%s]", chatName)
	}
	fmt.Fprintf(&b, " %s: %s", senderLabel(m), m.Body())
	if m.Quote != "" {
		fmt.Fprintf(&b, " (reply to %q)", clip(m.Quote, 80))
	}
	if m.Edited {
		b.WriteString(" (edited)")
	}
	fmt.Fprintf(&b, " [id=%s from=%s]", m.ID, m.Sender)
	return b.String()
}

func senderLabel(m Message) string {
	switch {
	case m.Role == RoleAgent:
		return "WhatsAgent"
	case m.FromMe:
		return "owner"
	case m.SenderName != "":
		return m.SenderName
	}
	return m.Sender
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// SortRoutes orders routes by session name, then chat.
func SortRoutes(routes []Route) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Session != routes[j].Session {
			return routes[i].Session < routes[j].Session
		}
		return routes[i].Chat < routes[j].Chat
	})
}
