package api_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/api"
	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/testkit"
)

func start(t *testing.T) (*testkit.Daemon, *api.Client) {
	t.Helper()
	socket := filepath.Join(testkit.ShortDir(t), "d.sock")
	d := testkit.Start(t, socket)
	return d, api.NewClient(socket)
}

// session opens a session stream and returns its token and the frames it gets.
func session(t *testing.T, ctx context.Context, c *api.Client, query url.Values) (string, chan api.Frame) {
	t.Helper()
	frames := make(chan api.Frame, 20)
	go func() {
		_ = c.Session(ctx, query, func(f api.Frame) error {
			frames <- f
			return nil
		})
	}()
	select {
	case f := <-frames:
		if f.Type != "hello" || f.Token == "" {
			t.Fatalf("first frame = %+v", f)
		}
		return f.Token, frames
	case <-time.After(3 * time.Second):
		t.Fatal("no hello frame")
	}
	return "", nil
}

func next(t *testing.T, frames chan api.Frame, kind string) api.Frame {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case f := <-frames:
			if f.Type == kind {
				return f
			}
		case <-timeout:
			t.Fatalf("no %s frame", kind)
		}
	}
}

func TestOwnerTools(t *testing.T) {
	d, c := start(t)
	ctx := context.Background()
	d.Core.OnChatName("g@g.us", "Team <> Sam")
	d.Keep("g@g.us")
	d.Receive("g@g.us", "hello", false)

	var chats []model.Chat
	if err := c.Tool(ctx, "", "list_chats", api.QueryArgs{Query: "<>"}, &chats); err != nil || len(chats) != 1 {
		t.Fatalf("list_chats = %+v, %v", chats, err)
	}
	var read core.ChatMessages
	if err := c.Tool(ctx, "", "read_chat", api.ChatArgs{Chat: "Team <> Sam"}, &read); err != nil || len(read.Messages) != 1 {
		t.Fatalf("read_chat = %+v, %v", read, err)
	}
	raw, err := c.ToolRaw(ctx, "", "list_chats", api.QueryArgs{})
	if err != nil || !strings.Contains(string(raw), "Team <> Sam") {
		t.Errorf("raw result escapes <>: %s", raw)
	}
	var text api.Text
	if err := c.Tool(ctx, "", "send", api.SendArgs{Chat: "g@g.us", Text: "hi all"}, &text); err != nil || !strings.HasPrefix(text.Text, "sent") {
		t.Fatalf("send = %+v, %v", text, err)
	}
	if s := d.Expect("hi all"); s.Text != "hi all" {
		t.Errorf("owner send = %q", s.Text)
	}
	if err := c.As("helper").Tool(ctx, "", "send", api.SendArgs{Chat: "g@g.us", Text: "for an agent"}, &text); err != nil {
		t.Fatal(err)
	}
	if s := d.Expect("for an agent"); s.Text != "🤖 *Agent*: for an agent" {
		t.Errorf("agent send = %q", s.Text)
	}
	// The owner answers the message, edits the answer, forwards it and deletes it.
	if err := c.Tool(ctx, "", "send", api.SendArgs{Chat: "g@g.us", Text: "answer", ReplyTo: "M1"}, &text); err != nil {
		t.Fatal(err)
	}
	answer := d.Expect("answer")
	if answer.Out.ReplyTo == nil || answer.Out.ReplyTo.ID != "M1" {
		t.Errorf("reply %+v", answer.Out)
	}
	for _, call := range []struct {
		tool string
		args any
	}{
		{"edit_message", api.EditArgs{Chat: "g@g.us", MessageID: answer.ID, Text: "better answer"}},
		{"forward_message", api.ForwardArgs{Chat: "g@g.us", MessageID: answer.ID, To: "self"}},
		{"delete_message", api.MessageArgs{Chat: "g@g.us", MessageID: answer.ID}},
	} {
		if err := c.Tool(ctx, "", call.tool, call.args, &text); err != nil {
			t.Errorf("%s: %v", call.tool, err)
		}
		d.Expect("")
	}
	file := &model.File{Name: "r.pdf", Data: []byte("%PDF")}
	if err := c.Tool(ctx, "", "send", api.SendArgs{Chat: "g@g.us", Text: "the report", File: file}, &text); err != nil {
		t.Fatal(err)
	}
	if s := d.Expect("the report"); s.File != "r.pdf" {
		t.Errorf("file send = %+v", s)
	}
	for tool, args := range map[string]any{
		"search":         api.SearchArgs{Query: "hello"},
		"react":          api.MessageArgs{Chat: "g@g.us", MessageID: "M1", Emoji: "👀"},
		"route_here":     api.RouteArgs{Chat: "g@g.us"},
		"list_sessions":  struct{}{},
		"command":        api.CommandArgs{Text: "/rules"},
		"status":         struct{}{},
		"export":         api.ChatArgs{Chat: "g@g.us"},
		"changes":        api.ChangesArgs{Cursor: 0},
		"download_media": api.MessageArgs{Chat: "g@g.us", MessageID: "M1"},
	} {
		_, err := c.ToolRaw(ctx, "", tool, args)
		if err != nil && tool != "download_media" {
			t.Errorf("%s: %v", tool, err)
		}
	}
	if _, err := c.ToolRaw(ctx, "", "nope", nil); err == nil {
		t.Error("unknown tool gave no error")
	}
	if _, err := c.ToolRaw(ctx, "", "send", "not an object"); err == nil {
		t.Error("bad arguments gave no error")
	}
}

