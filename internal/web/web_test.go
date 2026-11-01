package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/osolmaz/WhatsAgent/internal/term"
)

type memory struct{ saved string }

func (m *memory) Load() string            { return m.saved }
func (m *memory) Save(value string) error { m.saved = value; return nil }

func start(t *testing.T) (*Server, *httptest.Server, *term.Host, *memory) {
	t.Helper()
	terminals := term.NewHost()
	store := &memory{}
	s, err := New(terminals, store)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts, terminals, store
}

func get(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func dial(t *testing.T, ts *httptest.Server, path string, origin bool) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	header := http.Header{}
	if origin {
		header.Set("Origin", ts.URL)
	}
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+path, header)
}

func TestPageAndAPI(t *testing.T) {
	s, ts, _, store := start(t)
	tok := "?token=" + s.token
	if code, _ := get(t, ts, "/"); code != http.StatusForbidden {
		t.Errorf("page without the token = %d", code)
	}
	if code, body := get(t, ts, "/"+tok); code != http.StatusOK || !strings.Contains(body, "<title>WhatsAgent</title>") {
		t.Errorf("page = %d", code)
	}
	for _, asset := range []string{"/app.js", "/style.css", "/logo", "/favicon.ico", "/vendor/ghostty-web.js",
		"/ghostty-vt.wasm", "/fonts/monaspace-argon-400.woff2"} {
		if code, _ := get(t, ts, asset); code != http.StatusOK {
			t.Errorf("%s = %d", asset, code)
		}
	}
	if code, _ := get(t, ts, "/nothing.js"); code != http.StatusNotFound {
		t.Errorf("missing asset = %d", code)
	}
	if code, _ := get(t, ts, "/api/config"); code != http.StatusForbidden {
		t.Errorf("api without the token = %d", code)
	}
	code, body := get(t, ts, "/api/config"+tok)
	if code != http.StatusOK || !strings.Contains(body, `"title":"WhatsAgent"`) || !strings.Contains(body, "catppuccin-latte") {
		t.Errorf("config = %d %s", code, body)
	}
	if code, body := get(t, ts, "/api/sessions"+tok); code != http.StatusOK || body != "{\"sessions\":[]}\n" {
		t.Errorf("sessions = %d %s", code, body)
	}
	if code, _ := get(t, ts, "/api/nothing"+tok); code != http.StatusNotFound {
		t.Errorf("unknown api = %d", code)
	}
	resp, err := http.Post(ts.URL+"/api/sessions"+tok, "application/json", strings.NewReader("{}"))
	if err != nil || resp.StatusCode != http.StatusConflict {
		t.Errorf("new session = %v, %v", resp, err)
	}
	resp.Body.Close()

	// Settings change for every page and persist.
	page, _, err := dial(t, ts, "/api/events"+tok, true)
	if err != nil {
		t.Fatal(err)
	}
	defer page.Close()
	var first map[string]any
	if err := page.ReadJSON(&first); err != nil || first["type"] != "sessions" {
		t.Fatalf("first event = %v, %v", first, err)
	}
	resp, err = http.Post(ts.URL+"/api/settings"+tok, "application/json", strings.NewReader(`{"theme":"catppuccin-mocha","fontSize":16,"accent":"teal"}`))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("settings = %v, %v", resp, err)
	}
	resp.Body.Close()
	var changed struct {
		Type     string   `json:"type"`
		Settings Settings `json:"settings"`
	}
	if err := page.ReadJSON(&changed); err != nil || changed.Settings.Theme != "catppuccin-mocha" || changed.Settings.FontSize != 16 {
		t.Errorf("settings event = %+v, %v", changed, err)
	}
	if !strings.Contains(store.saved, "catppuccin-mocha") {
		t.Errorf("saved settings = %q", store.saved)
	}
	again, err := New(term.NewHost(), store)
	if err != nil || again.settings.Theme != "catppuccin-mocha" {
		t.Errorf("settings after a restart = %+v, %v", again.settings, err)
	}
	for _, bad := range []string{`{"theme":"nope"}`, `{"fontSize":9}`, `{"accent":"plaid"}`, `[1]`} {
		resp, err := http.Post(ts.URL+"/api/settings"+tok, "application/json", strings.NewReader(bad))
		if err != nil || resp.StatusCode != http.StatusBadRequest {
			t.Errorf("settings %s = %v, %v", bad, resp, err)
		}
		resp.Body.Close()
	}
}

