// Package model holds the types that the WhatsApp wrapper, the store, the daemon core and the
// clients share. It depends on nothing else in WhatsAgent.
package model

import (
	"fmt"
	"regexp"
	"slices"
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
	// PollVote holds the SHA-256 hashes, in hex, of the options that a vote picks, until the core
	// turns them into the options' names. A vote's ReplyTo is the poll.
	PollVote []string `json:"-"`
}

// MaxFileSize is the largest file that WhatsAgent sends, in bytes. The daemon holds a file in
// memory while it uploads it.
const MaxFileSize = 100_000_000

// File is a file that a session sends: its name and contents, as the CLI or the WhatsAgent MCP
// server read it from the caller's disk.
type File struct {
	Name string `json:"name"`
	Data []byte `json:"data"`
}

// Media is a file that WhatsAgent uploaded to WhatsApp, ready to go out in a message to any
// chat.
type Media struct {
	// Type is how WhatsApp shows the file: image, video, audio or document, as in
	// Message.MediaType.
	Type     string
	MimeType string
	Filename string
	Size     int
	// Raw is the encoded media message without a caption. Only the WhatsApp wrapper reads it.
	Raw []byte
}

// Captioned reports whether WhatsApp shows a caption with the media. Audio has none.
func (m Media) Captioned() bool {
	return m.Type != "audio"
}

// Describe names the media for the owner, such as "photo chart.png (84 KB)".
func (m Media) Describe() string {
	noun := map[string]string{"image": "photo", "video": "video", "audio": "audio file"}[m.Type]
	if noun == "" {
		noun = "document"
	}
	return fmt.Sprintf("%s %s (%s)", noun, m.Filename, SizeLabel(m.Size))
}

// Outgoing is one message that WhatsAgent sends: text, media with the text as its caption, a poll,
// a location, or a forwarded message.
type Outgoing struct {
	Text  string
	Media *Media
	// ReplyTo is the message that this one answers, which WhatsApp shows quoted above it.
	ReplyTo *Message
	// Mentions are the JIDs of the people that the text mentions.
	Mentions []string
	// Forward is a stored message to forward. Text and Media stay empty then.
	Forward *Message
	// Poll is a poll to send; Text stays empty, because the question carries the words.
	Poll *Poll
	// Location is a place to send.
	Location *Location
}

// Captioned reports whether the message shows Text with its content. Audio and locations show
// none, so their text goes out as a message of its own.
func (o Outgoing) Captioned() bool {
	if o.Location != nil {
		return false
	}
	return o.Media == nil || o.Media.Captioned()
}

// Poll is a WhatsApp poll.
type Poll struct {
	Question string
	Options  []string
	// Multiple lets each voter pick any number of options, and not only one.
	Multiple bool
}

// Text writes a poll as WhatsAgent stores it: the question, then one line per option.
func (p Poll) Text() string {
	return p.Question + "\n○ " + strings.Join(p.Options, "\n○ ")
}

// PollOptions returns the options of a poll that WhatsAgent stored as Poll.Text writes it.
func PollOptions(text string) []string {
	var options []string
	for line := range strings.Lines(text) {
		if option, ok := strings.CutPrefix(strings.TrimRight(line, "\n"), "○ "); ok {
			options = append(options, option)
		}
	}
	return options
}

// Location is a place on the map.
type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Name      string  `json:"name,omitempty"`
	Address   string  `json:"address,omitempty"`
}

// MentionsIn returns the JIDs of the people that text mentions as @ and a phone number, which
// WhatsApp shows as their names.
func MentionsIn(text string) []string {
	var jids []string
	for _, m := range mentionPattern.FindAllStringSubmatch(text, -1) {
		if jid := m[1] + "@s.whatsapp.net"; !slices.Contains(jids, jid) {
			jids = append(jids, jid)
		}
	}
	return jids
}

// SizeLabel writes a byte count for people, such as "840 bytes", "84 KB" or "1.2 MB".
func SizeLabel(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d bytes", n)
	case n < 1000*1000:
		return fmt.Sprintf("%d KB", n/1000)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + " MB"
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

// Route links one chat to the session that handles it. A chat without a route is handled by
// the route for "*", or else by the chat's own agent, which WhatsAgent runs.
type Route struct {
	Chat    string `json:"chat"`
	Session string `json:"session"`
	// Prefix replaces the owner's agent prefix for the session's messages in this chat.
	Prefix string `json:"prefix,omitempty"`
}

// Permission is the outcome of a send rule.
type Permission string

const (
	// Allow sends at once.
	Allow Permission = "allow"
	// Ask sends after the owner answers yes in the self-chat.
	Ask Permission = "ask"
	// Deny refuses the send.
	Deny Permission = "deny"
)

// ParsePermission returns the Permission named s.
func ParsePermission(s string) (Permission, error) {
	switch p := Permission(s); p {
	case Allow, Ask, Deny:
		return p, nil
	}
	return "", fmt.Errorf("unknown send rule %q: use allow, ask or deny", s)
}

// Rule is one send rule. Session and Chat can be "*".
type Rule struct {
	Session    string     `json:"session"`
	Chat       string     `json:"chat"`
	Permission Permission `json:"permission"`
}

// Decide returns the permission for a send from session to chat. The most specific rule wins:
// session and chat, session and "*", "*" and chat, then "*" and "*". Without a rule, a send that
// the session's chat mode allows goes out, and every other send asks the owner.
func Decide(rules []Rule, session, chat string, allowed bool) Permission {
	best, bestRank := Permission(""), 0
	for _, r := range rules {
		if rank := r.rank(session, chat); rank > bestRank {
			best, bestRank = r.Permission, rank
		}
	}
	if best != "" {
		return best
	}
	if allowed {
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
	// RunAgent is true while the session is an agent that WhatsAgent runs and recognized by its
	// process, which may read chats that are not kept.
	RunAgent bool `json:"run_agent,omitempty"`
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
