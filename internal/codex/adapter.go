package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// Thread is the part of a Codex thread that the adapter uses.
type Thread struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Cwd    string `json:"cwd"`
	Status struct {
		Type string `json:"type"`
	} `json:"status"`
	ParentThreadID string `json:"parentThreadId"`
	AgentNickname  string `json:"agentNickname"`
	Ephemeral      bool   `json:"ephemeral"`
}

// Approver asks the owner whether a Codex thread may run something.
type Approver func(ctx context.Context, title, detail string) bool

// Options configure the adapter.
type Options struct {
	// Socket is the app-server daemon's control socket.
	Socket  string
	Version string
	Log     *slog.Logger
	Approve Approver
	// Poll is how often the adapter checks a busy thread before it starts a turn.
	Poll time.Duration
	// MaxWait is how long a push waits for a busy thread.
	MaxWait time.Duration
	// Tools are the WhatsAgent MCP tools. Threads that the adapter starts run them without Codex
	// asking first, because WhatsAgent checks each call itself.
	Tools []string
}

// Adapter is the Codex adapter.
type Adapter struct {
	opts Options

	mu        sync.Mutex
	conn      *Conn
	listeners map[string]func(method string, params json.RawMessage)
}

// New returns a Codex adapter. It connects on first use.
func New(opts Options) *Adapter {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Poll == 0 {
		opts.Poll = 2 * time.Second
	}
	if opts.MaxWait == 0 {
		opts.MaxWait = 30 * time.Minute
	}
	return &Adapter{opts: opts, listeners: map[string]func(string, json.RawMessage){}}
}

// connect returns the open connection, dialing again after it closed.
func (a *Adapter) connect(ctx context.Context) (*Conn, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn != nil {
		select {
		case <-a.conn.Done():
			a.conn = nil
		default:
			return a.conn, nil
		}
	}
	conn, err := Dial(ctx, a.opts.Socket, a.opts.Version, Handlers{Notify: a.notify, Request: a.request})
	if err != nil {
		return nil, err
	}
	a.conn = conn
	return conn, nil
}

// Close closes the daemon connection.
func (a *Adapter) Close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.conn != nil {
		a.conn.Close()
	}
}

func (a *Adapter) call(ctx context.Context, method string, params, result any) error {
	conn, err := a.connect(ctx)
	if err != nil {
		return err
	}
	return conn.Call(ctx, method, params, result)
}

// notify passes a thread's notifications to the listener for that thread.
func (a *Adapter) notify(method string, params json.RawMessage) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	if json.Unmarshal(params, &p) != nil || p.ThreadID == "" {
		return
	}
	a.mu.Lock()
	listen := a.listeners[p.ThreadID]
	a.mu.Unlock()
	if listen != nil {
		listen(method, params)
	}
}

func (a *Adapter) listen(threadID string, fn func(method string, params json.RawMessage)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if fn == nil {
		delete(a.listeners, threadID)
		return
	}
	a.listeners[threadID] = fn
}

// approvalMethods are the daemon requests that the adapter relays to the owner.
var approvalMethods = map[string]string{
	"item/commandExecution/requestApproval": "run a command",
	"item/fileChange/requestApproval":       "change files",
}

// request relays an approval request to the owner and answers with the decision.
func (a *Adapter) request(ctx context.Context, method string, params json.RawMessage) (any, error) {
	what, ok := approvalMethods[method]
	if !ok || a.opts.Approve == nil {
		return nil, fmt.Errorf("WhatsAgent does not handle %s", method)
	}
	decision := "decline"
	if a.opts.Approve(ctx, "Codex wants to "+what, describe(params)) {
		decision = "accept"
	}
	return map[string]string{"decision": decision}, nil
}

// describe shows the useful fields of an approval request: first the facts of the request, then
// the reason, which the agent writes itself and which is labeled as its own words.
func describe(params json.RawMessage) string {
	var p map[string]any
	if json.Unmarshal(params, &p) != nil {
		return string(params)
	}
	var parts []string
	for _, key := range []string{"command", "cwd", "grantRoot"} {
		if v, ok := p[key]; ok && v != nil {
			parts = append(parts, fmt.Sprintf("%s: %v", key, v))
		}
	}
	if reason, ok := p["reason"]; ok && reason != nil {
		parts = append(parts, fmt.Sprintf("The agent's own reason: %v", reason))
	}
	if len(parts) == 0 {
		return string(params)
	}
	return strings.Join(parts, "\n")
}

// Threads returns the threads that the daemon has loaded, without sub-agent and ephemeral ones.
func (a *Adapter) Threads(ctx context.Context) ([]Thread, error) {
	var loaded struct {
		Data []string `json:"data"`
	}
	if err := a.call(ctx, "thread/loaded/list", map[string]any{}, &loaded); err != nil {
		return nil, err
	}
	threads := make([]Thread, 0, len(loaded.Data))
	for _, id := range loaded.Data {
		t, err := a.Thread(ctx, id)
		if err != nil || t.ParentThreadID != "" || t.AgentNickname != "" || t.Ephemeral {
			continue
		}
		threads = append(threads, t)
	}
	return threads, nil
}

// Thread reads one thread.
func (a *Adapter) Thread(ctx context.Context, id string) (Thread, error) {
	var read struct {
		Thread Thread `json:"thread"`
	}
	err := a.call(ctx, "thread/read", map[string]any{"threadId": id}, &read)
	return read.Thread, err
}

// StartTurn starts a turn with text in a thread once the thread is idle. A turn that starts in
// the moment between the check and the call gets the text as a steer; Codex has no idle-only
// turn start yet.
func (a *Adapter) StartTurn(ctx context.Context, threadID, text string) error {
	if err := a.waitIdle(ctx, threadID); err != nil {
		return err
	}
	params := map[string]any{"threadId": threadID, "input": []map[string]any{{"type": "text", "text": text}}}
	return a.call(ctx, "turn/start", params, nil)
}

func (a *Adapter) waitIdle(ctx context.Context, threadID string) error {
	deadline := time.Now().Add(a.opts.MaxWait)
	for {
		t, err := a.Thread(ctx, threadID)
		if err != nil {
			return err
		}
		if t.Status.Type != "active" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("thread %s stayed busy for %s", threadID, a.opts.MaxWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.opts.Poll):
		}
	}
}

// SessionName is the WhatsAgent session name of a thread: its name, or its folder's base name
// with "-codex" when it has no name.
func SessionName(t Thread) string {
	if name := strings.Join(strings.Fields(t.Name), "-"); name != "" {
		return name
	}
	return filepath.Base(t.Cwd) + "-codex"
}

// Pusher pushes routed events into one Codex thread with turn/start.
type Pusher struct {
	adapter  *Adapter
	threadID string
	format   func([]model.Event) string
}

// NewPusher returns a pusher for a thread. format turns events into the turn text.
func (a *Adapter) NewPusher(threadID string, format func([]model.Event) string) *Pusher {
	return &Pusher{adapter: a, threadID: threadID, format: format}
}

// Push starts a turn with the events. A heartbeat checks that the thread is still loaded.
func (p *Pusher) Push(ctx context.Context, events []model.Event) error {
	if events == nil {
		_, err := p.adapter.Thread(ctx, p.threadID)
		return err
	}
	return p.adapter.StartTurn(ctx, p.threadID, p.format(events))
}

// Verdict is not used: Codex sends approval requests to the adapter itself.
func (p *Pusher) Verdict(context.Context, string, bool) error {
	return nil
}
