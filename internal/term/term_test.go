package term

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// collector gathers a viewer's output.
type collector struct {
	mu     sync.Mutex
	text   strings.Builder
	exited bool
}

func (c *collector) viewer() Viewer {
	return Viewer{
		Data: func(p []byte) {
			c.mu.Lock()
			c.text.Write(p)
			c.mu.Unlock()
		},
		Exit: func() {
			c.mu.Lock()
			c.exited = true
			c.mu.Unlock()
		},
	}
}

func (c *collector) wait(t *testing.T, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		c.mu.Lock()
		got := c.text.String()
		c.mu.Unlock()
		if strings.Contains(got, want) {
			return
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t.Fatalf("no %q in %q", want, c.text.String())
}

func shell(name, script string, answers ...Answer) Spec {
	return Spec{Name: name, Title: name + " title", Command: "/bin/sh", Args: []string{"-c", script},
		Env: append(os.Environ(), "PS1="), Answers: answers}
}

func TestSessionLifecycle(t *testing.T) {
	h := NewHost()
	changes := make(chan struct{}, 10)
	h.Changed = func() { changes <- struct{}{} }
	ctx := context.Background()
	script := `printf 'Do you \033[1mtrust\033[0m\033[1Cthe files? '; read a; echo "answer:$a"; ` +
		`read b; echo "typed:$b"; stty size; read c`
	ended, err := h.Start(ctx, shell("one", script, Answer{Prompt: "Do you trust the files?", Keys: "yes\r"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Start(ctx, shell("one", "true")); !errors.Is(err, ErrRunning) {
		t.Errorf("a second session with the same name: %v", err)
	}
	c := &collector{}
	detach, ok := h.Attach("one", 100, 30, c.viewer())
	if !ok {
		t.Fatal("Attach found no session")
	}
	c.wait(t, "answer:yes")
	h.Input("one", "hello\r")
	c.wait(t, "typed:hello")
	c.wait(t, "30 100")
	if list := h.List(); len(list) != 1 || list[0].Name != "one" || list[0].Title != "one title" || !list[0].Responding {
		t.Errorf("List = %+v", list)
	}
	if h.PID("one") <= 0 || h.PID("none") != 0 {
		t.Errorf("PID = %d, %d", h.PID("one"), h.PID("none"))
	}
	late := &collector{}
	if _, ok := h.Attach("one", 0, 0, late.viewer()); !ok {
		t.Fatal("a second viewer could not attach")
	}
	late.wait(t, "typed:hello") // the replay
	detach()
	h.Stop("one")
	<-ended
	if h.Running("one") {
		t.Error("the stopped session still runs")
	}
	late.mu.Lock()
	exited := late.exited
	late.mu.Unlock()
	if !exited {
		t.Error("the viewer heard no exit")
	}
	if _, ok := h.Attach("one", 80, 24, Viewer{}); ok {
		t.Error("Attach found an ended session")
	}
	for deadline := time.Now().Add(5 * time.Second); len(changes) < 2 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if len(changes) < 2 {
		t.Errorf("Changed ran %d times", len(changes))
	}
	h.Input("none", "x")
	h.Resize("none", 1, 1)
	h.Stop("none")
}

func TestStartFailsAndContextEnds(t *testing.T) {
	h := NewHost()
	if _, err := h.Start(context.Background(), Spec{Name: "x", Command: "/no/such/program"}); err == nil {
		t.Error("a missing program gave no error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended, err := h.Start(ctx, shell("sleeper", "sleep 30"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the session outlived its context")
	}
}

func TestSquashAndKeepLast(t *testing.T) {
	if got := squash("\x1b[1mI am\x1b[0m using\x1b[1Cthis \x1b]0;title\x07Here"); got != "iamusingthishere" {
		t.Errorf("squash = %q", got)
	}
	if got := string(keepLast([]byte("abcdef"), 3)); got != "def" {
		t.Errorf("keepLast = %q", got)
	}
	if got := string(keepLast([]byte("ab"), 3)); got != "ab" {
		t.Errorf("keepLast short = %q", got)
	}
}
