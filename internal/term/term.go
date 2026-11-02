// Package term runs the agents that WhatsAgent starts in pseudo-terminals, so their own
// terminal interface keeps working and any number of viewers, such as browser tabs, can watch
// and type into them. A session keeps running when no viewer is attached.
package term

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

const (
	// replayBytes is how much recent output a new viewer gets, so it can draw the screen.
	replayBytes = 512 * 1024
	// busyFor is how long after its last output a session counts as responding.
	busyFor = 2 * time.Second
	// answerWindow is how much recent output the startup answers are matched against.
	answerWindow = 8 * 1024
	// answerDelay is how long an answer waits after its question appears.
	answerDelay = 500 * time.Millisecond
	// retryRead is how long reading pauses after a failed read while the process runs.
	retryRead   = 10 * time.Millisecond
	defaultCols = 120
	defaultRows = 40
)

// Answer types Keys into a session once, the first time its output contains Prompt. It
// answers a startup question that the agent's interface asks, such as a trust prompt.
type Answer struct {
	Prompt string
	Keys   string
}

// Spec describes one session.
type Spec struct {
	// Name is the session's key, unique among running sessions.
	Name string
	// Title is what a viewer shows for the session.
	Title   string
	Command string
	Args    []string
	Dir     string
	// Env is the complete environment of the process.
	Env     []string
	Answers []Answer
}

// Summary describes a running session.
type Summary struct {
	Name       string
	Title      string
	Started    time.Time
	LastOutput time.Time
	// Responding is true while the session produced output within the last two seconds.
	Responding bool
}

// Viewer receives a session's output.
type Viewer struct {
	Data func(p []byte)
	// Exit is called once when the session's process ends.
	Exit func()
}

// Host runs sessions.
type Host struct {
	// Changed, when set, is called after a session starts or ends.
	Changed func()

	mu       sync.Mutex
	sessions map[string]*session
}

// NewHost returns an empty host.
func NewHost() *Host {
	return &Host{sessions: map[string]*session{}}
}

type session struct {
	spec    Spec
	pty     *os.File
	cmd     *exec.Cmd
	started time.Time
	ended   chan struct{}

	mu       sync.Mutex
	output   []byte
	recent   []byte
	answered []bool
	last     time.Time
	viewers  map[int]Viewer
	next     int
	done     bool
}

// ErrRunning is returned when a session with the same name already runs.
var ErrRunning = errors.New("a session with this name is already running")

// Start starts a session. It ends when its process exits or ctx ends, and the returned channel
// closes then.
func (h *Host) Start(ctx context.Context, spec Spec) (<-chan struct{}, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.sessions[spec.Name]; ok {
		return nil, fmt.Errorf("%w: %s", ErrRunning, spec.Name)
	}
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Dir, cmd.Env = spec.Dir, spec.Env
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: defaultCols, Rows: defaultRows})
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", spec.Name, err)
	}
	s := &session{spec: spec, pty: f, cmd: cmd, started: time.Now(), last: time.Now(), ended: make(chan struct{}),
		answered: make([]bool, len(spec.Answers)), viewers: map[int]Viewer{}}
	h.sessions[spec.Name] = s
	go h.run(s)
	h.changed()
	return s.ended, nil
}

func (h *Host) changed() {
	if h.Changed != nil {
		go h.Changed()
	}
}

// run copies the session's output until its process ends.
func (h *Host) run(s *session) {
	exited := make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(exited)
	}()
	s.copyOutput(exited)
	_ = s.pty.Close()
	h.mu.Lock()
	if h.sessions[s.spec.Name] == s {
		delete(h.sessions, s.spec.Name)
	}
	h.mu.Unlock()
	s.end()
	close(s.ended)
	h.changed()
}

