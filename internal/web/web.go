// Package web serves the browser page that shows the sessions WhatsAgent starts itself, each in a
// web terminal. The page, its terminal (ghostty-web) and its font come from pi-factory-web and
// speak the same protocol; see the licenses folder. The server listens on loopback only, and
// every request needs the random token in the page's address.
package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	"github.com/osolmaz/WhatsAgent/internal/term"
)

//go:embed ui
var ui embed.FS

const (
	title      = "WhatsAgent"
	fontFamily = `"Monaspace Argon", ui-monospace, Menlo, Consolas, monospace`
	maxBody    = 64 * 1024
	// pollEvery is how often the page's session list is checked for changes, such as a session
	// that starts or stops responding.
	pollEvery = time.Second
)

// Sessions is the part of the session host that the page uses.
type Sessions interface {
	List() []term.Summary
	Attach(name string, cols, rows int, v term.Viewer) (func(), bool)
	Input(name, data string)
	Resize(name string, cols, rows int)
}

// Store keeps the page's settings across restarts.
type Store interface {
	Load() string
	Save(settings string) error
}

// Settings are the page's look, shared by every viewer.
type Settings struct {
	Theme    string  `json:"theme"`
	Accent   *string `json:"accent"`
	FontSize int     `json:"fontSize"`
}

type themeChoice struct {
	ID      string            `json:"id"`
	Accents map[string]string `json:"accents"`
}

var fontSizes = []int{11, 12, 13, 14, 15, 16, 18, 20}

// Server serves the page.
type Server struct {
	sessions Sessions
	store    Store
	// names are host names besides the loopback names that the page may be opened under, such
	// as a Tailscale MagicDNS name.
	names    []string
	token    string
	themes   json.RawMessage
	choices  []themeChoice
	upgrader websocket.Upgrader

	mu       sync.Mutex
	settings Settings
	pages    map[*page]bool
	last     string
}

// page is one open page's event socket.
type page struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (p *page) send(v any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.conn.WriteJSON(v)
}

// New returns a server for sessions, with settings kept in store. names are host names besides
// the loopback names that the page may be opened under.
func New(sessions Sessions, store Store, names ...string) (*Server, error) {
	themes, err := ui.ReadFile("ui/themes.json")
	if err != nil {
		return nil, err
	}
	s := &Server{sessions: sessions, store: store, names: names, token: rand.Text(), themes: themes, pages: map[*page]bool{}}
	if err := json.Unmarshal(themes, &s.choices); err != nil || len(s.choices) == 0 {
		return nil, fmt.Errorf("read the page themes: %w", err)
	}
	s.settings = Settings{Theme: s.choices[0].ID, FontSize: 14}
	if saved, err := s.parseSettings([]byte(store.Load())); err == nil {
		s.settings = saved
	}
	return s, nil
}

// Listen serves the page on a free port of host, or of loopback when host is "", until ctx ends,
// and returns its address. A host other than loopback also becomes one of the page's names.
func (s *Server) Listen(ctx context.Context, host string) (string, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return "", err
	}
	s.names = append(s.names, host)
	address := listener.Addr().String()
	if len(s.names) > 1 {
		_, port, _ := net.SplitHostPort(address)
		address = net.JoinHostPort(s.names[0], port)
	}
	server := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = server.Serve(listener) }()
	go s.watch(ctx)
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	return fmt.Sprintf("http://%s/?token=%s", address, s.token), nil
}

// watch sends the session list to the open pages whenever it changes.
func (s *Server) watch(ctx context.Context) {
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Notify()
		}
	}
}

// Notify sends the session list to every open page when it changed since the last time.
func (s *Server) Notify() {
	message := map[string]any{"type": "sessions", "sessions": s.summaries()}
	data, _ := json.Marshal(message)
	s.mu.Lock()
	if string(data) == s.last {
		s.mu.Unlock()
		return
	}
	s.last = string(data)
	pages := s.openPages()
	s.mu.Unlock()
	for _, p := range pages {
		p.send(message)
	}
}

// Viewers returns how many pages are open.
func (s *Server) Viewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pages)
}

