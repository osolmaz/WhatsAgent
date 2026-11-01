// Package browser opens a page in the default browser on Linux, macOS and Windows.
package browser

import (
	"os"
	"os/exec"
	"runtime"
)

// Command returns the command that opens url in the default browser on goos, or nil when the
// machine has no desktop to show it, for example over SSH.
func Command(goos string, getenv func(string) string, url string) []string {
	switch goos {
	case "darwin":
		return []string{"open", url}
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", url}
	case "linux":
		if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
			return nil
		}
		return []string{"xdg-open", url}
	}
	return nil
}

// Open opens url in the default browser of this machine, without waiting for the browser, and
// reports whether it could start it.
func Open(url string) bool {
	args := Command(runtime.GOOS, os.Getenv, url)
	return args != nil && exec.Command(args[0], args[1:]...).Start() == nil
}
