package cli

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/osolmaz/WhatsAgent/internal/paths"
)

func run(t *testing.T, env paths.Env, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	root := NewRoot(&out, slog.New(slog.NewTextHandler(io.Discard, nil)), env)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

func linuxEnv() paths.Env {
	return paths.Env{GOOS: "linux", Home: "/home/sam", Getenv: func(string) string { return "" }}
}

func TestVersion(t *testing.T) {
	out, err := run(t, linuxEnv(), "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if out != version+"\n" {
		t.Fatalf("version printed %q", out)
	}
}

func TestPaths(t *testing.T) {
	out, err := run(t, linuxEnv(), "paths")
	if err != nil {
		t.Fatalf("paths: %v", err)
	}
	for _, want := range []string{
		"data:     /home/sam/.local/state/whatsagent\n",
		"config:   /home/sam/.config/whatsagent\n",
		"session:  /home/sam/.local/state/whatsagent/session.db\n",
		"state:    /home/sam/.local/state/whatsagent/whatsagent.db\n",
		"socket:   /home/sam/.local/state/whatsagent/d.sock\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("paths output %q is missing %q", out, want)
		}
	}
}

func TestPathsErrors(t *testing.T) {
	if _, err := run(t, paths.Env{GOOS: "plan9", Home: "/home/sam"}, "paths"); err == nil {
		t.Fatal("paths accepted an unsupported OS")
	}
	long := paths.Env{GOOS: "darwin", Home: "/" + strings.Repeat("h", 80)}
	if _, err := run(t, long, "paths"); err == nil {
		t.Fatal("paths accepted a socket path over the limit")
	}
}

func TestMainVersion(t *testing.T) {
	t.Setenv("HOME", "/home/sam")
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("Main(version) = %d, stderr %q", code, stderr.String())
	}
	if stdout.String() != version+"\n" {
		t.Fatalf("Main(version) printed %q", stdout.String())
	}
}

func TestMainErrors(t *testing.T) {
	t.Setenv("HOME", "/home/sam")
	var stdout, stderr bytes.Buffer
	if code := Main([]string{"no-such-command"}, &stdout, &stderr); code != 1 {
		t.Fatalf("Main(no-such-command) = %d", code)
	}
	if !strings.HasPrefix(stderr.String(), "whatsagent: ") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	t.Setenv("HOME", "")
	stderr.Reset()
	if code := Main([]string{"version"}, &stdout, &stderr); code != 1 {
		t.Fatalf("Main without HOME = %d", code)
	}
}
