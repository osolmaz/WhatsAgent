// Package service installs the WhatsAgent daemon as a systemd user service on Linux and as a
// launchd LaunchAgent on macOS.
package service

import (
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
)

// Label is the launchd label and the base of the systemd unit name.
const Label = "io.github.osolmaz.whatsagent"

// Spec describes the service.
type Spec struct {
	// Bin is the absolute path of the whatsagent binary.
	Bin string
	// Path is the PATH the daemon gets, so it finds claude and codex.
	Path string
	// LogFile is where launchd writes the daemon's output.
	LogFile string
	// Args are extra arguments for `whatsagent daemon`.
	Args []string
}

// Runner runs a command such as systemctl.
type Runner func(name string, args ...string) error

// Unit returns the systemd user unit.
func Unit(s Spec) string {
	return fmt.Sprintf(`[Unit]
Description=WhatsAgent, a bridge between WhatsApp and coding agent sessions
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=%s
Restart=on-failure
RestartSec=5
Environment=PATH=%s

[Install]
WantedBy=default.target
`, strings.Join(command(s, quote), " "), s.Path)
}

// command is the daemon's command line, with each part passed through enc.
func command(s Spec, enc func(string) string) []string {
	parts := []string{enc(s.Bin), enc("daemon")}
	for _, arg := range s.Args {
		parts = append(parts, enc(arg))
	}
	return parts
}

// quote quotes a path for systemd when it has spaces.
func quote(path string) string {
	if strings.ContainsAny(path, " \t") {
		return `"` + path + `"`
	}
	return path
}

// Plist returns the launchd LaunchAgent.
func Plist(s Spec) string {
	e := html.EscapeString
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array>%s</array>
  <key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string></dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, Label, strings.Join(command(s, func(a string) string { return "<string>" + e(a) + "</string>" }), ""),
		e(s.Path), e(s.LogFile), e(s.LogFile))
}

// File returns where the service file goes on goos.
func File(goos, home, configHome string) (string, error) {
	switch goos {
	case "linux":
		return filepath.Join(configHome, "systemd", "user", "whatsagent.service"), nil
	case "darwin":
		return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
	}
	return "", fmt.Errorf("no service support for %s", goos)
}

// Install writes the service file for goos to path and starts the service.
func Install(goos, path string, s Spec, uid int, run Runner) error {
	content := Unit(s)
	if goos == "darwin" {
		content = Plist(s)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	if goos == "darwin" {
		_ = run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, Label))
		return run("launchctl", "bootstrap", fmt.Sprintf("gui/%d", uid), path)
	}
	if err := run("systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	return run("systemctl", "--user", "enable", "--now", "whatsagent.service")
}

// Uninstall stops the service and removes its file.
func Uninstall(goos, path string, uid int, run Runner) error {
	if goos == "darwin" {
		_ = run("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, Label))
	} else {
		_ = run("systemctl", "--user", "disable", "--now", "whatsagent.service")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if goos == "darwin" {
		return nil
	}
	return run("systemctl", "--user", "daemon-reload")
}
