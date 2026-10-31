package paths

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func envWith(goos string, vars map[string]string) Env {
	return Env{
		GOOS: goos,
		Home: "/home/sam",
		Getenv: func(key string) string {
			return vars[key]
		},
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name string
		env  Env
		want Dirs
	}{
		{
			name: "linux defaults",
			env:  envWith("linux", nil),
			want: Dirs{Data: "/home/sam/.local/state/whatsagent", Config: "/home/sam/.config/whatsagent"},
		},
		{
			name: "linux XDG variables",
			env:  envWith("linux", map[string]string{"XDG_STATE_HOME": "/srv/state", "XDG_CONFIG_HOME": "/srv/config"}),
			want: Dirs{Data: "/srv/state/whatsagent", Config: "/srv/config/whatsagent"},
		},
		{
			name: "linux ignores relative XDG variables",
			env:  envWith("linux", map[string]string{"XDG_STATE_HOME": "state", "XDG_CONFIG_HOME": "config"}),
			want: Dirs{Data: "/home/sam/.local/state/whatsagent", Config: "/home/sam/.config/whatsagent"},
		},
		{
			name: "linux without Getenv",
			env:  Env{GOOS: "linux", Home: "/home/sam"},
			want: Dirs{Data: "/home/sam/.local/state/whatsagent", Config: "/home/sam/.config/whatsagent"},
		},
		{
			name: "macOS",
			env:  envWith("darwin", map[string]string{"XDG_STATE_HOME": "/srv/state"}),
			want: Dirs{
				Data:   "/home/sam/Library/Application Support/WhatsAgent",
				Config: "/home/sam/Library/Application Support/WhatsAgent",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.env)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != tt.want {
				t.Fatalf("Resolve = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestResolveErrors(t *testing.T) {
	if _, err := Resolve(envWith("windows", nil)); !errors.Is(err, ErrUnsupportedOS) {
		t.Fatalf("windows: err = %v, want ErrUnsupportedOS", err)
	}
	if _, err := Resolve(Env{GOOS: "linux"}); !errors.Is(err, ErrNoHome) {
		t.Fatalf("no home: err = %v, want ErrNoHome", err)
	}
}

func TestFiles(t *testing.T) {
	dirs := Dirs{Data: "/data"}
	if got := dirs.SessionDB(); got != "/data/session.db" {
		t.Fatalf("SessionDB = %q", got)
	}
	if got := dirs.StateDB(); got != "/data/whatsagent.db" {
		t.Fatalf("StateDB = %q", got)
	}
}

func TestSocket(t *testing.T) {
	got, err := Dirs{Data: "/data"}.Socket()
	if err != nil || got != "/data/d.sock" {
		t.Fatalf("Socket = %q, %v", got, err)
	}
	long := Dirs{Data: "/" + strings.Repeat("a", maxSocketPath)}
	if _, err := long.Socket(); err == nil {
		t.Fatal("Socket accepted a path over the limit")
	}
	edge := Dirs{Data: "/" + strings.Repeat("a", maxSocketPath-len("/d.sock")-1)}
	if path, err := edge.Socket(); err != nil || len(path) != maxSocketPath {
		t.Fatalf("Socket at the limit = %q (%d bytes), %v", path, len(path), err)
	}
}

func TestFromOS(t *testing.T) {
	t.Setenv("HOME", "/home/sam")
	env, err := FromOS()
	if err != nil {
		t.Fatalf("FromOS: %v", err)
	}
	if env.Home != "/home/sam" || env.GOOS == "" || env.Getenv == nil {
		t.Fatalf("FromOS = %+v", env)
	}
	if !filepath.IsAbs(env.Home) {
		t.Fatalf("home %q is not absolute", env.Home)
	}
}

func TestFromOSWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	if _, err := FromOS(); !errors.Is(err, ErrNoHome) {
		t.Fatalf("err = %v, want ErrNoHome", err)
	}
}
