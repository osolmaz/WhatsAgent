package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/api"
	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/testkit"
)

// client drives an MCP server over pipes, as a harness does.
type client struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan map[string]any
	id    int
}

func run(t *testing.T, opts Options, socket string) *client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	opts.Retry = 50 * time.Millisecond
	opts.CheckInWait = 300 * time.Millisecond
	opts.Settle = settle
	server := New(opts, api.NewClient(socket), outW)
	done := make(chan struct{})
	go func() {
		_ = server.Run(ctx, inR)
		close(done)
	}()
	c := &client{t: t, in: inW, lines: make(chan map[string]any, 50)}
	go func() {
		scanner := bufio.NewScanner(outR)
		for scanner.Scan() {
			var msg map[string]any
			if json.Unmarshal(scanner.Bytes(), &msg) == nil {
				c.lines <- msg
			}
		}
	}()
	t.Cleanup(func() {
		inW.Close()
		cancel()
		<-done
		outW.Close()
	})
	return c
}

func (c *client) send(line string) {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatal(err)
	}
}

// call sends a request and returns its response.
func (c *client) call(method string, params any) map[string]any {
	c.t.Helper()
	c.id++
	data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	c.send(string(data))
	return c.wait(func(msg map[string]any) bool { return msg["id"] == float64(c.id) })
}

func (c *client) wait(match func(map[string]any) bool) map[string]any {
	c.t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case msg := <-c.lines:
			if match(msg) {
				return msg
			}
		case <-timeout:
			c.t.Fatal("no matching message from the server")
		}
	}
}

func (c *client) notification(method string) map[string]any {
	c.t.Helper()
	return c.wait(func(msg map[string]any) bool { return msg["method"] == method })
}

func resultText(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	result, _ := resp["result"].(map[string]any)
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("no content in %v", resp)
	}
	first, _ := content[0].(map[string]any)
	isErr, _ := result["isError"].(bool)
	return fmt.Sprint(first["text"]), isErr
}

