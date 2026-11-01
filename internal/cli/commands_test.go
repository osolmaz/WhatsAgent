package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/paths"
	"github.com/osolmaz/WhatsAgent/internal/testkit"
	"github.com/osolmaz/WhatsAgent/internal/wa"
)

// daemonEnv returns an environment whose WhatsAgent folders are in a temporary folder, and a
// test daemon on its socket.
func daemonEnv(t *testing.T) (paths.Env, *testkit.Daemon) {
	t.Helper()
	state := testkit.ShortDir(t)
	vars := map[string]string{"XDG_STATE_HOME": state, "XDG_CONFIG_HOME": filepath.Join(state, "config"), "PATH": "/usr/bin"}
	env := paths.Env{GOOS: "linux", Home: state, Getenv: func(k string) string { return vars[k] }}
	dirs, _ := paths.Resolve(env)
	socket, _ := dirs.Socket()
	d := testkit.Start(t, socket)
	d.Core.OnChatName("g@g.us", "Team <> Sam")
	return env, d
}

// runCtx runs the CLI with a context and standard input.
func runCtx(ctx context.Context, env paths.Env, stdin string, args ...string) (string, error) {
	var out bytes.Buffer
	root := NewRoot(&out, slog.New(slog.NewTextHandler(io.Discard, nil)), env)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return out.String(), err
}

func TestOwnerCommands(t *testing.T) {
	env, d := daemonEnv(t)
	d.Receive("g@g.us", "line one\nline two", false)
	media := model.Message{Chat: "g@g.us", ID: "IMG", Time: time.Now(), MediaType: "image", MimeType: "image/png", Raw: []byte{1}}
	d.Core.OnUpdate(model.Update{Kind: model.New, Message: media}, true)

	checks := []struct {
		args []string
		want string
	}{
		{[]string{"status"}, "WhatsApp: connected"},
		{[]string{"status", "--json"}, `"self_chat": "100@s.whatsapp.net"`},
		{[]string{"command", "/rules"}, "No send rules"},
		{[]string{"chats", "Team"}, "g@g.us\tTeam <> Sam"},
		{[]string{"chats", "--json"}, `"name": "Team <> Sam"`},
		{[]string{"messages", "--chat", "Team <> Sam"}, "Sam: line one ⏎ line two"},
		{[]string{"messages", "--chat", "g@g.us", "--json"}, `"messages": [`},
		{[]string{"search", "line"}, "[g@g.us] Sam: line one"},
		{[]string{"export", "--chat", "g@g.us"}, `"text":"line one\nline two"`},
		{[]string{"changes", "--since", "0"}, `"g@g.us"`},
		{[]string{"react", "--chat", "g@g.us", "--id", "M1", "--emoji", "👀"}, "reacted"},
		{[]string{"media", "--chat", "g@g.us", "--id", "IMG"}, "IMG.png"},
	}
	for _, c := range checks {
		out, err := runCtx(context.Background(), env, "", c.args...)
		if err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%v = %q, %v; want %q", c.args, out, err, c.want)
		}
	}
	copyTo := filepath.Join(t.TempDir(), "copy.png")
	if _, err := runCtx(context.Background(), env, "", "media", "--chat", "g@g.us", "--id", "IMG", "-o", copyTo); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(copyTo); string(data) != "media" {
		t.Errorf("copied %q", data)
	}
	if _, err := runCtx(context.Background(), env, "", "media", "--chat", "g@g.us", "--id", "M1"); err == nil {
		t.Error("media of a text message gave no error")
	}

	if out, err := runCtx(context.Background(), env, "", "send", "--to", "Team <> Sam", "--text", "hello"); err != nil || !strings.HasPrefix(out, "sent") {
		t.Errorf("send = %q, %v", out, err)
	}
	if s := d.Expect("hello"); s.Text != "hello" {
		t.Errorf("owner send = %q", s.Text)
	}
	if _, err := runCtx(context.Background(), env, "", "send", "--to", "self", "--session", "helper", "--text", "signed"); err != nil {
		t.Fatal(err)
	}
	if s := d.Expect("signed"); s.Text != "🤖 *Agent*: signed" {
		t.Errorf("agent send = %q", s.Text)
	}
	if _, err := runCtx(context.Background(), env, "from stdin\n", "send", "--to", "self"); err != nil {
		t.Error(err)
	}
	d.Expect("from stdin")
	if _, err := runCtx(context.Background(), env, "  \n", "send", "--to", "self"); err == nil {
		t.Error("an empty send gave no error")
	}
}

func TestWatch(t *testing.T) {
	env, d := daemonEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := make(chan string, 1)
	go func() {
		got, _ := runCtx(ctx, env, "", "watch", "--chat", "g@g.us", "--since", "0")
		out <- got
	}()
	time.Sleep(200 * time.Millisecond)
	d.Receive("g@g.us", "watched message", false)
	time.Sleep(200 * time.Millisecond)
	cancel()
	if got := <-out; !strings.Contains(got, "[Team <> Sam] Sam: watched message [id=M1") {
		t.Errorf("watch printed %q", got)
	}
}

