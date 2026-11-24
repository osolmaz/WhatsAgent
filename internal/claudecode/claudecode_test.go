package claudecode

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// channel records the events that the steering state pushes through the channel.
type channel struct {
	mu     sync.Mutex
	pushed []string
}

func (c *channel) push(events []model.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range events {
		c.pushed = append(c.pushed, e.Message.ID)
	}
}

func (c *channel) got() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.pushed...)
}

func event(id string) []model.Event {
	return []model.Event{{Message: model.Message{ID: id}}}
}

func ids(events []model.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Message.ID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestHookSettings(t *testing.T) {
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type, Server, Tool string
				Input              map[string]string
			}
		}
	}
	if err := json.Unmarshal([]byte(HookSettings("whatsagent")), &settings); err != nil {
		t.Fatal(err)
	}
	if len(settings.Hooks) != len(hookEvents) {
		t.Fatalf("hooks = %v", settings.Hooks)
	}
	for _, name := range hookEvents {
		groups := settings.Hooks[name]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			t.Fatalf("%s = %v", name, groups)
		}
		h := groups[0].Hooks[0]
		if h.Type != "mcp_tool" || h.Server != "whatsagent" || h.Tool != HookTool ||
			h.Input["event"] != "${hook_event_name}" || h.Input["agent_id"] != "${agent_id}" {
			t.Errorf("%s hook = %+v", name, h)
		}
	}
}

func TestSteering(t *testing.T) {
	c := &channel{}
	s := NewSteering(c.push, time.Hour)

	// An idle session gets its events through the channel at once.
	s.Deliver(event("a"))
	if !equal(c.got(), []string{"a"}) {
		t.Fatalf("idle push = %v", c.got())
	}

	// A busy session holds them until a hook that can add context.
	s.Hook(PreToolUse, "")
	s.Deliver(event("b"))
	s.Deliver(event("c"))
	if got := s.Hook(PreToolUse, ""); got != nil || len(c.got()) != 1 {
		t.Fatalf("PreToolUse took %v, pushed %v", got, c.got())
	}
	if got := ids(s.Hook(PostToolBatch, "")); !equal(got, []string{"b", "c"}) {
		t.Fatalf("PostToolBatch = %v", got)
	}
	if got := s.Hook(PostToolBatch, ""); got != nil {
		t.Fatalf("second PostToolBatch = %v", got)
	}

	// A subagent's hooks change nothing and take nothing.
	s.Deliver(event("d"))
	if got := s.Hook(PostToolBatch, "agent-1"); got != nil {
		t.Fatalf("subagent hook took %v", got)
	}

	// Stop passes the held events on and keeps the session busy, since the turn goes on.
	if got := ids(s.Hook(Stop, "")); !equal(got, []string{"d"}) {
		t.Fatalf("Stop = %v", got)
	}
	s.Deliver(event("e"))
	if len(c.got()) != 1 {
		t.Fatalf("pushed during the continued turn: %v", c.got())
	}
	if got := ids(s.Hook(UserPromptSubmit, "")); !equal(got, []string{"e"}) {
		t.Fatalf("UserPromptSubmit = %v", got)
	}

	// A Stop with nothing held ends the turn: the session is idle again.
	if got := s.Hook(Stop, ""); got != nil {
		t.Fatalf("empty Stop = %v", got)
	}
	s.Deliver(event("f"))
	if !equal(c.got(), []string{"a", "f"}) {
		t.Fatalf("push after Stop = %v", c.got())
	}

	// A turn that ends with an API error pushes what is held through the channel.
	s.Hook(PreToolUse, "")
	s.Deliver(event("g"))
	s.Hook(StopFailure, "")
	if !equal(c.got(), []string{"a", "f", "g"}) {
		t.Fatalf("push after StopFailure = %v", c.got())
	}
	s.Deliver(event("h"))
	if !equal(c.got(), []string{"a", "f", "g", "h"}) {
		t.Fatalf("idle after StopFailure = %v", c.got())
	}
}

// TestSteeringFallback checks that held events go through the channel when no hook comes, as after
// an interrupted turn, and that a recent hook postpones that.
func TestSteeringFallback(t *testing.T) {
	c := &channel{}
	stale := 300 * time.Millisecond
	s := NewSteering(c.push, stale)
	s.Hook(PreToolUse, "")
	s.Deliver(event("a"))
	time.Sleep(stale / 2)
	s.Hook(PreToolUse, "")
	time.Sleep(stale * 3 / 4)
	if len(c.got()) != 0 {
		t.Fatalf("pushed although a hook came: %v", c.got())
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(c.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !equal(c.got(), []string{"a"}) {
		t.Fatalf("fallback push = %v", c.got())
	}
	s.Deliver(event("b"))
	if !equal(c.got(), []string{"a", "b"}) {
		t.Fatalf("not idle after the fallback: %v", c.got())
	}

	// A timer that finds nothing held leaves the state alone.
	s.Hook(PreToolUse, "")
	s.Deliver(event("c"))
	if got := ids(s.Hook(PostToolBatch, "")); !equal(got, []string{"c"}) {
		t.Fatalf("PostToolBatch = %v", got)
	}
	time.Sleep(stale * 2)
	s.Deliver(event("d"))
	if len(c.got()) != 2 {
		t.Fatalf("busy session pushed after an empty timer: %v", c.got())
	}
}