// openPages returns the open pages. The caller holds s.mu.
func (s *Server) openPages() []*page {
	pages := make([]*page, 0, len(s.pages))
	for p := range s.pages {
		pages = append(pages, p)
	}
	return pages
}

// summary is one row of the page's session list.
type summary struct {
	Key       string `json:"key"`
	Title     string `json:"title"`
	UpdatedAt int64  `json:"updatedAt"`
	Live      bool   `json:"live"`
	Status    string `json:"status"`
}

func (s *Server) summaries() []summary {
	list := s.sessions.List()
	out := make([]summary, len(list))
	for i, t := range list {
		status := "idle"
		if t.Responding {
			status = "responding"
		}
		out[i] = summary{Key: t.Name, Title: t.Title, UpdatedAt: t.LastOutput.UnixMilli(), Live: true, Status: status}
	}
	return out
}

// httpError is an error with an HTTP status.
type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

var errNotFound = httpError{http.StatusNotFound, "not found"}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := s.serve(w, r); err != nil {
		var he httpError
		status := http.StatusInternalServerError
		if errors.As(err, &he) {
			status = he.status
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
	}
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) error {
	if !s.allowedHost(r.Host) {
		return httpError{http.StatusForbidden, "host not allowed"}
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return s.asset(w, r)
	}
	if r.URL.Query().Get("token") != s.token {
		return httpError{http.StatusForbidden, "missing or wrong token"}
	}
	return s.api(w, r)
}

func (s *Server) api(w http.ResponseWriter, r *http.Request) error {
	switch route := r.Method + " " + r.URL.Path; {
	case route == "GET /api/config":
		s.mu.Lock()
		settings := s.settings
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"title": title, "themes": s.themes, "settings": settings, "fontFamily": fontFamily})
	case route == "GET /api/sessions":
		writeJSON(w, http.StatusOK, map[string]any{"sessions": s.summaries()})
	case route == "POST /api/settings":
		return s.changeSettings(w, r)
	case route == "GET /api/events":
		return s.events(w, r)
	case strings.HasPrefix(route, "GET /api/terminal/"):
		return s.terminal(w, r, strings.TrimPrefix(r.URL.Path, "/api/terminal/"))
	case strings.HasPrefix(route, "POST /api/sessions"):
		return httpError{http.StatusConflict, "WhatsAgent starts, names and ends its sessions itself"}
	default:
		return errNotFound
	}
	return nil
}

func (s *Server) changeSettings(w http.ResponseWriter, r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return err
	}
	s.mu.Lock()
	current, _ := json.Marshal(s.settings)
	s.mu.Unlock()
	merged, err := mergeJSON(current, body)
	if err != nil {
		return httpError{http.StatusBadRequest, err.Error()}
	}
	settings, err := s.parseSettings(merged)
	if err != nil {
		return httpError{http.StatusBadRequest, err.Error()}
	}
	saved, _ := json.Marshal(settings)
	if err := s.store.Save(string(saved)); err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = settings
	pages := s.openPages()
	s.mu.Unlock()
	for _, p := range pages {
		p.send(map[string]any{"type": "settings", "settings": settings})
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
	return nil
}

// mergeJSON lays the fields of change over the object current.
func mergeJSON(current, change []byte) ([]byte, error) {
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(current, &fields); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(change, &fields); err != nil {
		return nil, errors.New("settings must be a JSON object")
	}
	return json.Marshal(fields)
}

// parseSettings reads settings and checks them against the themes and font sizes.
func (s *Server) parseSettings(data []byte) (Settings, error) {
	var settings Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return settings, err
	}
	for _, choice := range s.choices {
		if choice.ID != settings.Theme {
			continue
		}
		if settings.Accent != nil && choice.Accents[*settings.Accent] == "" {
			return settings, fmt.Errorf("unknown accent %q", *settings.Accent)
		}
		for _, size := range fontSizes {
			if size == settings.FontSize {
				return settings, nil
			}
		}
		return settings, fmt.Errorf("unsupported font size %d", settings.FontSize)
	}
	return settings, fmt.Errorf("unknown theme %q", settings.Theme)
}

// upgrade opens a WebSocket for the page that this server served: the Origin must match the Host.
func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	s.upgrader.CheckOrigin = func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		return origin == "http://"+r.Host || origin == "https://"+r.Host
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has answered the request already.
		return nil, nil
	}
	return conn, nil
}