func TestGuards(t *testing.T) {
	s, ts, _, _ := start(t)
	tok := "?token=" + s.token
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/"+tok, nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign host = %v, %v", resp, err)
	}
	resp.Body.Close()
	if _, resp, err := dial(t, ts, "/api/events"+tok, false); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("socket without an origin = %v", err)
	}
	if !loopback("localhost:80") || !loopback("[::1]:9") || !loopback("127.0.0.1") || loopback("example.com:80") {
		t.Error("loopback is wrong")
	}
}

func TestTerminal(t *testing.T) {
	s, ts, terminals, _ := start(t)
	tok := "token=" + s.token
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ended, err := terminals.Start(ctx, term.Spec{Name: "chat-team", Title: "Team", Command: "/bin/sh",
		Args: []string{"-c", `echo "héllo"; read a; echo "typed:$a"; stty size; read b`}, Env: os.Environ()})
	if err != nil {
		t.Fatal(err)
	}
	s.Notify()
	conn, _, err := dial(t, ts, "/api/terminal/chat-team?cols=90&rows=20&"+tok, true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	read := func(want string) {
		t.Helper()
		var all strings.Builder
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			_ = conn.SetReadDeadline(deadline)
			_, data, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read: %v; got %q", err, all.String())
			}
			if all.Write(data); strings.Contains(all.String(), want) {
				return
			}
		}
		t.Fatalf("no %q in %q", want, all.String())
	}
	read("héllo")
	_ = conn.WriteJSON(map[string]any{"type": "resize", "cols": 77, "rows": 21})
	_ = conn.WriteMessage(websocket.TextMessage, []byte("not json"))
	_ = conn.WriteJSON(map[string]any{"type": "input", "data": "hi\r"})
	read("typed:hi")
	read("21 77")
	if list := s.summaries(); len(list) != 1 || list[0].Key != "chat-team" || list[0].Title != "Team" || !list[0].Live {
		t.Errorf("summaries = %+v", list)
	}
	_ = conn.WriteJSON(map[string]any{"type": "input", "data": "bye\r"})
	<-ended
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if ce, ok := err.(*websocket.CloseError); !ok || ce.Code != 4410 {
				t.Errorf("close after the session ended = %v", err)
			}
			break
		}
	}

	unknown, _, err := dial(t, ts, "/api/terminal/none?"+tok, true)
	if err != nil {
		t.Fatal(err)
	}
	defer unknown.Close()
	if _, _, err := unknown.ReadMessage(); err == nil || err.(*websocket.CloseError).Code != 4404 {
		t.Errorf("unknown session = %v", err)
	}
}

func TestListenAndHelpers(t *testing.T) {
	s, err := New(term.NewHost(), &memory{saved: "not json"})
	if err != nil || s.settings.Theme != "catppuccin-latte" || s.settings.FontSize != 14 {
		t.Fatalf("New with bad saved settings = %+v, %v", s.settings, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	address, err := s.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(address)
	if err != nil || u.Query().Get("token") != s.token || !strings.HasPrefix(u.Host, "127.0.0.1:") {
		t.Fatalf("Listen = %q, %v", address, err)
	}
	resp, err := http.Get(address)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("served page = %v, %v", resp, err)
	}
	resp.Body.Close()

	text, rest := whole([]byte("ab\xc3"))
	if string(text) != "ab" || string(rest) != "\xc3" {
		t.Errorf("whole split = %q %q", text, rest)
	}
	text, rest = whole(append(rest, 0xa9, 0xff))
	if string(text) != "é�" || len(rest) != 0 {
		t.Errorf("whole join = %q %q", text, rest)
	}
	merged, err := mergeJSON([]byte(`{"a":1}`), []byte(`{"b":2}`))
	if err != nil || string(merged) != `{"a":1,"b":2}` {
		t.Errorf("mergeJSON = %s, %v", merged, err)
	}
	var v map[string]any
	if json.Unmarshal(merged, &v) != nil {
		t.Error("merged settings are not JSON")
	}
}