func TestChannelCommand(t *testing.T) {
	env, _ := daemonEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := runCtx(ctx, env, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n", "channel")
	if err != nil || !strings.Contains(out, `"serverInfo":{"name":"whatsagent"`) {
		t.Errorf("channel = %q, %v", out, err)
	}
}

func TestNoDaemonCommands(t *testing.T) {
	env := paths.Env{GOOS: "linux", Home: testkit.ShortDir(t), Getenv: func(string) string { return "" }}
	if _, err := runCtx(context.Background(), env, "", "status"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("status without a daemon: %v", err)
	}
	for _, args := range [][]string{{"status", "--json"}, {"chats"}, {"messages", "--chat", "x"}, {"search", "x"},
		{"export", "--chat", "x"}, {"changes"}, {"media", "--chat", "x", "--id", "y"}, {"watch"}} {
		if _, err := runCtx(context.Background(), env, "", args...); err == nil {
			t.Errorf("%v without a daemon gave no error", args)
		}
	}
	bad := paths.Env{GOOS: "plan9", Home: "/x"}
	for _, args := range [][]string{{"status"}, {"watch"}, {"channel"}, {"daemon"}, {"service", "install"}} {
		if _, err := runCtx(context.Background(), bad, "", args...); err == nil {
			t.Errorf("%v on plan9 gave no error", args)
		}
	}
}

func TestDaemonCommandWithoutAccount(t *testing.T) {
	env := paths.Env{GOOS: "linux", Home: testkit.ShortDir(t), Getenv: func(string) string { return "" }}
	if _, err := runCtx(context.Background(), env, "", "daemon", "-v"); !errors.Is(err, wa.ErrNoAccount) {
		t.Errorf("daemon without an account: %v", err)
	}
	if got := codexHome(paths.Env{Home: "/h", Getenv: func(k string) string { return map[string]string{"CODEX_HOME": "/c"}[k] }}); got != "/c" {
		t.Errorf("codexHome = %q", got)
	}
	if getenv(paths.Env{}, "X") != "" {
		t.Error("getenv without Getenv")
	}
}

// seams replaces the process and command seams for one test.
func seams(t *testing.T) *[]string {
	t.Helper()
	var ran []string
	oldExec, oldRun, oldStart, oldExe := execProcess, runCommand, startCommand, executable
	execProcess = func(path string, argv, _ []string) error {
		ran = append(ran, "exec "+strings.Join(argv, " "))
		return nil
	}
	runCommand = func(name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		if name == "fail" {
			return errors.New("failed")
		}
		return nil
	}
	startCommand = runCommand
	executable = func() (string, error) { return "/opt/whatsagent", nil }
	t.Cleanup(func() { execProcess, runCommand, startCommand, executable = oldExec, oldRun, oldStart, oldExe })
	return &ran
}

func TestSetupLaunchAndService(t *testing.T) {
	ran := seams(t)
	env, _ := daemonEnv(t)
	bin := testkit.ShortDir(t)
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := runCtx(context.Background(), env, "", "setup", "claude"); err != nil || !strings.Contains(out, "Registered") {
		t.Errorf("setup claude = %q, %v", out, err)
	}
	if _, err := runCtx(context.Background(), env, "", "setup", "codex"); err != nil {
		t.Error(err)
	}
	if _, err := runCtx(context.Background(), env, "", "setup", "vim"); err == nil {
		t.Error("setup vim gave no error")
	}
	if _, err := runCtx(context.Background(), env, "", "claude", "--resume", "x"); err != nil {
		t.Error(err)
	}
	if _, err := runCtx(context.Background(), env, "", "codex", "resume"); err != nil {
		t.Error(err)
	}
	out, err := runCtx(context.Background(), env, "", "service", "install", "--web-host", "100.1.2.3", "--web-name", "box.ts.net")
	if err != nil || !strings.Contains(out, "whatsagent.service") {
		t.Errorf("service install = %q, %v", out, err)
	}
	unitPath := strings.TrimSpace(strings.TrimPrefix(out, "Installed and started"))
	if unit, _ := os.ReadFile(unitPath); !strings.Contains(string(unit), "ExecStart=/opt/whatsagent daemon --web-host 100.1.2.3 --web-name box.ts.net\n") {
		t.Errorf("unit = %s", unit)
	}
	if _, err := runCtx(context.Background(), env, "", "service", "uninstall"); err != nil {
		t.Error(err)
	}
	joined := strings.Join(*ran, "\n")
	for _, want := range []string{
		"claude mcp add --scope user whatsagent -e WHATSAGENT_HARNESS=claude -- /opt/whatsagent channel",
		"codex mcp add whatsagent --env WHATSAGENT_HARNESS=codex -- /opt/whatsagent channel",
		"systemctl --user enable --now whatsagent.service",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("ran:\n%s\nmissing %q", joined, want)
		}
	}
	for _, want := range []string{"exec claude --dangerously-load-development-channels server:whatsagent --resume x", "exec codex --remote unix:// resume"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ran:\n%s\nmissing %q", joined, want)
		}
	}
	harnessSetup["broken"] = func(string) [][]string { return [][]string{{"fail"}, {"fail"}} }
	defer delete(harnessSetup, "broken")
	if _, err := runCtx(context.Background(), env, "", "setup", "broken"); err == nil {
		t.Error("a failing setup gave no error")
	}
	executable = func() (string, error) { return "", errors.New("no executable") }
	if _, err := runCtx(context.Background(), env, "", "setup", "claude"); err == nil {
		t.Error("setup without an executable path gave no error")
	}
	if _, err := runCtx(context.Background(), env, "", "service", "uninstall"); err == nil {
		t.Error("service without an executable path gave no error")
	}
	if err := launch("no-such-harness-binary", nil, "", nil); err == nil {
		t.Error("launch of a missing binary gave no error")
	}
}
