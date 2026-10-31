// Package daemon wires the WhatsAgent daemon together: the WhatsApp connection, the store, the
// core, the local API socket, home and the Codex adapter.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/api"
	"github.com/osolmaz/WhatsAgent/internal/codex"
	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/home"
	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/paths"
	"github.com/osolmaz/WhatsAgent/internal/store"
	"github.com/osolmaz/WhatsAgent/internal/wa"
)

// Options configure the daemon.
type Options struct {
	Dirs    paths.Dirs
	Log     *slog.Logger
	Version string
	// ClaudeBin is the claude executable that home on Claude Code runs.
	ClaudeBin string
	// CodexSocket is the Codex app-server daemon's control socket.
	CodexSocket string
	// CodexEvery is how often the Codex adapter looks for threads.
	CodexEvery time.Duration
}

// ErrRunning is returned when another daemon already serves the socket.
var ErrRunning = errors.New("a WhatsAgent daemon is already running")

// Run runs the daemon until ctx ends.
func Run(ctx context.Context, opts Options) error {
	socket, err := opts.Dirs.Socket()
	if err != nil {
		return err
	}
	client, err := wa.Open(ctx, opts.Dirs.SessionDB(), opts.Log)
	if err != nil {
		return err
	}
	defer client.Close()
	st, err := store.Open(ctx, opts.Dirs.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()
	listener, err := Listen(socket)
	if err != nil {
		return err
	}
	c, adapter := build(opts, st, client)
	defer adapter.Close()
	server := &http.Server{Handler: api.NewServer(c), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	c.Start(ctx)
	go discover(ctx, c, st, adapter, opts.CodexEvery, turnText(client.SelfChat()))
	return client.Run(ctx, c)
}

// build creates the core with both home adapters and the Codex adapter.
func build(opts Options, st *store.Store, client *wa.Client) (*core.Core, *codex.Adapter) {
	var c *core.Core
	adapter := codex.New(codex.Options{Socket: opts.CodexSocket, Version: opts.Version, Log: opts.Log,
		Approve: func(ctx context.Context, title, detail string) bool {
			return c.Approve(ctx, core.Caller{Session: "codex"}, title, detail, 30*time.Minute)
		}})
	homeDir := filepath.Join(opts.Dirs.Data, "home")
	selfChat := client.SelfChat()
	c = core.New(core.Config{
		Store: st, WhatsApp: client, Log: opts.Log, MediaDir: filepath.Join(opts.Dirs.Data, "media"),
		SendGap: 2 * time.Second, StatusDelay: 3 * time.Second, Heartbeat: 30 * time.Second,
		Homes: map[string]core.HomeFactory{
			"claude": func(ctx context.Context, host core.HomeHost) (core.Pusher, error) {
				return home.NewClaude(ctx, home.ClaudeOptions{Bin: opts.ClaudeBin, Dir: homeDir, SelfChat: selfChat, Log: opts.Log}, host)
			},
			"codex": func(ctx context.Context, host core.HomeHost) (core.Pusher, error) {
				format := func(events []model.Event) string { return home.Prompt(events, selfChat, time.Local) }
				return adapter.NewHome(ctx, host, homeDir, home.Instructions, format)
			},
		},
	})
	return c, adapter
}

// turnText formats routed events as one turn for a session that the adapter pushes into.
func turnText(selfChat string) func([]model.Event) string {
	return func(events []model.Event) string {
		return "New WhatsApp messages that WhatsAgent routed to this session. Answer with the whatsagent " +
			"send tool and the chat_id, only when a message needs an answer from you.\n\n" +
			home.Prompt(events, selfChat, time.Local)
	}
}

// Listen opens the daemon's Unix socket with owner-only permissions. It removes a stale socket
// file, and fails when another daemon answers on it.
func Listen(socket string) (net.Listener, error) {
	if err := clearSocket(socket); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

// clearSocket makes room for the socket: it fails when a daemon answers on it, removes a stale
// socket file, and creates the socket's folder.
func clearSocket(socket string) error {
	if conn, err := net.Dial("unix", socket); err == nil {
		conn.Close()
		return fmt.Errorf("%w on %s", ErrRunning, socket)
	}
	if err := os.Remove(socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.MkdirAll(filepath.Dir(socket), 0o700)
}

// threadSessions holds the push sessions that discovery attached, by thread ID.
type threadSessions map[string]context.CancelFunc

// discover attaches every loaded Codex thread as a push session and detaches threads that the
// Codex daemon unloaded. Home's own thread is left out.
func discover(ctx context.Context, c *core.Core, st *store.Store, a *codex.Adapter, every time.Duration,
	format func([]model.Event) string) {
	attached := threadSessions{}
	done := make(chan string, 16)
	for {
		if threads, err := a.Threads(ctx); err == nil {
			homeThread, _ := st.Setting(ctx, "home.codex.session")
			attached.sync(ctx, c, a, threads, homeThread, format, done)
		}
		select {
		case <-ctx.Done():
			return
		case id := <-done:
			delete(attached, id)
		case <-time.After(every):
		}
	}
}

func (ts threadSessions) sync(ctx context.Context, c *core.Core, a *codex.Adapter, threads []codex.Thread,
	homeThread string, format func([]model.Event) string, done chan<- string) {
	loaded := map[string]bool{}
	names := map[string]bool{}
	for _, t := range threads {
		loaded[t.ID] = true
		if t.ID == homeThread || ts[t.ID] != nil {
			continue
		}
		name := codex.SessionName(t)
		if names[name] {
			name += "-" + t.ID[:min(6, len(t.ID))]
		}
		names[name] = true
		threadCtx, cancel := context.WithCancel(ctx)
		ts[t.ID] = cancel
		go func(id string) {
			info := model.Session{Name: name, Harness: "codex", Folder: t.Cwd}
			_ = c.Attach(threadCtx, info, "codex", a.NewPusher(id, format))
			done <- id
		}(t.ID)
	}
	for id, cancel := range ts {
		if !loaded[id] {
			cancel()
		}
	}
}

// CodexSocket returns the Codex app-server daemon's control socket for a Codex home folder.
func CodexSocket(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

// HarnessFromEnv guesses the harness that started the MCP server.
func HarnessFromEnv(getenv func(string) string) string {
	if h := getenv("WHATSAGENT_HARNESS"); h != "" {
		return h
	}
	if getenv("CLAUDECODE") != "" {
		return "claude"
	}
	return "unknown"
}