func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chart.png")
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if f, err := api.ReadFile(path); err != nil || f.Name != "chart.png" || string(f.Data) != "png" {
		t.Errorf("ReadFile = %+v, %v", f, err)
	}
	// A sparse file is large without taking disk space, and ReadFile refuses it before reading.
	big := filepath.Join(dir, "big.bin")
	if err := os.WriteFile(big, nil, 0o600); err != nil || os.Truncate(big, model.MaxFileSize+1) != nil {
		t.Fatal("cannot make a large file")
	}
	for _, p := range []string{big, dir, filepath.Join(dir, "missing")} {
		if _, err := api.ReadFile(p); err == nil {
			t.Errorf("ReadFile(%s) gave no error", p)
		}
	}
}

func TestSessionStreamToolsAndPermission(t *testing.T) {
	d, c := start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Core.OnChatName("g@g.us", "Team")
	if _, err := d.Core.Run(ctx, core.Owner, "/route Team dev"); !errors.Is(err, core.ErrNotAllowed) {
		t.Errorf("/route through the CLI = %v", err)
	}
	d.Phone("/route Team dev", "now goes to dev")
	d.Keep("g@g.us")
	token, frames := session(t, ctx, c, url.Values{"name": {"dev"}, "harness": {"claude"}, "push": {"channel"}})
	time.Sleep(50 * time.Millisecond)
	d.Receive("g@g.us", "ping", false)
	if f := next(t, frames, "events"); f.Events[0].Message.Text != "ping" {
		t.Fatalf("events frame = %+v", f)
	}
	next(t, frames, "heartbeat")

	var text api.Text
	if err := c.Tool(ctx, token, "send", api.SendArgs{Chat: "Team", Text: "pong"}, &text); err != nil {
		t.Fatalf("session send: %v", err)
	}
	d.Expect("pong")
	if _, err := c.ToolRaw(ctx, token, "export", api.ChatArgs{Chat: "g@g.us"}); err == nil {
		t.Error("a session could export")
	}
	if _, err := c.ToolRaw(ctx, "bad-token", "status", nil); err == nil {
		t.Error("a bad token worked")
	}
	if err := c.Permission(ctx, api.PermissionRequest{Token: token, RequestID: "qwert", ToolName: "Bash", Description: "list"}); err != nil {
		t.Fatal(err)
	}
	d.Receive(testkit.SelfChat, "yes "+codeOf(t, d.Expect("session dev wants to use the tool Bash")), true)
	if f := next(t, frames, "verdict"); f.RequestID != "qwert" || !f.Allow {
		t.Errorf("verdict frame = %+v", f)
	}
	if err := c.Permission(ctx, api.PermissionRequest{Token: "bad"}); err == nil {
		t.Error("a permission with a bad token worked")
	}

	approved := make(chan string, 1)
	go func() {
		raw, err := c.ToolRaw(ctx, token, "approve", map[string]any{"tool_name": "Write", "input": map[string]string{"path": "/x"}})
		if err != nil {
			approved <- err.Error()
			return
		}
		approved <- string(raw)
	}()
	code := codeOf(t, d.Expect("session dev wants to use the tool Write"))
	d.Receive(testkit.SelfChat, "no "+code, true)
	if got := <-approved; !strings.Contains(got, `"behavior":"deny"`) {
		t.Errorf("approve = %s", got)
	}
	go func() {
		raw, _ := c.ToolRaw(ctx, token, "approve", map[string]any{"tool_name": "Read"})
		approved <- string(raw)
	}()
	code = codeOf(t, d.Expect("session dev wants to use the tool Read"))
	d.Receive(testkit.SelfChat, "yes "+code, true)
	if got := <-approved; !strings.Contains(got, `"behavior":"allow"`) || !strings.Contains(got, `"updatedInput":{}`) {
		t.Errorf("approve = %s", got)
	}
}

