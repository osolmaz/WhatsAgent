// Package core is the WhatsAgent daemon's logic: it stores what WhatsApp reports, runs
// self-chat commands, routes messages to sessions, applies send rules and asks the owner for
// approvals. It knows nothing about whatsmeow, HTTP or a particular harness.
package core

import (
	"context"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/store"
)

// WhatsApp is the part of the WhatsApp connection that the core uses.
type WhatsApp interface {
	SelfChat() string
	OwnName() string
	NewID() string
	// Send sends one message under the message ID id.
	Send(ctx context.Context, chat, id string, out model.Outgoing) error
	// Upload uploads a file once, for any number of messages to carry.
	Upload(ctx context.Context, f model.File) (model.Media, error)
	// Edit replaces the text of the owner's message id, and Revoke deletes a message for everyone:
	// the owner's own when sender is "", or sender's in a group that the owner administers.
	Edit(ctx context.Context, chat, id, text string) error
	Revoke(ctx context.Context, chat, sender, id string) error
	React(ctx context.Context, chat, sender, id, emoji string) error
	// Typing shows the owner as typing in chat, and MarkRead marks the message id from sender,
	// sent at sent, and the chat's earlier messages as read.
	Typing(ctx context.Context, chat string) error
	MarkRead(ctx context.Context, chat, sender, id string, sent time.Time) error
	// Groups: what WhatsApp tells about one, and creating, changing and joining one.
	GroupInfo(ctx context.Context, chat string) (model.Group, error)
	CreateGroup(ctx context.Context, name string, members []string) (string, error)
	ChangeGroup(ctx context.Context, chat string, change model.GroupChange) (string, error)
	JoinGroup(ctx context.Context, link string) (string, error)
	// People: the owner's address book, what WhatsApp shows of a person, and the owner's profile.
	Contacts(ctx context.Context, query string) ([]model.Contact, error)
	ContactInfo(ctx context.Context, person string) (model.Contact, error)
	SetProfile(ctx context.Context, p model.Profile) error
	Download(ctx context.Context, raw []byte) ([]byte, error)
	ChatName(ctx context.Context, jid string) string
}

// Pusher delivers routed events into one session.
type Pusher interface {
	// Push delivers events. A nil slice is a heartbeat that checks the session is still there.
	Push(ctx context.Context, events []model.Event) error
	// Verdict passes the owner's answer to a permission prompt that the session relayed.
	Verdict(ctx context.Context, requestID string, allow bool) error
}

// AgentHost is what an agent that WhatsAgent runs itself gets from the core: home, or a chat's
// own agent.
type AgentHost struct {
	// Session is the agent's session name, which its WhatsAgent MCP tools check in with.
	Session string
	// Chat is the chat of a chat agent, and "" for home.
	Chat string
	// ChatName is the chat's display name.
	ChatName string
	// Name is the agents' name when the agent starts.
	Name string
	// Post sends one of the agent's answers, for an agent whose harness hands WhatsAgent its
	// final answer in a turn: home's to the self-chat, a chat agent's to its chat.
	Post func(text string)
	// SessionID is the stored ID of the agent's harness session, or "" for a fresh one.
	SessionID string
	// SaveSessionID stores the ID of the agent's harness session, so it continues after a restart.
	SaveSessionID func(id string) error
	// Approve asks the owner whether the agent may use a tool and waits for the answer.
	Approve func(ctx context.Context, tool, input string) bool
	// Auto turns on the harness's own automatic approval: Claude Code's auto mode, or Codex's
	// auto review. The harness still sends the requests it will not decide to Approve.
	Auto bool
}

// AgentFactory starts an agent on one harness. It returns the Pusher that delivers the agent's
// messages, or nil for an agent that gets them through its own WhatsAgent channel. The agent
// stops when ctx ends.
type AgentFactory func(ctx context.Context, host AgentHost) (Pusher, error)

// Config holds what the core needs.
type Config struct {
	Store    *store.Store
	WhatsApp WhatsApp
	Log      *slog.Logger
	// MediaDir is the folder where download_media saves files.
	MediaDir string
	Location *time.Location
	// Homes maps a harness name, "claude" or "codex", to the code that starts home on it.
	Homes map[string]AgentFactory
	// ChatAgent starts a chat's own agent. Without it, chats without a route reach no agent.
	ChatAgent AgentFactory
	// SendGap is the shortest time between two sends, because WhatsApp can ban numbers that
	// send automated messages quickly.
	SendGap time.Duration
	// StatusDelay is how long status lines wait, so lines close together merge into one message.
	StatusDelay time.Duration
	// Heartbeat is how often an idle push session is checked.
	Heartbeat time.Duration
	// WindowCheck is how often WhatsAgent closes windows that reached their end.
	WindowCheck time.Duration
	// AgentIdle is how long a chat agent without an open window and without new messages keeps
	// running before WhatsAgent stops it.
	AgentIdle time.Duration
	// Recent is how long the recent buffer keeps the messages of chats that are not kept.
	Recent time.Duration
	// Verify reports whether the session process behind a check-in is the run agent of session,
	// which ctx identifies. Only a verified session reads chats that are not kept.
	Verify func(ctx context.Context, session string) bool
	// PageURL is the address of the browser page, which /web sends to the self-chat.
	PageURL string
}