// events sends the page the session list now and on every change, and settings changes.
func (s *Server) events(w http.ResponseWriter, r *http.Request) error {
	conn, err := s.upgrade(w, r)
	if conn == nil {
		return err
	}
	p := &page{conn: conn}
	s.mu.Lock()
	s.pages[p] = true
	s.mu.Unlock()
	p.send(map[string]any{"type": "sessions", "sessions": s.summaries()})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			break
		}
	}
	s.mu.Lock()
	delete(s.pages, p)
	s.mu.Unlock()
	_ = conn.Close()
	return nil
}

// terminal connects a page's terminal to a session: output out, keys and sizes in.
func (s *Server) terminal(w http.ResponseWriter, r *http.Request, name string) error {
	conn, err := s.upgrade(w, r)
	if conn == nil {
		return err
	}
	defer conn.Close()
	out := make(chan []byte, 256)
	ended := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(ended) }) }
	detach, ok := s.sessions.Attach(name, size(r, "cols", 120), size(r, "rows", 40), term.Viewer{
		Data: func(p []byte) {
			select {
			case out <- append([]byte(nil), p...):
			default:
				stop() // a viewer that cannot keep up reconnects and gets the screen again
			}
		},
		Exit: stop,
	})
	if !ok {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4404, "unknown session"), time.Now().Add(time.Second))
		return nil
	}
	defer detach()
	go s.readInput(conn, name, stop)
	writeOutput(conn, out, ended)
	return nil
}

// readInput applies the page's keys and resizes until the socket closes.
func (s *Server) readInput(conn *websocket.Conn, name string, stop func()) {
	defer stop()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var m struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Cols int    `json:"cols"`
			Rows int    `json:"rows"`
		}
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "input":
			s.sessions.Input(name, m.Data)
		case "resize":
			s.sessions.Resize(name, m.Cols, m.Rows)
		}
	}
}

// writeOutput sends output as text frames, which must hold whole UTF-8 characters, until ended.
func writeOutput(conn *websocket.Conn, out <-chan []byte, ended <-chan struct{}) {
	var carry []byte
	for {
		select {
		case p := <-out:
			var text []byte
			text, carry = whole(append(carry, p...))
			if len(text) > 0 && conn.WriteMessage(websocket.TextMessage, text) != nil {
				return
			}
		case <-ended:
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4410, "session ended"), time.Now().Add(time.Second))
			return
		}
	}
}

// whole splits b into its complete UTF-8 text and an unfinished character at its end. Bytes that
// can never form a character become replacement characters.
func whole(b []byte) ([]byte, []byte) {
	cut := len(b)
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				cut = i
			}
			break
		}
	}
	return bytes.ToValidUTF8(b[:cut], []byte("�")), append([]byte(nil), b[cut:]...)
}

func size(r *http.Request, key string, fallback int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get(key)); err == nil && n > 0 && n < 10000 {
		return n
	}
	return fallback
}

var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".wasm": "application/wasm", ".woff2": "font/woff2",
	".svg": "image/svg+xml", ".json": "application/json",
}

// asset serves one embedded file. The page itself needs the token.
func (s *Server) asset(w http.ResponseWriter, r *http.Request) error {
	name := r.URL.Path
	switch name {
	case "/", "/index.html":
		if r.URL.Query().Get("token") != s.token {
			return httpError{http.StatusForbidden, "missing or wrong token"}
		}
		name = "/index.html"
	case "/logo", "/favicon.ico":
		name = "/logo.svg"
	}
	data, err := fs.ReadFile(ui, "ui"+path.Clean(name))
	if err != nil {
		return errNotFound
	}
	w.Header().Set("Content-Type", contentTypes[path.Ext(name)])
	w.Header().Set("Cache-Control", "no-store")
	_, err = w.Write(data)
	return err
}

// allowedHost reports whether a Host header names this machine by a loopback name or by one of
// the page's names, which also blocks DNS rebinding.
func (s *Server) allowedHost(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = host
	}
	return name == "127.0.0.1" || name == "localhost" || name == "::1" || slices.Contains(s.names, name)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
