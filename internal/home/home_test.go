package home

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/term"
)

// TestMain lets the test binary stand in for claude: with FAKE_CLAUDE set, it records its
// arguments, asks Claude Code's two startup questions, says READY, and ends at the line "quit".
func TestMain(m *testing.M) {
	if os.Getenv("FAKE_CLAUDE") == "interactive" {
		fakeClaude()
		return
	}
	os.Exit(m.Run())
}

func fakeClaude() {
	args := append(os.Args[1:], "WHATSAGENT_SESSION="+os.Getenv("WHATSAGENT_SESSION"), "TERM="+os.Getenv("TERM"))
	f, _ := os.OpenFile(os.Getenv("FAKE_CLAUDE_ARGS"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	fmt.Fprintln(f, strings.Join(args, "\n")+"\n--")
	f.Close()
	in := bufio.NewReader(os.Stdin)
	for _, question := range []string{"\x1b[1mI am\x1b[0m using this for local\x1b[1Cdevelopment", "Yes, I trust this folder"} {
		fmt.Println(question)
		// The terminal is in cooked mode here, so the Enter key's \r arrives as \n.
		if b, err := in.ReadByte(); err != nil || (b != '\r' && b != '\n') {
			fmt.Println("bad answer")
			os.Exit(1)
		}
	}
	fmt.Println("READY")
	for {
		line, err := in.ReadString('\n')
		if err != nil || strings.TrimSpace(line) == "quit" {
			return
		}
		fmt.Println("got " + strings.TrimSpace(line))
	}
}

type host struct {
	mu sync.Mutex
	id string
}

func (h *host) agentHost(session string) core.AgentHost {
	return core.AgentHost{Session: session, SessionID: h.saved(), Auto: true, SaveSessionID: func(id string) error {
		h.mu.Lock()
		h.id = id
		h.mu.Unlock()
		return nil
	}}
}

func (h *host) saved() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.id
}

// output collects what a session prints.
type output struct {
	mu   sync.Mutex
	text strings.Builder
}

func (o *output) viewer() term.Viewer {
	return term.Viewer{Data: func(p []byte) {
		o.mu.Lock()
		o.text.Write(p)
		o.mu.Unlock()
	}, Exit: func() {}}
}

func (o *output) wait(t *testing.T, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		o.mu.Lock()
		done := strings.Contains(o.text.String(), want)
		o.mu.Unlock()
		if done {
			return
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	t.Fatalf("no %q in %q", want, o.text.String())
}

// attach waits for the session to run and attaches out to it.
func attach(t *testing.T, terminals *term.Host, name string, out *output) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if _, ok := terminals.Attach(name, 80, 24, out.viewer()); ok {
			return
		}
	}
	t.Fatalf("session %s did not start", name)
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
	if !strings.Contains(ChatInstructions("Bob", "Team", "g@g.us"), `You are Bob, the WhatsApp agent of the owner, in the chat "Team" (chat_id g@g.us)`) {
		t.Error("ChatInstructions is wrong")
	}
}

func TestStartClaudeInTerminal(t *testing.T) {
	bin, _ := os.Executable()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv("FAKE_CLAUDE", "interactive")
	t.Setenv("FAKE_CLAUDE_ARGS", argsFile)
	t.Setenv("CLAUDECODE", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	terminals, h := term.NewHost(), &host{}
	opts := ClaudeOptions{Terminals: terminals, Bin: bin, Dir: filepath.Join(t.TempDir(), "chat-team"), Title: "Team",
		Instructions: "be Bob", Tools: []string{"WebSearch"}, MCPConfig: `{"mcpServers":{}}`, ConfigDir: t.TempDir(),
		Args: []string{"--model", "claude-opus-5-5"}, Restart: 10 * time.Millisecond}
	pusher, err := StartClaude(ctx, opts, h.agentHost("chat-team"))
	if err != nil || pusher != nil {
		t.Fatalf("StartClaude = %v, %v", pusher, err)
	}
	out := &output{}
	attach(t, terminals, "chat-team", out)
	out.wait(t, "READY")
	terminals.Input("chat-team", "hello\r")
	out.wait(t, "got hello")
	first := h.saved()
	args, _ := os.ReadFile(argsFile)
	for _, want := range []string{"--dangerously-load-development-channels\nserver:whatsagent", "--append-system-prompt\nbe Bob",
		"--allowedTools\nmcp__whatsagent", "--strict-mcp-config\n--mcp-config\n{\"mcpServers\":{}}", "--permission-mode\nauto",
		"--tools\nWebSearch", "--session-id\n" + first, "--model\nclaude-opus-5-5", "WHATSAGENT_SESSION=chat-team",
		"TERM=xterm-256color"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	if strings.Contains(string(args), "CLAUDECODE") {
		t.Error("the agent inherited a session marker")
	}

	// Once Claude Code keeps a transcript, the agent's next process resumes that session.
	transcript := filepath.Join(opts.ConfigDir, "projects", nonAlphanumeric.ReplaceAllString(opts.Dir, "-"), first+".jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	terminals.Input("chat-team", "quit\r")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if args, _ := os.ReadFile(argsFile); strings.Contains(string(args), "--resume\n"+first) {
			break
		}
	}
	if args, _ := os.ReadFile(argsFile); !strings.Contains(string(args), "--resume\n"+first) || h.saved() != first {
		t.Errorf("the restarted agent did not resume %s: %q", first, args)
	}
	cancel()
	for deadline := time.Now().Add(5 * time.Second); terminals.Running("chat-team") && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if terminals.Running("chat-team") {
		t.Error("the agent outlived its context")
	}
}

func TestStartClaudeFailures(t *testing.T) {
	h := &host{}
	if _, err := StartClaude(context.Background(), ClaudeOptions{Terminals: term.NewHost(), Bin: "/no/such/claude",
		Dir: t.TempDir()}, h.agentHost("x")); err == nil {
		t.Error("a missing claude gave no error")
	}
	if _, err := StartClaude(context.Background(), ClaudeOptions{Dir: "/proc/no/such/dir"}, h.agentHost("x")); err == nil {
		t.Error("an unwritable folder gave no error")
	}
	if dir := ConfigDir(func(string) string { return "/cfg" }, "/home/sam"); dir != "/cfg" {
		t.Errorf("ConfigDir with CLAUDE_CONFIG_DIR = %q", dir)
	}
	if dir := ConfigDir(func(string) string { return "" }, "/home/sam"); dir != "/home/sam/.claude" {
		t.Errorf("ConfigDir = %q", dir)
	}
}