const (
	// prefixWhatsAgent marks WhatsAgent's own self-chat lines.
	prefixWhatsAgent = "⚙️ "
	// prefixHome marks home's replies in the self-chat.
	prefixHome = "🏠 "
	homeName   = "home"
	eventBatch = 200
	// approvalTimeout is how long a tool approval waits for the owner's answer.
	approvalTimeout = 30 * time.Minute
)

// verdictPattern matches an approval answer such as "yes 42".
var verdictPattern = regexp.MustCompile(`(?i)^\s*(y|yes|n|no)\s*(\d{2,3})\s*$`)

// Core is the daemon's logic.
type Core struct {
	cfg Config
	st  *store.Store
	wa  WhatsApp
	log *slog.Logger

	ctx    context.Context
	outbox chan outgoing

	mu       sync.Mutex
	state    string
	conns    map[string]*conn
	subs     map[string]*subscriber
	pending  map[string]*pending
	closed   map[string]time.Time
	status   []string
	statusOn bool
	homeStop context.CancelFunc
	homeOn   string
	agents   map[string]*chatAgent
	// activity is what each session got recently.
	activity map[string]*activity

	sendMu   sync.Mutex
	lastSend time.Time
}

// New returns a core for cfg. Call Start before passing WhatsApp events to it.
func New(cfg Config) *Core {
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.WindowCheck <= 0 {
		cfg.WindowCheck = 10 * time.Second
	}
	if cfg.AgentIdle <= 0 {
		cfg.AgentIdle = 15 * time.Minute
	}
	if cfg.Recent <= 0 {
		cfg.Recent = 24 * time.Hour
	}
	return &Core{
		cfg:      cfg,
		st:       cfg.Store,
		wa:       cfg.WhatsApp,
		log:      cfg.Log,
		ctx:      context.Background(),
		outbox:   make(chan outgoing, 64),
		state:    "starting",
		conns:    map[string]*conn{},
		subs:     map[string]*subscriber{},
		pending:  map[string]*pending{},
		closed:   map[string]time.Time{},
		agents:   map[string]*chatAgent{},
		activity: map[string]*activity{},
	}
}

// Start keeps the self-chat, deletes stored messages of chats that are not kept, runs the
// self-chat outbox, the window clock and the recent buffer's cleanup, and starts home when the
// owner turned it on. It returns at once; everything stops when ctx ends.
func (c *Core) Start(ctx context.Context) {
	c.ctx = ctx
	if _, _, err := c.st.Keep(ctx, c.wa.SelfChat()); err != nil {
		c.log.Warn("keep the self-chat", "err", err)
	}
	if n, err := c.st.Purge(ctx); err != nil || n > 0 {
		c.log.Info("deleted the stored messages of chats that are not kept", "messages", n, "err", err)
	}
	go c.runOutbox(ctx)
	go c.runWindows(ctx)
	go c.runPrune(ctx)
	if harness, _ := c.st.Setting(ctx, "home"); harness != "" {
		if err := c.startHome(ctx, harness); err != nil {
			c.log.Warn("start home", "harness", harness, "err", err)
		}
	}
}

func (c *Core) runOutbox(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case o := <-c.outbox:
			if _, err := c.send(ctx, c.wa.SelfChat(), model.Outgoing{Text: o.text, Media: o.media}, !o.private); err != nil {
				c.log.Warn("send to self-chat", "err", err)
			}
		}
	}
}

// outgoing is one queued self-chat message, with media when media is not nil. A private one, such
// as the browser page's link, goes to the phone without being stored, so no reader of the
// self-chat finds it.
type outgoing struct {
	text    string
	media   *model.Media
	private bool
}

// runPrune drops messages older than Config.Recent from the recent buffer, every minute or at a
// tenth of Config.Recent when that is shorter.
func (c *Core) runPrune(ctx context.Context) {
	ticker := time.NewTicker(min(c.cfg.Recent/10, time.Minute))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := c.st.Prune(ctx, time.Now().Add(-c.cfg.Recent)); err != nil {
				c.log.Warn("prune the recent buffer", "err", err)
			}
		}
	}
}

