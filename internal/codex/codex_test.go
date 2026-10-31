package codex

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/model"
)

// fakeDaemon is a stand-in for the Codex app-server daemon.
type fakeDaemon struct {
	mu      sync.Mutex
	status  map[string]string
	turns   []string
	conn    *websocket.Conn
	wmu     sync.Mutex
	replies chan map[string]any
	resume  bool
}

func startDaemon(t *testing.T) (*fakeDaemon, string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "codex.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{status: map[string]string{"t1": "idle", "t2": "idle", "sub": "idle"}, replies: make(chan map[string]any, 10), resume: true}
	upgrader := websocket.Upgrader{}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		d.mu.Lock()
		d.conn = conn
		d.mu.Unlock()
		d.serve(conn)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Close() })
	return d, socket
}

func (d *fakeDaemon) write(v any) {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	d.mu.Lock()
	conn := d.conn
	d.mu.Unlock()
	_ = conn.WriteJSON(v)
}

func (d *fakeDaemon) serve(conn *websocket.Conn) {
	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}
		if msg["method"] == nil {
			d.replies <- msg
			continue
		}
		if msg["id"] == nil {
			continue
		}
		result, failure := d.answer(msg["method"].(string), msg["params"])
		reply := map[string]any{"jsonrpc": "2.0", "id": msg["id"]}
		if failure != "" {
			reply["error"] = map[string]any{"code": -1, "message": failure}
		} else {
			reply["result"] = result
		}
		d.write(reply)
	}
}

func (d *fakeDaemon) answer(method string, params any) (any, string) {
	p, _ := params.(map[string]any)
	d.mu.Lock()
	defer d.mu.Unlock()
	switch method {
	case "initialize":
		return map[string]any{"userAgent": "fake"}, ""
	case "thread/loaded/list":
		return map[string]any{"data": []string{"t1", "t2", "sub", "missing"}}, ""
	case "thread/read":
		id, _ := p["threadId"].(string)
		status, ok := d.status[id]
		if !ok {
			return nil, "no thread " + id
		}
		thread := map[string]any{"id": id, "cwd": "/repos/" + id, "status": map[string]any{"type": status}}
		if id == "t2" {
			thread["name"] = "billing api"
		}
		if id == "sub" {
			thread["parentThreadId"] = "t1"
		}
		return map[string]any{"thread": thread}, ""
	case "turn/start":
		input := p["input"].([]any)[0].(map[string]any)
		d.turns = append(d.turns, p["threadId"].(string)+": "+input["text"].(string))
		return map[string]any{}, ""
	case "thread/start":
		return map[string]any{"thread": map[string]any{"id": "home-1"}}, ""
	case "thread/resume":
		if !d.resume {
			return nil, "gone"
		}
		return map[string]any{"thread": map[string]any{"id": p["threadId"]}}, ""
	}
	return nil, "unknown method " + method
}

func (d *fakeDaemon) turnList() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.turns...)
}

func format(events []model.Event) string {
	return "msg " + events[0].Message.Text
}

