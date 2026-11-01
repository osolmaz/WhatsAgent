package browser

import (
	"runtime"
	"strings"
	"testing"
)

func TestCommand(t *testing.T) {
	none := func(string) string { return "" }
	for goos, want := range map[string]string{
		"darwin":  "open u",
		"windows": "rundll32 url.dll,FileProtocolHandler u",
		"linux":   "",
		"plan9":   "",
	} {
		if got := strings.Join(Command(goos, none, "u"), " "); got != want {
			t.Errorf("Command(%s) = %q, want %q", goos, got, want)
		}
	}
	wayland := func(k string) string {
		if k == "WAYLAND_DISPLAY" {
			return "wayland-0"
		}
		return ""
	}
	if got := strings.Join(Command("linux", wayland, "u"), " "); got != "xdg-open u" {
		t.Errorf("Command on Wayland = %q", got)
	}
}

func TestOpenWithoutDesktop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux can be kept from opening a real browser in a test")
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if Open("http://127.0.0.1:1/") {
		t.Error("Open found a desktop without a display")
	}
}