// post queues a message for the self-chat.
func (c *Core) post(text string) {
	c.queue(outgoing{text: text})
}

func (c *Core) queue(o outgoing) {
	select {
	case c.outbox <- o:
	default:
		c.log.Warn("self-chat outbox full, dropping a message")
	}
}

// say posts a WhatsAgent line in the self-chat at once.
func (c *Core) say(text string) {
	c.post(prefixWhatsAgent + text)
}

// sayPrivately posts a WhatsAgent line in the self-chat without storing it.
func (c *Core) sayPrivately(text string) {
	c.queue(outgoing{text: prefixWhatsAgent + text, private: true})
}

// PostHome posts a reply from home in the self-chat.
func (c *Core) PostHome(text string) {
	text = strings.TrimSpace(text)
	if text == "" || text == "NO_REPLY" {
		return
	}
	c.post(prefixHome + text)
}

// statusLine queues a status line. Lines that arrive within StatusDelay go out as one message.
func (c *Core) statusLine(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = append(c.status, line)
	if c.statusOn {
		return
	}
	c.statusOn = true
	time.AfterFunc(c.cfg.StatusDelay, c.flushStatus)
}

func (c *Core) flushStatus() {
	c.mu.Lock()
	lines := c.status
	c.status, c.statusOn = nil, false
	c.mu.Unlock()
	c.say(strings.Join(lines, "\n"))
}

// sendText sends text to chat, spacing sends by SendGap, and stores the message as WhatsAgent's.
func (c *Core) sendText(ctx context.Context, chat, text string) (string, error) {
	return c.send(ctx, chat, model.Outgoing{Text: text}, true)
}

