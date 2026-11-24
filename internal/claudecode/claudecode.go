// Package claudecode holds what WhatsAgent does only for Claude Code: the hooks that steer routed
// messages into a running turn, and the steering state that decides whether a message goes through
// the channel at once or waits for the next hook.
package claudecode

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// HookTool is the WhatsAgent MCP tool that the steering hooks call.
const HookTool = "claude_code_hook"

// The hook events that steering uses.
const (
	UserPromptSubmit = "UserPromptSubmit"
	PreToolUse       = "PreToolUse"
	PostToolBatch    = "PostToolBatch"
	Stop             = "Stop"
	StopFailure      = "StopFailure"
)

var hookEvents = []string{UserPromptSubmit, PreToolUse, PostToolBatch, Stop, StopFailure}

// HookSettings returns the Claude Code settings, as JSON for --settings, that call HookTool on the
// MCP server named server for every steering event, with the event name and the subagent ID.
func HookSettings(server string) string {
	hook := map[string]any{
		"type": "mcp_tool", "server": server, "tool": HookTool,
		"input": map[string]string{"event": "${hook_event_name}", "agent_id": "${agent_id}"},
	}
	hooks := map[string]any{}
	for _, event := range hookEvents {
		hooks[event] = []map[string]any{{"hooks": []any{hook}}}
	}
	data, _ := json.Marshal(map[string]any{"hooks": hooks})
	return string(data)
}

// Steering decides for one session whether a routed event goes through the channel at once or
// waits for a hook. The session is busy from a turn's first hook to its Stop; events that arrive
// meanwhile are held, and the next hook that can add context takes them.
type Steering struct {
	push  func([]model.Event)
	stale time.Duration

	mu    sync.Mutex
	busy  bool
	held  []model.Event
	last  time.Time
	timer *time.Timer
}

// NewSteering returns the steering state of a session. push sends events through the channel.
// When no hook has come for stale while events are held, the session counts as idle again, because
// Claude Code fires no hook when the owner interrupts a turn.
func NewSteering(push func([]model.Event), stale time.Duration) *Steering {
	return &Steering{push: push, stale: stale}
}

// Deliver passes routed events on: through the channel when the session is idle, and into the held
// events when it is busy. Like idle, it pushes with mu held, so a hook cannot come between the check
// and the push.
func (s *Steering) Deliver(events []model.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.busy {
		s.push(events)
		return
	}
	s.held = append(s.held, events...)
	s.arm()
}

// Hook takes one hook call and returns the held events that the hook passes to Claude. It ignores
// hooks from subagents, whose agentID is set, so events reach only the main agent.
func (s *Steering) Hook(event, agentID string) []model.Event {
	if agentID != "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()
	switch event {
	case UserPromptSubmit, PostToolBatch:
		s.busy = true
		return s.take()
	case PreToolUse:
		s.busy = true
	case Stop:
		held := s.take()
		s.busy = len(held) > 0
		return held
	case StopFailure:
		s.idle()
	}
	return nil
}

func (s *Steering) take() []model.Event {
	held := s.held
	s.held = nil
	return held
}

// arm starts the fallback timer for held events, unless it runs already.
func (s *Steering) arm() {
	if s.timer == nil {
		s.timer = time.AfterFunc(s.stale, s.expire)
	}
}

// expire marks the session idle when no hook has come for stale, and checks again later otherwise.
func (s *Steering) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timer = nil
	if len(s.held) == 0 {
		return
	}
	if wait := s.stale - time.Since(s.last); wait > 0 {
		s.timer = time.AfterFunc(wait, s.expire)
		return
	}
	s.idle()
}

// idle marks the session idle and pushes the held events through the channel. It runs with mu
// held; a push only writes to the client and must not call back into the steering state.
func (s *Steering) idle() {
	s.busy = false
	if held := s.take(); len(held) > 0 {
		s.push(held)
	}
}
