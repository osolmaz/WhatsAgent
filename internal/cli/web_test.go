package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/osolmaz/WhatsAgent/internal/paths"
)

func TestWebCommand(t *testing.T) {
	env, _ := daemonEnv(t)
	ran := seams(t)
	out, err := runCtx(context.Background(), env, "", "web", "--print")
	if err != nil || out != "http://127.0.0.1:1/?token=test\n" {
		t.Errorf("web --print = %q, %v", out, err)
	}
	// Without a desktop, the address is printed instead of opened.
	if out, err := runCtx(context.Background(), env, "", "web"); err != nil || out != "http://127.0.0.1:1/?token=test\n" || len(*ran) != 0 {
		t.Errorf("web without a desktop = %q, %v, ran %v", out, err, *ran)
	}
	desktop := env
	desktop.Getenv = func(k string) string {
		if k == "DISPLAY" {
			return ":0"
		}
		return env.Getenv(k)
	}
	out, err = runCtx(context.Background(), desktop, "", "web")
	if err != nil || !strings.HasPrefix(out, "Opened http://127.0.0.1:1/") || len(*ran) != 1 || !strings.HasPrefix((*ran)[0], "xdg-open ") {
		t.Errorf("web = %q, %v, ran %v", out, err, *ran)
	}
	if _, err := runCtx(context.Background(), paths.Env{GOOS: "linux", Home: t.TempDir()}, "", "web"); err == nil {
		t.Error("web without a daemon gave no error")
	}
}

func TestBrowserCommand(t *testing.T) {
	none := func(string) string { return "" }
	for goos, want := range map[string]string{
		"darwin":  "open u",
		"windows": "rundll32 url.dll,FileProtocolHandler u",
		"linux":   "",
		"plan9":   "",
	} {
		if got := strings.Join(browserCommand(goos, none, "u"), " "); got != want {
			t.Errorf("browserCommand(%s) = %q, want %q", goos, got, want)
		}
	}
	wayland := func(k string) string {
		if k == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	if got := strings.Join(browserCommand("linux", wayland, "u"), " "); got != "xdg-open u" {
		t.Errorf("browserCommand on Wayland = %q", got)
	}
}