func TestThreadsAndPush(t *testing.T) {
	d, socket := startDaemon(t)
	a := New(Options{Socket: socket, Version: "test", Poll: 10 * time.Millisecond, MaxWait: 200 * time.Millisecond})
	defer a.Close()
	ctx := context.Background()
	threads, err := a.Threads(ctx)
	if err != nil || len(threads) != 2 {
		t.Fatalf("Threads = %+v, %v", threads, err)
	}
	if SessionName(threads[0]) != "t1-codex" || SessionName(threads[1]) != "billing-api" {
		t.Errorf("names = %q, %q", SessionName(threads[0]), SessionName(threads[1]))
	}
	p := a.NewPusher("t1", format)
	if err := p.Push(ctx, nil); err != nil {
		t.Errorf("heartbeat: %v", err)
	}
	if err := p.Push(ctx, []model.Event{{Message: model.Message{Text: "hello"}}}); err != nil {
		t.Fatal(err)
	}
	if turns := d.turnList(); len(turns) != 1 || turns[0] != "t1: msg hello" {
		t.Errorf("turns = %v", turns)
	}
	if err := p.Verdict(ctx, "x", true); err != nil {
		t.Error(err)
	}

	// A busy thread gets the turn once it is idle, and a thread that stays busy fails.
	d.mu.Lock()
	d.status["t1"] = "active"
	d.mu.Unlock()
	go func() {
		time.Sleep(50 * time.Millisecond)
		d.mu.Lock()
		d.status["t1"] = "idle"
		d.mu.Unlock()
	}()
	if err := a.StartTurn(ctx, "t1", "later"); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.status["t1"] = "active"
	d.mu.Unlock()
	if err := a.StartTurn(ctx, "t1", "never"); err == nil || !strings.Contains(err.Error(), "stayed busy") {
		t.Errorf("busy thread: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := a.waitIdle(cancelled, "t1"); err == nil {
		t.Error("waitIdle on a cancelled context gave no error")
	}
	if _, err := a.Thread(ctx, "missing"); err == nil {
		t.Error("missing thread gave no error")
	}
}

func TestApprovalRequestsAndNotifications(t *testing.T) {
	d, socket := startDaemon(t)
	asked := make(chan string, 2)
	a := New(Options{Socket: socket, Approve: func(_ context.Context, title, detail string) bool {
		asked <- title + "|" + detail
		return strings.Contains(detail, "ls")
	}})
	defer a.Close()
	ctx := context.Background()
	if _, err := a.Threads(ctx); err != nil {
		t.Fatal(err)
	}
	d.write(map[string]any{"jsonrpc": "2.0", "id": 90, "method": "item/commandExecution/requestApproval",
		"params": map[string]any{"threadId": "t1", "command": "ls -la", "cwd": "/repos"}})
	if got := <-asked; got != "Codex wants to run a command|command: ls -la\ncwd: /repos" {
		t.Errorf("asked %q", got)
	}
	if reply := <-d.replies; reply["result"].(map[string]any)["decision"] != "accept" {
		t.Errorf("reply = %v", reply)
	}
	d.write(map[string]any{"jsonrpc": "2.0", "id": 91, "method": "item/fileChange/requestApproval",
		"params": map[string]any{"threadId": "t1"}})
	<-asked
	if reply := <-d.replies; reply["result"].(map[string]any)["decision"] != "decline" {
		t.Errorf("reply = %v", reply)
	}
	d.write(map[string]any{"jsonrpc": "2.0", "id": 92, "method": "item/tool/requestUserInput", "params": map[string]any{}})
	if reply := <-d.replies; reply["error"] == nil {
		t.Errorf("unhandled request reply = %v", reply)
	}
	if describe(json.RawMessage(`"x"`)) != `"x"` || describe(json.RawMessage(`{"a":1}`)) != `{"a":1}` {
		t.Error("describe fallback is wrong")
	}
	d.write(map[string]any{"jsonrpc": "2.0", "method": "configWarning", "params": map[string]any{}})
	d.write(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "nobody"}})
}

func TestHome(t *testing.T) {
	d, socket := startDaemon(t)
	a := New(Options{Socket: socket, Poll: 10 * time.Millisecond})
	defer a.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	posts := make(chan string, 5)
	saved := ""
	host := core.HomeHost{Post: func(text string) { posts <- text }, SaveSessionID: func(id string) error { saved = id; return nil }}
	home, err := a.NewHome(ctx, host, "/home", "be home", format)
	if err != nil || home.ThreadID() != "home-1" || saved != "home-1" {
		t.Fatalf("NewHome = %v, %v, saved %q", home, err, saved)
	}
	d.mu.Lock()
	d.status["home-1"] = "idle"
	d.mu.Unlock()
	if err := home.Push(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := home.Push(ctx, []model.Event{{Message: model.Message{Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	d.write(map[string]any{"jsonrpc": "2.0", "method": "item/completed", "params": map[string]any{"threadId": "home-1",
		"item": map[string]any{"type": "agentMessage", "text": "hello owner"}}})
	d.write(map[string]any{"jsonrpc": "2.0", "method": "turn/completed", "params": map[string]any{"threadId": "home-1"}})
	select {
	case got := <-posts:
		if got != "hello owner" {
			t.Errorf("posted %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("home posted nothing")
	}
	if err := home.Verdict(ctx, "x", true); err != nil {
		t.Error(err)
	}
	host.SessionID = "old"
	if resumed, err := a.NewHome(ctx, host, "/home", "", format); err != nil || resumed.ThreadID() != "old" {
		t.Errorf("resume = %v, %v", resumed, err)
	}
	d.mu.Lock()
	d.resume = false
	d.mu.Unlock()
	if fresh, err := a.NewHome(ctx, host, "/home", "", format); err != nil || fresh.ThreadID() != "home-1" {
		t.Errorf("fresh after a failed resume = %v, %v", fresh, err)
	}
}

func TestNoDaemon(t *testing.T) {
	a := New(Options{Socket: filepath.Join(t.TempDir(), "none.sock")})
	defer a.Close()
	if _, err := a.Threads(context.Background()); err == nil {
		t.Error("Threads without a daemon gave no error")
	}
	if _, err := a.NewHome(context.Background(), core.HomeHost{}, "/", "", format); err == nil {
		t.Error("NewHome without a daemon gave no error")
	}
}

func TestReconnectAfterClose(t *testing.T) {
	d, socket := startDaemon(t)
	a := New(Options{Socket: socket})
	defer a.Close()
	ctx := context.Background()
	if _, err := a.Threads(ctx); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	_ = d.conn.Close()
	d.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	if _, err := a.Threads(ctx); err != nil {
		t.Errorf("Threads after the daemon closed the connection: %v", err)
	}
}
