package home

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/model"
)

// TestMain lets the test binary stand in for claude: with FAKE_CLAUDE set, it answers each
// stream-json user message with a result line that echoes it.
func TestMain(m *testing.M) {
	switch os.Getenv("FAKE_CLAUDE") {
	case "echo":
		fakeClaude()
		return
	case "noresume":
		// Like claude, a resume of an unknown session prints an error result and exits.
		if slices.Contains(os.Args, "--resume") {
			fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"",` +
				`"errors":["No conversation found with session ID: old-session"]}`)
			os.Exit(1)
		}
		fakeClaude()
		return
	}
	os.Exit(m.Run())
}

func fakeClaude() {
	if path := os.Getenv("FAKE_CLAUDE_ARGS"); path != "" {
		args := append(os.Args[1:], "WHATSAGENT_SESSION="+os.Getenv("WHATSAGENT_SESSION"))
		_ = os.WriteFile(path, []byte(strings.Join(args, "\n")), 0o600)
	}
	fmt.Println(`{"type":"system","subtype":"init","session_id":"s"}`)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var line struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &line)
		result, _ := json.Marshal(map[string]any{"type": "result", "result": "echo: " + line.Message.Content,
			"is_error": strings.Contains(line.Message.Content, "fail")})
		fmt.Println("not json")
		fmt.Println(string(result))
	}
}

type host struct {
	mu    sync.Mutex
	posts chan string
	id    string
}

func newHost(id string) *host { return &host{posts: make(chan string, 10), id: id} }

func (h *host) homeHost() core.AgentHost {
	return core.AgentHost{
		Post:      func(text string) { h.posts <- text },
		Fail:      func(text string) { h.posts <- text },
		SessionID: h.id,
		SaveSessionID: func(id string) error {
			h.mu.Lock()
			h.id = id
			h.mu.Unlock()
			return nil
		},
	}
}

func (h *host) saved() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.id
}

func (h *host) expect(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-h.posts:
		if !strings.Contains(got, want) {
			t.Fatalf("posted %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing posted, want %q", want)
	}
}

const selfChat = "100@s.whatsapp.net"

func event(chat, text string, fromMe bool) model.Event {
	role := model.RoleOther
	if fromMe {
		role = model.RoleOwner
	}
	return model.Event{ChatName: "Team", Message: model.Message{Chat: chat, ID: "M1", Sender: "200@s.whatsapp.net",
		SenderName: "Sam", Time: time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC), FromMe: fromMe, Role: role, Text: text}}
}

func TestPrompt(t *testing.T) {
	got := Prompt([]model.Event{event(selfChat, "hi home", true), event("g@g.us", "hello", false)}, selfChat, time.UTC)
	for _, want := range []string{"The owner wrote in the self-chat: hi home", `in "Team" (chat_id g@g.us) from Sam (200@s.whatsapp.net), trust=other`} {
		if !strings.Contains(got, want) {
			t.Errorf("Prompt() = %q, missing %q", got, want)
		}
	}
	if senderOf(model.Message{FromMe: true}) != "the owner" || senderOf(model.Message{Sender: "x"}) != "x" {
		t.Error("senderOf is wrong")
	}
}

func TestClaudeHome(t *testing.T) {
	bin, _ := os.Executable()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_CLAUDE", "echo")
	t.Setenv("FAKE_CLAUDE_ARGS", argsFile)
	t.Setenv("CLAUDECODE", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHost("")
	home, err := NewClaude(ctx, ClaudeOptions{Bin: bin, Dir: filepath.Join(t.TempDir(), "home"), SelfChat: selfChat,
		Location: time.UTC, Args: []string{"--model", "claude-opus-5-5"}}, h.homeHost())
	if err != nil {
		t.Fatal(err)
	}
	if h.saved() != "" {
		t.Fatal("home saved the ID of a session that has no conversation yet")
	}
	if err := home.Push(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := home.Push(ctx, []model.Event{event(selfChat, "hello home", true)}); err != nil {
		t.Fatal(err)
	}
	h.expect(t, "echo: [2026-11-01 01:00] The owner wrote in the self-chat: hello home")
	if h.saved() == "" {
		t.Fatal("home saved no session ID after its conversation started")
	}
	_ = home.Push(ctx, []model.Event{event(selfChat, "please fail", true)})
	h.expect(t, "home failed: echo:")
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--session-id\n" + h.saved(), "--permission-prompt-tool\nmcp__whatsagent__approve",
		"--allowedTools\nmcp__whatsagent", "--model\nclaude-opus-5-5", "WHATSAGENT_SESSION=home"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	if strings.Contains(string(args), "--tools") || strings.Contains(string(args), "--strict-mcp-config") ||
		strings.Contains(string(args), "--permission-mode") {
		t.Errorf("home's tools were limited: %q", args)
	}
	if err := home.Verdict(ctx, "x", true); err != nil {
		t.Error(err)
	}
	if env := cleanEnv([]string{"CLAUDECODE=1", "PATH=/bin", "CLAUDE_CODE_SESSION_ID=x"}); len(env) != 1 || env[0] != "PATH=/bin" {
		t.Errorf("cleanEnv = %v", env)
	}
	if _, err := (logWriter{home.opts.Log}).Write([]byte("stderr line\n")); err != nil {
		t.Error(err)
	}
}

func TestClaudeHomeResumesAndRecovers(t *testing.T) {
	bin, _ := os.Executable()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_CLAUDE_ARGS", argsFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A session that Claude Code cannot resume makes home start a fresh one and send the same
	// message again, without an error for the owner.
	t.Setenv("FAKE_CLAUDE", "noresume")
	h := newHost("old-session")
	home, err := NewClaude(ctx, ClaudeOptions{Bin: bin, Dir: t.TempDir(), SelfChat: selfChat}, h.homeHost())
	if err != nil {
		t.Fatal(err)
	}
	_ = home.Push(ctx, []model.Event{event(selfChat, "one", true)})
	if got := <-h.posts; !strings.Contains(got, "echo:") || !strings.HasSuffix(got, "one") {
		t.Fatalf("after a failed resume home posted %q", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.saved() == "old-session" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if h.saved() == "old-session" || h.saved() == "" {
		t.Fatalf("home kept session %q", h.saved())
	}

	t.Setenv("FAKE_CLAUDE", "echo")
	resumed := newHost("kept-session")
	home, _ = NewClaude(ctx, ClaudeOptions{Bin: bin, Dir: t.TempDir(), SelfChat: selfChat}, resumed.homeHost())
	_ = home.Push(ctx, []model.Event{event(selfChat, "two", true)})
	resumed.expect(t, "two")
	if args, _ := os.ReadFile(argsFile); !strings.Contains(string(args), "--resume\nkept-session") {
		t.Errorf("args = %q", args)
	}

	if _, err := NewClaude(ctx, ClaudeOptions{Bin: "/no/such/claude", Dir: "/proc/no/such/dir"}, newHost("").homeHost()); err == nil {
		t.Error("NewClaude in an unwritable folder gave no error")
	}
	broken, _ := NewClaude(ctx, ClaudeOptions{Bin: "/no/such/claude", Dir: t.TempDir()}, newHost("x").homeHost())
	if err := broken.Push(ctx, []model.Event{event(selfChat, "x", true)}); err == nil {
		t.Error("a missing claude binary gave no error")
	}
}

func TestClaudeChatAgent(t *testing.T) {
	bin, _ := os.Executable()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_CLAUDE", "echo")
	t.Setenv("FAKE_CLAUDE_ARGS", argsFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHost("")
	host := h.homeHost()
	host.Session, host.Auto = "chat-team", true
	instructions := ChatInstructions("Bob", "Team", "g@g.us")
	agent, err := NewClaude(ctx, ClaudeOptions{Bin: bin, Dir: t.TempDir(), SelfChat: selfChat, Instructions: instructions,
		Tools: []string{"WebSearch", "WebFetch"}, MCPConfig: `{"mcpServers":{}}`}, host)
	if err != nil {
		t.Fatal(err)
	}
	_ = agent.Push(ctx, []model.Event{event("g@g.us", "please fail", false)})
	h.expect(t, "chat-team failed: echo:")
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--append-system-prompt\n" + instructions, "--tools\nWebSearch,WebFetch",
		"--strict-mcp-config\n--mcp-config\n{\"mcpServers\":{}}", "WHATSAGENT_SESSION=chat-team", "--permission-mode\nauto"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	if !strings.Contains(instructions, `You are Bob`) || !strings.Contains(instructions, `"Team" (chat_id g@g.us)`) {
		t.Errorf("ChatInstructions = %q", instructions)
	}
}
