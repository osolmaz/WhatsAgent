package codex

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/model"
)

// Home runs home as a thread on the Codex app-server daemon, so the owner can watch it live by
// attaching a TUI with `codex resume --remote unix://`.
type Home struct {
	adapter  *Adapter
	host     core.AgentHost
	threadID string
	format   func([]model.Event) string

	mu   sync.Mutex
	last string
}

// NewHome continues home's stored thread, or starts a new one in dir with instructions.
func (a *Adapter) NewHome(ctx context.Context, host core.AgentHost, dir, instructions string,
	format func([]model.Event) string) (*Home, error) {
	h := &Home{adapter: a, host: host, format: format}
	var started struct {
		Thread Thread `json:"thread"`
	}
	var err error
	if host.SessionID != "" {
		err = a.call(ctx, "thread/resume", map[string]any{"threadId": host.SessionID}, &started)
	}
	if host.SessionID == "" || err != nil {
		params := map[string]any{"cwd": dir, "developerInstructions": instructions}
		if err = a.call(ctx, "thread/start", params, &started); err != nil {
			return nil, err
		}
	}
	h.threadID = started.Thread.ID
	if err := host.SaveSessionID(h.threadID); err != nil {
		return nil, err
	}
	a.listen(h.threadID, h.onNote)
	go func() {
		<-ctx.Done()
		a.listen(h.threadID, nil)
	}()
	return h, nil
}

// ThreadID returns the ID of home's thread.
func (h *Home) ThreadID() string {
	return h.threadID
}

// Push starts a turn in home's thread with the events.
func (h *Home) Push(ctx context.Context, events []model.Event) error {
	if events == nil {
		return nil
	}
	return h.adapter.StartTurn(ctx, h.threadID, h.format(events))
}

// Verdict is not used: Codex sends approval requests to the adapter itself.
func (h *Home) Verdict(context.Context, string, bool) error {
	return nil
}

// onNote keeps the last agent message of a turn and posts it when the turn completes.
func (h *Home) onNote(method string, params json.RawMessage) {
	switch method {
	case "item/completed":
		var p struct {
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
		}
		if json.Unmarshal(params, &p) == nil && p.Item.Type == "agentMessage" {
			h.mu.Lock()
			h.last = p.Item.Text
			h.mu.Unlock()
		}
	case "turn/completed":
		h.mu.Lock()
		text := h.last
		h.last = ""
		h.mu.Unlock()
		h.host.Post(text)
	}
}
