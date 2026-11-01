package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// A chat without a route is handled by its own agent, which WhatsAgent runs on demand. Each chat
// gets a separate agent, so what one chat lets the agents see never reaches another chat.

// chatAgent is a running chat agent.
type chatAgent struct {
	chat   string
	cancel context.CancelFunc

	mu   sync.Mutex
	last time.Time
}

func (a *chatAgent) touch() {
	a.mu.Lock()
	a.last = time.Now()
	a.mu.Unlock()
}

func (a *chatAgent) idleSince() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

// trackedPusher records when a chat agent last got messages, so idle agents can stop.
type trackedPusher struct {
	Pusher
	agent *chatAgent
}

func (p trackedPusher) Push(ctx context.Context, events []model.Event) error {
	if events != nil {
		p.agent.touch()
	}
	return p.Pusher.Push(ctx, events)
}

// handler returns the session that handles chat: its route's, else the route for "*", else the
// chat's own agent.
func (c *Core) handler(ctx context.Context, chat string) (string, error) {
	routes, err := c.st.Routes(ctx)
	if err != nil {
		return "", err
	}
	if session := routeFor(routes, chat); session != "" {
		return session, nil
	}
	return c.chatAgentName(ctx, chat)
}

// routeFor returns the session that a route sends chat to, or "" when no route does.
func routeFor(routes []model.Route, chat string) string {
	session := ""
	for _, r := range routes {
		if r.Chat == chat {
			return r.Session
		}
		if r.Chat == everyChat {
			session = r.Session
		}
	}
	return session
}

// chatAgentName returns the session name of chat's own agent, and picks one on first use.
func (c *Core) chatAgentName(ctx context.Context, chat string) (string, error) {
	if name, err := c.st.ChatAgent(ctx, chat); err != nil || name != "" {
		return name, err
	}
	info, err := c.st.Chat(ctx, chat)
	if err != nil {
		return "", err
	}
	sessions, err := c.st.Sessions(ctx)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, s := range sessions {
		taken[s.Name] = true
	}
	base := "chat-" + slug(info.Label())
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name, c.st.SetChatAgent(ctx, chat, name)
}

// slug turns a chat name into a short session name part: lower-case letters and digits joined by
// dashes.
func slug(label string) string {
	words := strings.FieldsFunc(strings.ToLower(label), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	s := strings.Join(words, "-")
	if len(s) > 24 {
		s = strings.TrimRight(s[:24], "-")
	}
	if s == "" {
		return "chat"
	}
	return s
}

// wakeChatAgent starts the agent of m's chat when the chat has no route and its mode shows m.
// seq is m's event number: a new agent starts right before it.
func (c *Core) wakeChatAgent(ctx context.Context, m model.Message, seq int64) {
	if !c.forChatAgent(ctx, m) {
		return
	}
	name, err := c.chatAgentName(ctx, m.Chat)
	if err != nil {
		c.log.Warn("name the chat agent", "chat", m.Chat, "err", err)
		return
	}
	if err := c.startChatAgent(ctx, m.Chat, name, seq-1); err != nil {
		c.log.Warn("start the chat agent", "session", name, "err", err)
	}
}

// forChatAgent reports whether m goes to its chat's own agent: the chat has no route, and its
// mode shows m.
func (c *Core) forChatAgent(ctx context.Context, m model.Message) bool {
	if m.Chat == c.wa.SelfChat() || c.cfg.ChatAgent == nil || !c.sees(ctx, c.chatView(ctx, m.Chat), m) {
		return false
	}
	routes, err := c.st.Routes(ctx)
	return err == nil && routeFor(routes, m.Chat) == ""
}

// startChatAgent starts the named agent of chat unless it runs already. A session that is new
// starts after event cursor.
func (c *Core) startChatAgent(ctx context.Context, chat, name string, cursor int64) error {
	agentCtx, cancel := context.WithCancel(c.ctx)
	agent := &chatAgent{chat: chat, cancel: cancel, last: time.Now()}
	c.mu.Lock()
	_, running := c.agents[name]
	if !running {
		c.agents[name] = agent
	}
	c.mu.Unlock()
	if running {
		cancel()
		return nil
	}
	pusher, err := c.newChatAgent(ctx, agentCtx, chat, name, cursor)
	if err != nil {
		c.dropAgent(name, agent)
		return err
	}
	go func() {
		defer c.dropAgent(name, agent)
		if err := c.serve(agentCtx, &subscriber{name: name, push: "agent", pusher: trackedPusher{pusher, agent}}); err != nil {
			c.log.Warn("chat agent stopped", "session", name, "err", err)
		}
	}()
	return nil
}

// newChatAgent records the agent's session and starts its harness.
func (c *Core) newChatAgent(ctx, agentCtx context.Context, chat, name string, cursor int64) (Pusher, error) {
	_, cursorErr := c.st.Cursor(ctx, name)
	if err := c.st.TouchSession(ctx, model.Session{Name: name, Harness: "agent", LastSeen: time.Now().UTC()}); err != nil {
		return nil, err
	}
	if cursorErr != nil {
		if err := c.st.SetCursor(ctx, name, cursor); err != nil {
			return nil, err
		}
	}
	info, _ := c.st.Chat(ctx, chat)
	key := name + ".session"
	id, err := c.st.Setting(ctx, key)
	if err != nil {
		return nil, err
	}
	return c.cfg.ChatAgent(agentCtx, AgentHost{
		Session: name, Chat: chat, ChatName: info.Label(), Name: c.AgentName(ctx),
		Post:          func(text string) { c.agentPost(name, chat, text) },
		Fail:          c.say,
		SessionID:     id,
		SaveSessionID: func(id string) error { return c.st.SetSetting(c.ctx, key, id) },
		Approve: func(ctx context.Context, tool, input string) bool {
			return c.Approve(ctx, Caller{Session: name}, tool, input, approvalTimeout)
		},
	})
}

// agentPost sends a chat agent's answer to its chat, following the send rules.
func (c *Core) agentPost(name, chat, text string) {
	text = strings.TrimSpace(text)
	if text == "" || text == "NO_REPLY" {
		return
	}
	if _, err := c.Send(c.ctx, Caller{Session: name}, chat, text); err != nil {
		c.log.Warn("send the chat agent's answer", "session", name, "err", err)
	}
}

// dropAgent stops a chat agent and forgets it.
func (c *Core) dropAgent(name string, agent *chatAgent) {
	c.mu.Lock()
	if c.agents[name] == agent {
		delete(c.agents, name)
	}
	c.mu.Unlock()
	agent.cancel()
}

// stopIdleAgents stops the chat agents whose chat has no open window and that got no messages
// for AgentIdle. A later message starts them again, on the same harness session.
func (c *Core) stopIdleAgents(ctx context.Context) {
	c.mu.Lock()
	agents := make(map[string]*chatAgent, len(c.agents))
	for name, agent := range c.agents {
		agents[name] = agent
	}
	c.mu.Unlock()
	for name, agent := range agents {
		if time.Since(agent.idleSince()) < c.cfg.AgentIdle {
			continue
		}
		if _, open, err := c.st.Window(ctx, agent.chat); err == nil && !open {
			c.dropAgent(name, agent)
		}
	}
}
