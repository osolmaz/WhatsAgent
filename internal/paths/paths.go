// Package paths decides where WhatsAgent keeps its files on Linux and macOS.
package paths

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// maxSocketPath is the longest Unix socket path that both Linux (108 bytes) and macOS
// (104 bytes) accept, leaving room for the terminating NUL byte.
const maxSocketPath = 103

// ErrUnsupportedOS is returned for an operating system other than Linux or macOS.
var ErrUnsupportedOS = errors.New("unsupported operating system")

// ErrNoHome is returned when the home folder is unknown.
var ErrNoHome = errors.New("home directory is not set")

// Env is the part of the process environment that decides WhatsAgent's folders.
type Env struct {
	GOOS   string
	Home   string
	Getenv func(key string) string
}

// FromOS reads the environment of the running process.
func FromOS() (Env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Env{}, fmt.Errorf("%w: %w", ErrNoHome, err)
	}
	return Env{GOOS: runtime.GOOS, Home: home, Getenv: os.Getenv}, nil
}

// Dirs holds the folders WhatsAgent uses.
type Dirs struct {
	Data   string
	Config string
}

// Resolve returns WhatsAgent's folders for env.
func Resolve(env Env) (Dirs, error) {
	if env.Home == "" {
		return Dirs{}, ErrNoHome
	}
	switch env.GOOS {
	case "linux":
		return Dirs{
			Data:   filepath.Join(xdg(env, "XDG_STATE_HOME", filepath.Join(".local", "state")), "whatsagent"),
			Config: filepath.Join(xdg(env, "XDG_CONFIG_HOME", ".config"), "whatsagent"),
		}, nil
	case "darwin":
		dir := filepath.Join(env.Home, "Library", "Application Support", "WhatsAgent")
		return Dirs{Data: dir, Config: dir}, nil
	default:
		return Dirs{}, fmt.Errorf("%w: %s", ErrUnsupportedOS, env.GOOS)
	}
}

// xdg returns the XDG base folder that key names, or fallback under the home folder. The XDG
// Base Directory specification says to ignore a value that is not an absolute path.
func xdg(env Env, key, fallback string) string {
	if env.Getenv != nil {
		if value := env.Getenv(key); filepath.IsAbs(value) {
			return value
		}
	}
	return filepath.Join(env.Home, fallback)
}

// SessionDB is whatsmeow's session store, which holds the linked-device keys.
func (d Dirs) SessionDB() string {
	return filepath.Join(d.Data, "session.db")
}

// StateDB is the WhatsAgent database with messages, the event log, routes, sessions and cursors.
func (d Dirs) StateDB() string {
	return filepath.Join(d.Data, "whatsagent.db")
}

// Socket returns the daemon's Unix socket path. It fails when the path is too long for macOS.
func (d Dirs) Socket() (string, error) {
	path := filepath.Join(d.Data, "d.sock")
	if len(path) > maxSocketPath {
		return "", fmt.Errorf("socket path %q is %d bytes, over the %d-byte limit", path, len(path), maxSocketPath)
	}
	return path, nil
}
