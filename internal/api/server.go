// Package api is the daemon's local HTTP API on its Unix socket, and the client that the CLI and
// the WhatsAgent MCP server use to reach it.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/model"
)

// Frame is one line of a session stream or a watch stream.
type Frame struct {
	// Type is "hello", "events", "heartbeat" or "verdict".
	Type      string        `json:"type"`
	Token     string        `json:"token,omitempty"`
	Name      string        `json:"name,omitempty"`
	Events    []model.Event `json:"events,omitempty"`
	RequestID string        `json:"request_id,omitempty"`
	Allow     bool          `json:"allow,omitempty"`
}

// ToolRequest is the body of a tool call.
type ToolRequest struct {
	Token string          `json:"token,omitempty"`
	Tool  string          `json:"tool"`
	Args  json.RawMessage `json:"args,omitempty"`
	// As names the agent session that an owner call acts for, so that its sends get the agent
	// prefix. Only /v1/owner reads it.
	As string `json:"as,omitempty"`
}

// ToolResponse is the answer to a tool call.
type ToolResponse struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// PermissionRequest is a permission prompt that a session's harness relays.
type PermissionRequest struct {
	Token        string `json:"token"`
	RequestID    string `json:"request_id"`
	ToolName     string `json:"tool_name"`
	Description  string `json:"description"`
	InputPreview string `json:"input_preview"`
}

// Server serves the API for one core.
type Server struct {
	core  *core.Core
	tools map[string]tool
	mux   *http.ServeMux
}

// Opened is the answer to a request to open the browser page.
type Opened struct {
	Opened bool `json:"opened"`
}

// NewServer returns the API server for c. openPage opens the browser page in this machine's
// default browser and reports whether it could; the page's link never leaves the daemon this way,
// because the link is a way to see messages.
func NewServer(c *core.Core, openPage func() bool) *Server {
	s := &Server{core: c, tools: tools(), mux: http.NewServeMux()}
	s.mux.HandleFunc("POST /v1/web", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, Opened{openPage()}, nil)
	})
	s.mux.HandleFunc("GET /v1/session", s.session)
	s.mux.HandleFunc("GET /v1/watch", s.watch)
	s.mux.HandleFunc("POST /v1/tool", s.tool)
	s.mux.HandleFunc("POST /v1/owner", s.owner)
	s.mux.HandleFunc("POST /v1/permission", s.permission)
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// stream writes frames as JSON lines. It is the Pusher of a session or watch stream.
type stream struct {
	mu sync.Mutex
	w  http.ResponseWriter
}

func newStream(w http.ResponseWriter) *stream {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	return &stream{w: w}
}

func (st *stream) write(f Frame) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := json.NewEncoder(st.w).Encode(f); err != nil {
		return err
	}
	return http.NewResponseController(st.w).Flush()
}

func (st *stream) Push(_ context.Context, events []model.Event) error {
	if events == nil {
		return st.write(Frame{Type: "heartbeat"})
	}
	return st.write(Frame{Type: "events", Events: events})
}

func (st *stream) Verdict(_ context.Context, requestID string, allow bool) error {
	return st.write(Frame{Type: "verdict", RequestID: requestID, Allow: allow})
}

// session checks a session process in, keeps it checked in while the request stays open, and
// pushes its routed events when the query asks for pushes.
func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	info := model.Session{Name: q.Get("name"), Harness: q.Get("harness"), Folder: q.Get("folder")}
	token, name, err := s.core.CheckIn(r.Context(), info)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer s.core.CheckOut(token)
	st := newStream(w)
	if err := st.write(Frame{Type: "hello", Token: token, Name: name}); err != nil {
		return
	}
	if push := q.Get("push"); push != "" {
		_ = s.core.Subscribe(r.Context(), token, push, st)
		return
	}
	heartbeat(r.Context(), st)
}

func heartbeat(ctx context.Context, st *stream) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if st.Push(ctx, nil) != nil {
				return
			}
		}
	}
}

// watch streams the events of a session's routes plus the chats in the query.
func (s *Server) watch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	since, err := strconv.ParseInt(q.Get("since"), 10, 64)
	if err != nil {
		since = -1
	}
	st := newStream(w)
	if err := s.core.Watch(r.Context(), q.Get("name"), q["chat"], since, st); err != nil {
		_ = st.write(Frame{Type: "error", Name: err.Error()})
	}
}

// maxRequest bounds a request body: a file of model.MaxFileSize in base64, plus room for the
// rest of the request.
const maxRequest = model.MaxFileSize/3*4 + 1<<20

// decode reads a JSON request body of at most maxRequest bytes into v, and answers with the
// error when it cannot.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequest)).Decode(v)
	if err != nil {
		reply(w, nil, err)
	}
	return err == nil
}

func (s *Server) tool(w http.ResponseWriter, r *http.Request) {
	var req ToolRequest
	if !decode(w, r, &req) {
		return
	}
	caller, err := s.core.Caller(req.Token)
	if err != nil {
		reply(w, nil, err)
		return
	}
	s.run(w, r.Context(), caller, req)
}

func (s *Server) owner(w http.ResponseWriter, r *http.Request) {
	var req ToolRequest
	if !decode(w, r, &req) {
		return
	}
	caller := core.Owner
	if req.As != "" {
		caller = core.As(req.As)
	}
	s.run(w, r.Context(), caller, req)
}

func (s *Server) run(w http.ResponseWriter, ctx context.Context, caller core.Caller, req ToolRequest) {
	t, ok := s.tools[req.Tool]
	if !ok || (t.ownerOnly && caller != core.Owner) {
		reply(w, nil, fmt.Errorf("unknown tool %q", req.Tool))
		return
	}
	result, err := t.call(ctx, s.core, caller, req.Args)
	reply(w, result, err)
}

func reply(w http.ResponseWriter, result any, err error) {
	w.Header().Set("Content-Type", "application/json")
	resp := ToolResponse{}
	if err != nil {
		resp.Error = err.Error()
		w.WriteHeader(http.StatusBadRequest)
	} else {
		resp.Result = encode(result)
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(resp)
}

// encode marshals v without escaping <, > and &, which chat names often contain.
func encode(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSpace(b.Bytes())
}

func (s *Server) permission(w http.ResponseWriter, r *http.Request) {
	var req PermissionRequest
	if !decode(w, r, &req) {
		return
	}
	caller, err := s.core.Caller(req.Token)
	if err != nil {
		reply(w, nil, err)
		return
	}
	s.core.RelayPermission(caller, req.RequestID, req.ToolName, req.Description, req.InputPreview)
	reply(w, "relayed", nil)
}

// ErrDaemon is returned when the daemon is not running.
var ErrDaemon = errors.New("the WhatsAgent daemon is not running: start it with `whatsagent daemon` or `whatsagent service install`")