// send sends out to chat, spacing sends by SendGap, and stores the message as WhatsAgent's when
// store is true.
func (c *Core) send(ctx context.Context, chat string, out model.Outgoing, store bool) (string, error) {
	out, err := c.sendCaption(ctx, chat, out, store)
	if err != nil {
		return "", err
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if err := c.waitGap(ctx); err != nil {
		return "", err
	}
	id := c.wa.NewID()
	now := time.Now().UTC()
	if err := c.st.RecordSent(ctx, id, chat, now); err != nil {
		return "", err
	}
	if err := c.wa.Send(ctx, chat, id, out); err != nil {
		return "", err
	}
	c.lastSend = time.Now()
	if store {
		c.storeSent(ctx, model.Message{Chat: chat, ID: id, Time: now}, out)
	}
	return id, nil
}

// sendCaption sends the text of a message that WhatsApp shows without one, such as audio or a
// location, as a message of its own first, as the answer and with the mentions. It returns what
// is left to send.
func (c *Core) sendCaption(ctx context.Context, chat string, out model.Outgoing, store bool) (model.Outgoing, error) {
	if out.Captioned() || out.Text == "" {
		return out, nil
	}
	_, err := c.send(ctx, chat, model.Outgoing{Text: out.Text, ReplyTo: out.ReplyTo, Mentions: out.Mentions}, store)
	return model.Outgoing{Media: out.Media, Location: out.Location}, err
}

// storeSent stores a message that WhatsAgent sent. A stored file keeps its encoded message, so
// download_media can fetch it again.
func (c *Core) storeSent(ctx context.Context, sent model.Message, out model.Outgoing) {
	sent.Sender, sent.SenderName, sent.FromMe, sent.Role = c.wa.SelfChat(), c.wa.OwnName(), true, model.RoleAgent
	sent.Text = out.Text
	if media := sentMedia(out); media != nil {
		sent.MediaType, sent.MimeType, sent.Filename, sent.Raw = media.Type, media.MimeType, media.Filename, media.Raw
	}
	switch {
	case out.Forward != nil:
		sent.Text, sent.Forwarded = out.Forward.Text, true
	case out.Poll != nil:
		sent.Text = out.Poll.Text()
	case out.Location != nil:
		sent.Text = strings.TrimSpace(out.Location.Name + " " + out.Location.Address)
	}
	if r := out.ReplyTo; r != nil {
		sent.ReplyTo, sent.Quote = r.ID, r.Body()
	}
	if _, err := c.st.Apply(ctx, model.Update{Kind: model.New, Message: sent}); err != nil {
		c.log.Warn("store sent message", "err", err)
	}
}

// sentMedia returns what a sent message carries besides text, as stored media details: the file,
// the forwarded message's media, a poll or a location.
func sentMedia(out model.Outgoing) *model.Media {
	switch {
	case out.Forward != nil:
		f := out.Forward
		return &model.Media{Type: f.MediaType, MimeType: f.MimeType, Filename: f.Filename, Raw: f.Raw}
	case out.Poll != nil:
		return &model.Media{Type: "poll"}
	case out.Location != nil:
		return &model.Media{Type: "location"}
	}
	return out.Media
}

// waitGap waits until SendGap has passed since the last send. The caller holds c.sendMu.
func (c *Core) waitGap(ctx context.Context) error {
	wait := time.Until(c.lastSend.Add(c.cfg.SendGap))
	if wait <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// OnState records a WhatsApp connection change.
func (c *Core) OnState(state string) {
	c.mu.Lock()
	first := c.state == "starting" && state == "connected"
	c.state = state
	c.mu.Unlock()
	c.log.Info("whatsapp", "state", state)
	if first {
		c.greet(c.ctx)
	}
}

func (c *Core) greet(ctx context.Context) {
	if done, _ := c.st.Setting(ctx, "welcomed"); done == "" {
		c.say("Welcome to WhatsAgent. This chat is the control room: messages that start with / " +
			"are commands, and other messages go to home.\n\n" + helpText())
		_ = c.st.SetSetting(ctx, "welcomed", time.Now().UTC().Format(time.RFC3339))
		return
	}
	c.statusLine("WhatsAgent is running. " + c.homeLine())
}

// OnChatName records a chat's display name.
func (c *Core) OnChatName(jid, name string) {
	if name == "" {
		return
	}
	if err := c.st.SetChatName(c.ctx, jid, name); err != nil {
		c.log.Warn("store chat name", "chat", jid, "err", err)
	}
}

// OnUpdate stores a live message, edit or deletion: in the file for a kept chat, in the recent
// buffer otherwise. An owner message in the self-chat that is a command or an approval answer
// runs at once; anything else wakes the sessions it is routed to.
func (c *Core) OnUpdate(u model.Update) {
	ctx := c.ctx
	if u.Kind == model.New {
		u.Message = c.prepare(ctx, u.Message)
	}
	seq, err := c.st.Apply(ctx, u)
	if err != nil {
		c.log.Warn("store update", "id", u.Message.ID, "err", err)
		return
	}
	if seq != 0 {
		c.dispatch(ctx, u, seq)
	}
}

// prepare sets a new message's role, fills in its chat's name, and runs the trigger before the
// message is stored, so no session sees the message under the window that the message changes.
func (c *Core) prepare(ctx context.Context, m model.Message) model.Message {
	m.Role = c.role(ctx, m)
	if m.PollVote != nil {
		m = c.nameVote(ctx, m)
	}
	c.fillChatName(ctx, m)
	c.runTrigger(ctx, m)
	return m
}

// dispatch acts on a stored live update: it runs a self-chat command, or wakes the sessions.
func (c *Core) dispatch(ctx context.Context, u model.Update, seq int64) {
	if u.Kind == model.New && u.Message.Role == model.RoleCommand {
		c.handleCommand(ctx, u.Message.Chat, u.Message.Text)
		return
	}
	if u.Kind == model.New {
		c.wakeChatAgent(ctx, u.Message, seq)
	}
	c.wakeAll()
}

func (c *Core) role(ctx context.Context, m model.Message) model.Role {
	if sent, _ := c.st.IsSent(ctx, m.ID); sent {
		return model.RoleAgent
	}
	if !m.FromMe {
		return model.RoleOther
	}
	if (m.Chat == c.wa.SelfChat() && isCommand(m.Text)) || c.verdict(m) {
		return model.RoleCommand
	}
	return model.RoleOwner
}

func isCommand(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "/") || verdictPattern.MatchString(text)
}

// fillChatName names a direct chat after the person when the chat has no name yet.
func (c *Core) fillChatName(ctx context.Context, m model.Message) {
	if !strings.HasSuffix(m.Chat, "@s.whatsapp.net") {
		return
	}
	chat, err := c.st.Chat(ctx, m.Chat)
	if err != nil || chat.Name != "" {
		return
	}
	name := m.SenderName
	if m.FromMe || name == "" {
		name = c.wa.ChatName(ctx, m.Chat)
	}
	c.OnChatName(m.Chat, name)
}

// handleCommand runs a command line or an approval answer that the owner wrote in chat. Only
// answers come from chats other than the self-chat.
func (c *Core) handleCommand(ctx context.Context, chat, text string) {
	if v := verdictPattern.FindStringSubmatch(text); v != nil {
		c.answer(ctx, chat, strings.ToLower(v[2]), strings.HasPrefix(strings.ToLower(v[1]), "y"))
		return
	}
	reply, err := c.Run(ctx, phoneOwner, text)
	if err != nil {
		reply = "Error: " + err.Error()
	}
	if name, _ := parseCommand(text); phoneOnly[name] {
		c.sayPrivately(reply)
		return
	}
	c.say(reply)
}
