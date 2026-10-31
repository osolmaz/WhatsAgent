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

// mutate4go-manifest-begin
// {"version":1,"tested_at":"2026-10-31T23:35:30+08:00","module_hash":"a113134b992a22011d5d19712a4e6042e61d9ba1bcabdd9ce5979b9ef49787f2","functions":[{"id":"func/FromOS","name":"FromOS","line":30,"end_line":36,"hash":"9c739b5b19a143f587dbeb3f2200a201dea1398fd6a922b99d228bb0609a4d75"},{"id":"func/Resolve","name":"Resolve","line":45,"end_line":61,"hash":"1b57d687b91dd5c0ac50116ee8f777a0f935f6624e24626f38f575f57f89c586"},{"id":"func/xdg","name":"xdg","line":65,"end_line":72,"hash":"5fbb559ff433f898db84700150bf114b94a903d85cf698a3935d76b824d801f8"},{"id":"func/Dirs.SessionDB","name":"Dirs.SessionDB","line":75,"end_line":77,"hash":"bfdfb6ecf5d5a4ef51585d64279ec0583336a044e6bdb67687b5e6624eb8c238"},{"id":"func/Dirs.StateDB","name":"Dirs.StateDB","line":80,"end_line":82,"hash":"31836b2f1805a94faa2c6e4fb791cc07d0bc13c95a62534de6245fe535e27d2d"},{"id":"func/Dirs.Socket","name":"Dirs.Socket","line":85,"end_line":91,"hash":"de1fd9c610780b0f0ec206e51b75b0b7d685de64a80705dd04d55720b4033bd4"}]}
// mutate4go-manifest-end
