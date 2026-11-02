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

// keep makes chat kept in the test daemon, so the CLI may read it.
func keep(t *testing.T, d *testkit.Daemon, chat string) {
	t.Helper()
	if _, _, err := d.Store.Keep(context.Background(), chat); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerCommands(t *testing.T) {
	env, d := daemonEnv(t)
	keep(t, d, "g@g.us")
	d.Receive("g@g.us", "line one\nline two", false)
	media := model.Message{Chat: "g@g.us", ID: "IMG", Time: time.Now(), MediaType: "image", MimeType: "image/png", Raw: []byte{1}}
	d.Core.OnUpdate(model.Update{Kind: model.New, Message: media})

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
	// The owner answers a message in a kept chat, edits the answer, forwards it and deletes it.
	keep(t, d, "g@g.us")
	question := d.Receive("g@g.us", "when?", false)
	if _, err := runCtx(context.Background(), env, "", "send", "--to", "g@g.us", "--text", "at 5", "--reply-to", question.ID); err != nil {
		t.Fatal(err)
	}
	answer := d.Expect("at 5")
	if answer.Out.ReplyTo == nil {
		t.Error("the answer quotes nothing")
	}
	for _, args := range [][]string{
		{"edit", "--chat", "g@g.us", "--id", answer.ID, "--text", "at 6"},
		{"forward", "--chat", "g@g.us", "--id", answer.ID, "--to", "self"},
		{"delete", "--chat", "g@g.us", "--id", answer.ID},
		{"poll", "--to", "g@g.us", "--question", "Lunch?", "--option", "Pizza", "--option", "Sushi", "--multiple"},
		{"location", "--to", "g@g.us", "--lat", "52.52", "--lng", "13.405", "--name", "Berlin"},
		{"read", "--chat", "g@g.us", "--id", question.ID},
		{"group", "rename", "--chat", "g@g.us", "--text", "Crew"},
		{"group", "add", "--chat", "g@g.us", "--member", "+1 555 000 1111"},
		{"group", "announce", "--chat", "g@g.us", "--on"},
		{"group", "create", "--name", "Trip", "--member", "+1 555 000 1111"},
		{"group", "join", "--link", "https://chat.whatsapp.com/AbC"},
		{"profile", "--name", "Kan"},
	} {
		if out, err := runCtx(context.Background(), env, "", args...); err != nil || out == "" {
			t.Errorf("%s = %q, %v", args[0], out, err)
		}
		d.Expect("")
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"group", "info", "--chat", "g@g.us"}, `"name": "Team"`},
		{[]string{"contacts", "Sam"}, "200@s.whatsapp.net\tSam"},
		{[]string{"contacts", "Sam", "--json"}, `"name": "Sam"`},
		{[]string{"contact", "--person", "200@s.whatsapp.net"}, `"about": "Hey there"`},
	} {
		if out, err := runCtx(context.Background(), env, "", c.args...); err != nil || !strings.Contains(out, c.want) {
			t.Errorf("%v = %q, %v", c.args, out, err)
		}
	}
	if _, err := runCtx(context.Background(), env, "", "profile", "--picture", "/no/such/photo.png"); err == nil {
		t.Error("a missing picture gave no error")
	}
	if label := contactLabel(model.Contact{PushName: "Sammy", OnWhatsApp: new(false)}); label != "Sammy (not on WhatsApp)" {
		t.Errorf("contactLabel = %q", label)
	}
	photo := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(photo, []byte("jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCtx(context.Background(), env, "", "send", "--to", "self", "--file", photo); err != nil || !strings.HasPrefix(out, "sent") {
		t.Errorf("send --file = %q, %v", out, err)
	}
	if s := d.Expect(""); s.File != "photo.jpg" || s.Text != "" {
		t.Errorf("sent %+v", s)
	}
	if _, err := runCtx(context.Background(), env, "", "send", "--to", "self", "--file", photo+".missing"); err == nil {
		t.Error("a missing file gave no error")
	}
}

func TestWatch(t *testing.T) {
	env, d := daemonEnv(t)
	keep(t, d, "g@g.us")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := make(chan string, 1)
	go func() {
		got, _ := runCtx(ctx, env, "", "watch", "--chat", "g@g.us", "--chat", "o@g.us", "--since", "0")
		out <- got
	}()
	time.Sleep(200 * time.Millisecond)
	d.Receive("o@g.us", "OTHER-SECRET", false)
	d.Receive("g@g.us", "watched message", false)
	time.Sleep(200 * time.Millisecond)
	cancel()
	got := <-out
	if !strings.Contains(got, "[Team <> Sam] Sam: watched message") || strings.Contains(got, "OTHER-SECRET") {
		t.Errorf("watch printed %q", got)
	}
}

// TestCLIReadsOnlyKeptChats is the CLI boundary: no command prints a message of a chat that is
// not kept, and no command changes what is kept, routed or shown.
func TestCLIReadsOnlyKeptChats(t *testing.T) {
	env, d := daemonEnv(t)
	d.Core.OnChatName("o@g.us", "Other")
	secret := d.Receive("o@g.us", "OTHER-SECRET", false)
	d.Core.OnUpdate(model.Update{Kind: model.New, Message: model.Message{Chat: "o@g.us", ID: "IMG", Time: time.Now(),
		MediaType: "image", MimeType: "image/png", Raw: []byte("OTHER-SECRET")}})
	for _, args := range [][]string{
		{"messages", "--chat", "Other"}, {"messages", "--chat", "o@g.us", "--json"}, {"search", "SECRET"},
		{"search", "SECRET", "--chat", "Other"}, {"export", "--chat", "Other"}, {"changes", "--since", "0"},
		{"media", "--chat", "Other", "--id", "IMG"}, {"react", "--chat", "Other", "--id", secret.ID, "--emoji", "👀"},
		{"command", "/keep Other on"}, {"command", "/route Other helper"}, {"command", "/mode Other all"},
		{"command", "/web"}, {"status"}, {"command", "/keep"},
	} {
		out, _ := runCtx(context.Background(), env, "", args...)
		if strings.Contains(out, "OTHER-SECRET") || strings.Contains(out, "token=") {
			t.Errorf("%v printed %q", args, out)
		}
	}
	for _, line := range []string{"/keep Other on", "/route Other helper", "/mode Other all", "/web"} {
		if _, err := runCtx(context.Background(), env, "", "command", line); err == nil {
			t.Errorf("command %s ran through the CLI", line)
		}
	}
	if kept, _ := d.Store.Kept(context.Background(), "o@g.us"); kept {
		t.Error("the CLI kept a chat")
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
	oldExec, oldRun, oldExe := execProcess, runCommand, executable
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
	executable = func() (string, error) { return "/opt/whatsagent", nil }
	t.Cleanup(func() { execProcess, runCommand, executable = oldExec, oldRun, oldExe })
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