// copyOutput reads the session's output until a read fails after the process exited. Linux
// reports an I/O error on the terminal's master side while no process holds the other side
// open, which also happens for a moment while a process starts, so a failed read before the
// exit only pauses the reading. After the exit, reads return the remaining output first.
func (s *session) copyOutput(exited <-chan struct{}) {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.write(buf[:n])
		}
		if err == nil {
			continue
		}
		select {
		case <-exited:
			return
		case <-time.After(retryRead):
		}
	}
}

// write records output, answers startup questions, and passes the output to the viewers.
func (s *session) write(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = time.Now()
	s.output = keepLast(append(s.output, p...), replayBytes)
	s.recent = keepLast(append(s.recent, p...), answerWindow)
	s.answer()
	for _, v := range s.viewers {
		v.Data(p)
	}
}

// answer types each startup answer whose prompt appeared, after answerDelay, because an interface
// can draw a question before it reads keys. The caller holds s.mu.
func (s *session) answer() {
	text := squash(string(s.recent))
	for i, a := range s.spec.Answers {
		if !s.answered[i] && strings.Contains(text, squash(a.Prompt)) {
			s.answered[i] = true
			s.recent = nil
			keys := a.Keys
			time.AfterFunc(answerDelay, func() { _, _ = s.pty.WriteString(keys) })
			return
		}
	}
}

var escapes = regexp.MustCompile(`\x1b(\[[0-9;?<>=]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

// squash removes terminal escape sequences and all white space and lowers the case, because a
// terminal interface can draw the spaces of a sentence as cursor moves.
func squash(s string) string {
	s = escapes.ReplaceAllString(s, "")
	return strings.ToLower(strings.Join(strings.Fields(s), ""))
}

func keepLast(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return append([]byte(nil), b[len(b)-n:]...)
}

func (s *session) end() {
	s.mu.Lock()
	viewers := s.viewers
	s.viewers, s.done = map[int]Viewer{}, true
	s.mu.Unlock()
	for _, v := range viewers {
		v.Exit()
	}
}

func (h *Host) get(name string) (*session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[name]
	return s, ok
}

// Attach replays a session's recent output to v and then streams new output, after resizing the
// terminal so the interface redraws. It returns a function that detaches v, and false when no
// session has that name.
func (h *Host) Attach(name string, cols, rows int, v Viewer) (func(), bool) {
	s, ok := h.get(name)
	if !ok {
		return nil, false
	}
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return nil, false
	}
	id := s.next
	s.next++
	s.viewers[id] = v
	v.Data(append([]byte(nil), s.output...))
	s.mu.Unlock()
	h.Resize(name, cols, rows)
	return func() {
		s.mu.Lock()
		delete(s.viewers, id)
		s.mu.Unlock()
	}, true
}

// Input types data into a session.
func (h *Host) Input(name, data string) {
	if s, ok := h.get(name); ok {
		_, _ = s.pty.WriteString(data)
	}
}

// Resize sets a session's terminal size.
func (h *Host) Resize(name string, cols, rows int) {
	if s, ok := h.get(name); ok && cols > 0 && rows > 0 {
		_ = pty.Setsize(s.pty, &pty.Winsize{Cols: uint16(min(cols, 1000)), Rows: uint16(min(rows, 1000))})
	}
}

// Stop ends a session's process.
func (h *Host) Stop(name string) {
	if s, ok := h.get(name); ok && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

// Running reports whether a session with this name runs.
func (h *Host) Running(name string) bool {
	_, ok := h.get(name)
	return ok
}

// PID returns the process ID of the running session with this name, or 0.
func (h *Host) PID(name string) int {
	if s, ok := h.get(name); ok && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}

// List returns the running sessions, sorted by name.
func (h *Host) List() []Summary {
	h.mu.Lock()
	sessions := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.mu.Unlock()
	out := make([]Summary, len(sessions))
	for i, s := range sessions {
		s.mu.Lock()
		out[i] = Summary{Name: s.spec.Name, Title: s.spec.Title, Started: s.started, LastOutput: s.last,
			Responding: time.Since(s.last) < busyFor}
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
