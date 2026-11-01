package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/codex"
	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/paths"
	"github.com/osolmaz/WhatsAgent/internal/store"
	"github.com/osolmaz/WhatsAgent/internal/wa"
)

// shortDir returns a folder under /tmp, because a Unix socket path must stay under 104 bytes.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func dirs(t *testing.T) paths.Dirs {
	t.Helper()
	dir := shortDir(t)
	return paths.Dirs{Data: dir, Config: dir}
}

func TestListen(t *testing.T) {
	socket := filepath.Join(shortDir(t), "d.sock")
	listener, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(socket); info.Mode().Perm() != 0o600 {
		t.Errorf("socket mode = %v", info.Mode().Perm())
	}
	go func(l net.Listener) {
		if conn, err := l.Accept(); err == nil {
			conn.Close()
		}
	}(listener)
	if _, err := Listen(socket); !errors.Is(err, ErrRunning) {
		t.Errorf("second Listen: %v", err)
	}
	listener.Close()
	// A stale socket file from a crashed daemon is replaced.
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err = Listen(socket)
	if err != nil {
		t.Fatalf("Listen over a stale file: %v", err)
	}
	listener.Close()
	if _, err := Listen("/proc/none/d.sock"); err == nil {
		t.Error("Listen in an unwritable folder gave no error")
	}
	// A folder in the socket's place cannot be removed.
	busy := filepath.Join(t.TempDir(), "busy")
	if err := os.MkdirAll(filepath.Join(busy, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(busy); err == nil {
		t.Error("Listen over a folder gave no error")
	}
}

func TestRunWithoutAccount(t *testing.T) {
	d := dirs(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	err := Run(context.Background(), Options{Dirs: d, Log: log, CodexSocket: filepath.Join(d.Data, "none.sock"), CodexEvery: time.Hour})
	if !errors.Is(err, wa.ErrNoAccount) {
		t.Errorf("Run = %v, want ErrNoAccount", err)
	}
	long := paths.Dirs{Data: "/" + strings.Repeat("x", 120)}
	if err := Run(context.Background(), Options{Dirs: long, Log: log}); err == nil {
		t.Error("Run with a long socket path gave no error")
	}
	if err := Run(context.Background(), Options{Dirs: paths.Dirs{Data: "/proc/none"}, Log: log}); err == nil {
		t.Error("Run in an unwritable folder gave no error")
	}
}

func TestHelpers(t *testing.T) {
	if got := CodexSocket("/home/o/.codex"); got != "/home/o/.codex/app-server-control/app-server-control.sock" {
		t.Errorf("CodexSocket = %q", got)
	}
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }
	if HarnessFromEnv(getenv) != "unknown" {
		t.Error("empty env harness")
	}
	env["CLAUDECODE"] = "1"
	if HarnessFromEnv(getenv) != "claude" {
		t.Error("claude env harness")
	}
	env["WHATSAGENT_HARNESS"] = "codex"
	if HarnessFromEnv(getenv) != "codex" {
		t.Error("explicit harness")
	}
	text := turnText("self@s.whatsapp.net")([]model.Event{{ChatName: "Team", Message: model.Message{Chat: "g@g.us", Text: "hi", Time: time.Now()}}})
	if !strings.Contains(text, "whatsagent send tool") || !strings.Contains(text, "hi") {
		t.Errorf("turnText = %q", text)
	}
	want := `{"mcpServers":{"whatsagent":{"args":["channel"],"command":"/bin/whatsagent",` +
		`"env":{"WHATSAGENT_HARNESS":"claude","WHATSAGENT_SESSION":"chat-team"},"type":"stdio"}}}`
	if got := mcpConfig("/bin/whatsagent", "chat-team"); got != want {
		t.Errorf("mcpConfig = %s", got)
	}
	dir := t.TempDir()
	start := chatAgents(Options{Dirs: paths.Dirs{Data: dir}, ClaudeBin: "/no/such/claude", Bin: "/bin/whatsagent"}, "self")
	if _, err := start(context.Background(), core.AgentHost{Session: "chat-team", SaveSessionID: func(string) error { return nil }}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agents", "chat-team")); err != nil {
		t.Errorf("the chat agent got no folder: %v", err)
	}
}

type fakeWA struct{}

func (fakeWA) SelfChat() string                                            { return "self@s.whatsapp.net" }
func (fakeWA) OwnName() string                                             { return "Owner" }
func (fakeWA) NewID() string                                               { return "id" }
func (fakeWA) SendText(context.Context, string, string, string) error      { return nil }
func (fakeWA) React(context.Context, string, string, string, string) error { return nil }
func (fakeWA) Download(context.Context, []byte) ([]byte, error)            { return nil, nil }
func (fakeWA) ChatName(context.Context, string) string                     { return "" }

func TestBuildAndDiscover(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := dirs(t)
	st, err := store.Open(ctx, filepath.Join(d.Data, "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	client, err := wa.Open(ctx, filepath.Join(d.Data, "session.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	c, adapter := build(Options{Dirs: d, CodexSocket: filepath.Join(d.Data, "none.sock")}, st, client)
	if c == nil || adapter == nil {
		t.Fatal("build returned nil")
	}
	if reply, err := c.Run(ctx, core.Owner, "/home codex"); err == nil {
		t.Errorf("/home codex without a Codex daemon = %q", reply)
	}
	if reply, err := c.Run(ctx, core.Owner, "/home claude"); err != nil {
		t.Errorf("/home claude = %q, %v", reply, err)
	}
	if !c.Approve(ctx, core.Caller{Session: "x"}, "t", "i", time.Millisecond) == false {
		t.Error("Approve without an answer")
	}

	// discover runs until ctx ends, even without a Codex daemon.
	short, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	discover(short, c, st, adapter, 10*time.Millisecond, turnText("self"))

	// sync attaches new threads, names clashes apart, skips home and detaches unloaded threads.
	core2 := core.New(core.Config{Store: st, WhatsApp: fakeWA{}, Heartbeat: time.Hour, StatusDelay: time.Millisecond})
	core2.Start(ctx)
	ts := threadSessions{}
	done := make(chan string, 10)
	threads := []codex.Thread{{ID: "aaaaaaaa1", Cwd: "/r/app"}, {ID: "bbbbbbbb2", Cwd: "/r/app"}, {ID: "home"}}
	ts.sync(ctx, core2, adapter, threads, "home", turnText("self"), done)
	if len(ts) != 2 {
		t.Fatalf("attached %d threads", len(ts))
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		sessions, _ := st.Sessions(ctx)
		if len(sessions) >= 3 {
			names := []string{}
			for _, s := range sessions {
				names = append(names, s.Name)
			}
			joined := strings.Join(names, ",")
			if !strings.Contains(joined, "app-codex") || !strings.Contains(joined, "app-codex-bbbbbb") {
				t.Errorf("sessions = %s", joined)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ts.sync(ctx, core2, adapter, nil, "home", turnText("self"), done)
	for i := 0; i < 2; i++ {
		select {
		case id := <-done:
			delete(ts, id)
		case <-time.After(3 * time.Second):
			t.Fatal("a detached thread did not end")
		}
	}
}