func TestChannelSession(t *testing.T) {
	socket := filepath.Join(testkit.ShortDir(t), "d.sock")
	d := testkit.Start(t, socket)
	d.Core.OnChatName("g@g.us", "Team")
	d.Phone("/route Team dev", "now goes to dev")
	d.Keep("g@g.us")
	c := run(t, Options{Name: "dev", Harness: "claude", Push: true, Version: "test", Location: time.UTC}, socket)

	init := c.call("initialize", map[string]any{"protocolVersion": "2026-07-28"})
	result, _ := init["result"].(map[string]any)
	if result["protocolVersion"] != protocolVersions[0] {
		t.Errorf("negotiated %v", result["protocolVersion"])
	}
	caps, _ := result["capabilities"].(map[string]any)
	if _, ok := caps["experimental"].(map[string]any)["claude/channel"]; !ok {
		t.Errorf("capabilities = %v", caps)
	}
	if !strings.Contains(fmt.Sprint(result["instructions"]), "<channel source=") {
		t.Error("push instructions missing")
	}
	if old := c.call("initialize", map[string]any{"protocolVersion": "2025-06-18"}); old["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Errorf("older revision not kept: %v", old)
	}
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if tools := c.call("tools/list", nil); len(tools["result"].(map[string]any)["tools"].([]any)) != len(toolList) {
		t.Errorf("tools/list = %v", tools)
	}
	if pong := c.call("ping", nil); pong["result"] == nil {
		t.Errorf("ping = %v", pong)
	}
	if bad := c.call("resources/list", nil); bad["error"] == nil {
		t.Errorf("unknown method = %v", bad)
	}
	c.send("not json")
	c.wait(func(msg map[string]any) bool { return msg["error"] != nil && msg["id"] == nil })

	time.Sleep(100 * time.Millisecond)
	look := d.Receive("g@g.us", "please look", false)
	note := c.notification("notifications/claude/channel")
	params, _ := note["params"].(map[string]any)
	meta, _ := params["meta"].(map[string]any)
	if params["content"] != "[Team] Sam: please look" || meta["chat_id"] != "g@g.us" || meta["trust"] != "other" || meta["chat_name"] != "Team" {
		t.Errorf("channel notification = %v", note)
	}
	for e, want := range map[*model.Event]string{
		{ChatName: "Sam", Message: model.Message{SenderName: "Sam"}}:                      "Sam: ",
		{Message: model.Message{Sender: "15551234567@s.whatsapp.net"}}:                    "15551234567: ",
		{ChatName: "Team", Message: model.Message{Sender: "200@lid", SenderName: "Maya"}}: "[Team] Maya: ",
		{ChatName: "Sam Lee", Message: model.Message{Chat: "1@s.whatsapp.net", Sender: "1@s.whatsapp.net",
			SenderName: "Sam", FromMe: true}}: "[self-chat] Sam: ",
	} {
		if got := speaker(*e); got != want {
			t.Errorf("speaker(%+v) = %q, want %q", *e, got, want)
		}
	}

	text, isErr := resultText(t, c.call("tools/call", map[string]any{"name": "read_chat", "arguments": map[string]any{"chat": "Team"}}))
	if isErr || !strings.Contains(text, "Team (g@g.us)") || !strings.Contains(text, "Sam: please look") {
		t.Errorf("read_chat = %q", text)
	}
	if text, _ := resultText(t, c.call("tools/call", map[string]any{"name": "search", "arguments": map[string]any{"query": "look"}})); !strings.Contains(text, "1 messages") {
		t.Errorf("search = %q", text)
	}
	if text, _ := resultText(t, c.call("tools/call", map[string]any{"name": "send", "arguments": map[string]any{"chat": "Team", "text": "on it"}})); !strings.HasPrefix(text, "sent") {
		t.Errorf("send = %q", text)
	}
	d.Expect("on it")
	// The server reads the file that the agent names, in the agent's working folder.
	t.Chdir(t.TempDir())
	if err := os.WriteFile("plan.pdf", []byte("%PDF"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"chat": "Team", "text": "the plan", "file": "plan.pdf", "reply_to": look.ID}
	if text, _ := resultText(t, c.call("tools/call", map[string]any{"name": "send", "arguments": args})); !strings.HasPrefix(text, "sent") {
		t.Errorf("send with a file = %q", text)
	}
	if s := d.Expect("the plan"); s.File != "plan.pdf" || s.Out.ReplyTo == nil {
		t.Errorf("sent %+v", s)
	}
	args["file"] = "missing.pdf"
	if _, isErr := resultText(t, c.call("tools/call", map[string]any{"name": "send", "arguments": args})); !isErr {
		t.Error("a missing file gave no error")
	}
	if _, isErr := resultText(t, c.call("tools/call", map[string]any{"name": "send", "arguments": "bad"})); !isErr {
		t.Error("bad send arguments gave no error")
	}
	if text, _ := resultText(t, c.call("tools/call", map[string]any{"name": "list_sessions"})); !strings.Contains(text, `"name":"dev"`) {
		t.Errorf("list_sessions = %q", text)
	}
	if _, isErr := resultText(t, c.call("tools/call", map[string]any{"name": "export", "arguments": map[string]any{"chat": "Team"}})); !isErr {
		t.Error("a session could export")
	}
	if _, isErr := resultText(t, c.call("tools/call", "bad")); !isErr {
		t.Error("bad tools/call params gave no error")
	}

	c.send(`{"jsonrpc":"2.0","method":"notifications/claude/channel/permission_request","params":{"request_id":"qwert","tool_name":"Bash","description":"list files","input_preview":"{\"command\":\"ls\"}"}}`)
	prompt := d.Expect("session dev wants to use the tool Bash with this input:\n{\"command\":\"ls\"}\nThe agent's own description: list files")
	_, rest, _ := strings.Cut(prompt.Text, `"no `)
	code, _, _ := strings.Cut(rest, `"`)
	d.Receive(testkit.SelfChat, "no "+code, true)
	verdict := c.notification("notifications/claude/channel/permission")
	if p := verdict["params"].(map[string]any); p["request_id"] != "qwert" || p["behavior"] != "deny" {
		t.Errorf("verdict = %v", verdict)
	}
	c.send(`{"jsonrpc":"2.0","method":"notifications/claude/channel/permission_request","params":"bad"}`)
}

const settle = 200 * time.Millisecond

// TestChannelHoldsPushesUntilTheClientIsReady checks a fresh agent's first message: it arrives
// while the client still connects, and the server pushes it only Settle after the client's
// initialized notification, when Claude Code has registered its channel handler.
func TestChannelHoldsPushesUntilTheClientIsReady(t *testing.T) {
	socket := filepath.Join(testkit.ShortDir(t), "d.sock")
	d := testkit.Start(t, socket)
	d.Core.OnChatName("g@g.us", "Team")
	d.Phone("/route Team dev", "now goes to dev")
	d.Keep("g@g.us")
	c := run(t, Options{Name: "dev", Harness: "claude", Push: true, Location: time.UTC}, socket)
	for deadline := time.Now().Add(5 * time.Second); !pushing(t, d, "dev"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the server did not check in")
		}
	}
	d.Receive("g@g.us", "first message", false)
	select {
	case msg := <-c.lines:
		t.Fatalf("the server wrote before the handshake: %v", msg)
	case <-time.After(300 * time.Millisecond):
	}
	c.call("initialize", map[string]any{"protocolVersion": protocolVersions[0]})
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	initialized := time.Now()
	note := c.notification("notifications/claude/channel")
	if waited := time.Since(initialized); waited < settle-20*time.Millisecond {
		t.Errorf("the push came %s after the initialized notification, before the client was ready", waited)
	}
	if content := note["params"].(map[string]any)["content"]; content != "[Team] Sam: first message" {
		t.Errorf("channel notification = %v", note)
	}
}

