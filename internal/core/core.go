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
	SendText(ctx context.Context, chat, id, text string) error
	React(ctx context.Context, chat, sender, id, emoji string) error
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
	// Post sends one of the agent's answers: home's to the self-chat, a chat agent's to its chat.
	Post func(text string)
	// Fail tells the owner in the self-chat that a turn of the agent failed.
	Fail func(text string)
	// SessionID is the stored ID of the agent's harness session, or "" for a fresh one.
	SessionID string
	// SaveSessionID stores the ID of the agent's harness session, so it continues after a restart.
	SaveSessionID func(id string) error
	// Approve asks the owner whether the agent may use a tool and waits for the answer.
	Approve func(ctx context.Context, tool, input string) bool
}

// AgentFactory starts an agent on one harness.
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

// verdictPattern matches an approval answer such as "yes abcde". The code alphabet skips "l",
// as Claude Code's request IDs do.
var verdictPattern = regexp.MustCompile(`(?i)^\s*(y|yes|n|no)\s+([a-km-z]{5})\s*$`)

// Core is the daemon's logic.
type Core struct {
	cfg Config
	st  *store.Store
	wa  WhatsApp
	log *slog.Logger

	ctx    context.Context
	outbox chan string

	mu       sync.Mutex
	state    string
	conns    map[string]*conn
	subs     map[string]*subscriber
	pending  map[string]*pending
	status   []string
	statusOn bool
	homeStop context.CancelFunc
	homeOn   string
	agents   map[string]*chatAgent

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
	return &Core{
		cfg:     cfg,
		st:      cfg.Store,
		wa:      cfg.WhatsApp,
		log:     cfg.Log,
		ctx:     context.Background(),
		outbox:  make(chan string, 64),
		state:   "starting",
		conns:   map[string]*conn{},
		subs:    map[string]*subscriber{},
		pending: map[string]*pending{},
		agents:  map[string]*chatAgent{},
	}
}

// Start runs the self-chat outbox and the window clock, and starts home when the owner turned
// it on. It returns at once; everything stops when ctx ends.
func (c *Core) Start(ctx context.Context) {
	c.ctx = ctx
	go c.runOutbox(ctx)
	go c.runWindows(ctx)
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
		case text := <-c.outbox:
			if _, err := c.sendText(ctx, c.wa.SelfChat(), text); err != nil {
				c.log.Warn("send to self-chat", "err", err)
			}
		}
	}
}

// post queues a message for the self-chat.
func (c *Core) post(text string) {
	select {
	case c.outbox <- text:
	default:
		c.log.Warn("self-chat outbox full, dropping a message")
	}
}

// say posts a WhatsAgent line in the self-chat at once.
func (c *Core) say(text string) {
	c.post(prefixWhatsAgent + text)
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
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if wait := time.Until(c.lastSend.Add(c.cfg.SendGap)); wait > 0 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(wait):
		}
	}
	id := c.wa.NewID()
	now := time.Now().UTC()
	if err := c.st.RecordSent(ctx, id, chat, now); err != nil {
		return "", err
	}
	if err := c.wa.SendText(ctx, chat, id, text); err != nil {
		return "", err
	}
	c.lastSend = time.Now()
	sent := model.Message{Chat: chat, ID: id, Sender: c.wa.SelfChat(), SenderName: c.wa.OwnName(),
		Time: now, FromMe: true, Role: model.RoleAgent, Text: text}
	if _, err := c.st.Apply(ctx, model.Update{Kind: model.New, Message: sent}, true); err != nil {
		c.log.Warn("store sent message", "err", err)
	}
	return id, nil
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

// OnUpdate stores a message, edit or deletion. A live owner message in the self-chat that is a
// command or an approval answer runs at once; anything else wakes the sessions it is routed to.
func (c *Core) OnUpdate(u model.Update, live bool) {
	ctx := c.ctx
	if u.Kind == model.New {
		u.Message = c.prepare(ctx, u.Message, live)
	}
	seq, err := c.st.Apply(ctx, u, live)
	if err != nil {
		c.log.Warn("store update", "id", u.Message.ID, "err", err)
		return
	}
	if seq != 0 && live {
		c.dispatch(ctx, u, seq)
	}
}

// prepare sets a new message's role and fills in its chat's name. For a live message it runs the
// trigger before the message is stored, so no session sees the message under the window that
// the message changes.
func (c *Core) prepare(ctx context.Context, m model.Message, live bool) model.Message {
	m.Role = c.role(ctx, m)
	c.fillChatName(ctx, m)
	if live {
		c.runTrigger(ctx, m)
	}
	return m
}

// dispatch acts on a stored live update: it runs a self-chat command, or wakes the sessions.
func (c *Core) dispatch(ctx context.Context, u model.Update, seq int64) {
	if u.Kind == model.New && u.Message.Role == model.RoleCommand {
		c.handleCommand(ctx, u.Message.Text)
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
	if m.Chat == c.wa.SelfChat() && isCommand(m.Text) {
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

func (c *Core) handleCommand(ctx context.Context, text string) {
	if v := verdictPattern.FindStringSubmatch(text); v != nil {
		c.answer(ctx, strings.ToLower(v[2]), strings.HasPrefix(strings.ToLower(v[1]), "y"))
		return
	}
	reply, err := c.Run(ctx, Owner, text)
	if err != nil {
		reply = "Error: " + err.Error()
	}
	c.say(reply)
}
