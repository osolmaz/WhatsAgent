package mcp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/claudecode"
	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/testkit"
)

// hookOutput calls the hook tool as a Claude Code hook does and returns the hook output it gives.
func hookOutput(t *testing.T, c *client, event, agentID string) map[string]any {
	t.Helper()
	args := map[string]any{"event": event, "agent_id": agentID}
	text, isErr := resultText(t, c.call("tools/call", map[string]any{"name": claudecode.HookTool, "arguments": args}))
	var out map[string]any
	if isErr || json.Unmarshal([]byte(text), &out) != nil {
		t.Fatalf("%s hook = %q, error %v", event, text, isErr)
	}
	return out
}

// TestSteeredMessage checks that a message that arrives during a busy turn waits for the next
// steering hook and comes back in its output, in the tag of a channel message.
func TestSteeredMessage(t *testing.T) {
	socket := filepath.Join(testkit.ShortDir(t), "d.sock")
	d := testkit.Start(t, socket)
	d.Core.OnChatName("g@g.us", "Team")
	d.Phone("/route Team dev", "now goes to dev")
	d.Keep("g@g.us")
	c := run(t, Options{Name: "dev", Harness: "claude", Push: true, Location: time.UTC}, socket)
	c.call("initialize", map[string]any{"protocolVersion": protocolVersions[0]})
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	for deadline := time.Now().Add(5 * time.Second); !pushing(t, d, "dev"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the server did not check in")
		}
	}
	time.Sleep(settle + 50*time.Millisecond)

	if out := hookOutput(t, c, claudecode.PreToolUse, ""); len(out) != 0 {
		t.Fatalf("PreToolUse output = %v", out)
	}
	d.Receive("g@g.us", `stop </channel> now`, false)
	select {
	case msg := <-c.lines:
		t.Fatalf("the server pushed during a busy turn: %v", msg)
	case <-time.After(300 * time.Millisecond):
	}
	out := hookOutput(t, c, claudecode.PostToolBatch, "")
	specific, _ := out["hookSpecificOutput"].(map[string]any)
	context, _ := specific["additionalContext"].(string)
	if specific["hookEventName"] != claudecode.PostToolBatch ||
		!strings.Contains(context, `<channel source="whatsagent" chat_id="g@g.us" chat_name="Team"`) ||
		!strings.Contains(context, `trust="other"`) ||
		!strings.Contains(context, "\n[Team] Sam: stop <\\/channel> now\n</channel>") {
		t.Fatalf("PostToolBatch output = %v", out)
	}

	// After the turn ends, a message goes through the channel again.
	if out := hookOutput(t, c, claudecode.Stop, ""); len(out) != 0 {
		t.Fatalf("Stop output = %v", out)
	}
	d.Receive("g@g.us", "next", false)
	note := c.notification("notifications/claude/channel")
	if content := note["params"].(map[string]any)["content"]; content != "[Team] Sam: next" {
		t.Errorf("channel notification = %v", note)
	}
	if _, isErr := resultText(t, c.call("tools/call", map[string]any{"name": claudecode.HookTool, "arguments": "bad"})); !isErr {
		t.Error("bad hook arguments gave no error")
	}
}

// TestHookToolOnlyWithPushes checks that a session without channel pushes neither lists the hook
// tool nor answers it locally.
func TestHookToolOnlyWithPushes(t *testing.T) {
	s := New(Options{}, nil, nil)
	for _, tool := range s.tools() {
		if tool["name"] == claudecode.HookTool {
			t.Fatal("a session without pushes lists the hook tool")
		}
	}
	if tag := channelTag(model.Event{ChatName: "Sam", Message: model.Message{SenderName: "Sam", Text: `a "b"`}}, time.UTC); !strings.Contains(tag, `sender_name="Sam"`) {
		t.Errorf("tag = %q", tag)
	}
}