// pushing reports whether the session takes channel pushes.
func pushing(t *testing.T, d *testkit.Daemon, name string) bool {
	t.Helper()
	sessions, err := d.Core.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range sessions {
		if s.Name == name && s.Push == "channel" {
			return true
		}
	}
	return false
}

// TestClaimedHomeReadsOnlyKeptChats is what `WHATSAGENT_SESSION=home whatsagent channel` gets: an
// MCP server that only claims home's name reads kept chats only, although home sees every chat.
func TestClaimedHomeReadsOnlyKeptChats(t *testing.T) {
	socket := filepath.Join(testkit.ShortDir(t), "d.sock")
	d := testkit.Start(t, socket)
	d.Core.OnChatName("o@g.us", "Other")
	d.Core.OnChatName("k@g.us", "Kept")
	d.Keep("k@g.us")
	d.Receive("o@g.us", "OTHER-SECRET", false)
	d.Receive("k@g.us", "kept text", false)
	c := run(t, Options{Name: "home", Harness: "claude", Version: "test", Location: time.UTC}, socket)
	c.call("initialize", map[string]any{})
	text, isErr := resultText(t, c.call("tools/call", map[string]any{"name": "read_chat", "arguments": map[string]any{"chat": "Other"}}))
	if !isErr || strings.Contains(text, "OTHER-SECRET") || !strings.Contains(text, "does not keep") {
		t.Errorf("claimed home's read of a chat that is not kept = %q, error %v", text, isErr)
	}
	text, _ = resultText(t, c.call("tools/call", map[string]any{"name": "search", "arguments": map[string]any{"query": "e"}}))
	if strings.Contains(text, "OTHER-SECRET") || !strings.Contains(text, "kept text") {
		t.Errorf("claimed home's search = %q", text)
	}
}

func TestToolsOnlyWithoutDaemon(t *testing.T) {
	c := run(t, Options{Harness: "codex"}, filepath.Join(testkit.ShortDir(t), "none.sock"))
	init := c.call("initialize", map[string]any{})
	caps := init["result"].(map[string]any)["capabilities"].(map[string]any)
	if _, ok := caps["experimental"]; ok {
		t.Error("a tools-only session declared the channel")
	}
	text, isErr := resultText(t, c.call("tools/call", map[string]any{"name": "status"}))
	if !isErr || !strings.Contains(text, "not running") {
		t.Errorf("status without a daemon = %q", text)
	}
}

// TestToolNames checks the exported tool names, and that exactly the tools that change nothing
// carry the read-only mark, because a harness such as Codex runs those without asking.
func TestToolNames(t *testing.T) {
	names := ToolNames()
	if len(names) != len(toolList) || names[0] != "read_chat" {
		t.Fatalf("ToolNames = %v", names)
	}
	var marked []string
	for _, tool := range toolList {
		if tool["annotations"] != nil {
			marked = append(marked, tool["name"].(string))
		}
	}
	if got := strings.Join(marked, ","); got != "read_chat,search,list_sessions,list_chats" {
		t.Errorf("read-only tools = %s", got)
	}
}

func TestRender(t *testing.T) {
	s := New(Options{}, nil, io.Discard)
	if got := s.render("send", json.RawMessage(`{"text":"sent"}`)); got != "sent" {
		t.Errorf("render text = %q", got)
	}
	if got := s.render("status", json.RawMessage(`{"whatsapp":"connected"}`)); got != `{"whatsapp":"connected"}` {
		t.Errorf("render other = %q", got)
	}
	if got := s.render("read_chat", json.RawMessage(`[1]`)); got != "[1]" {
		t.Errorf("render bad read_chat = %q", got)
	}
}