func codeOf(t *testing.T, s testkit.Sent) string {
	t.Helper()
	_, rest, ok := strings.Cut(s.Text, `"yes `)
	code, _, closed := strings.Cut(rest, `"`)
	if !ok || !closed {
		t.Fatalf("no code in %q", s.Text)
	}
	return code
}

func TestToolsOnlySessionAndWatch(t *testing.T) {
	d, c := start(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.Keep("g@g.us")
	token, _ := session(t, ctx, c, url.Values{"folder": {"/repos/app"}})
	var status core.Status
	if err := c.Tool(ctx, token, "status", nil, &status); err != nil || status.Online != 1 {
		t.Errorf("status = %+v, %v", status, err)
	}
	got := make(chan model.Event, 5)
	go func() {
		_ = c.Watch(ctx, "w", []string{"g@g.us"}, 0, func(f api.Frame) error {
			for _, e := range f.Events {
				got <- e
			}
			return nil
		})
	}()
	time.Sleep(100 * time.Millisecond)
	d.Receive("g@g.us", "watched", false)
	select {
	case e := <-got:
		if e.Message.Text != "watched" {
			t.Errorf("watch got %+v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watch got nothing")
	}
	err := c.Watch(ctx, "w2", []string{"unknown chat name"}, -1, func(api.Frame) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "no chat matches") {
		t.Errorf("watch of an unknown chat: %v", err)
	}
}

func TestNoDaemon(t *testing.T) {
	c := api.NewClient(filepath.Join(testkit.ShortDir(t), "none.sock"))
	if _, err := c.ToolRaw(context.Background(), "", "status", nil); !errors.Is(err, api.ErrDaemon) {
		t.Errorf("err = %v, want ErrDaemon", err)
	}
	if err := c.Session(context.Background(), nil, nil); !errors.Is(err, api.ErrDaemon) {
		t.Errorf("session err = %v", err)
	}
	if err := c.Permission(context.Background(), api.PermissionRequest{}); !errors.Is(err, api.ErrDaemon) {
		t.Errorf("permission err = %v", err)
	}
	if _, err := c.ToolRaw(context.Background(), "", "x", make(chan int)); err == nil {
		t.Error("unencodable arguments gave no error")
	}
}
