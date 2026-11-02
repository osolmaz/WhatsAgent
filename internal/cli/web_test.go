package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/osolmaz/WhatsAgent/internal/paths"
)

// TestWebCommand checks that `whatsagent web` asks the daemon to open the page and never prints
// the page's link.
func TestWebCommand(t *testing.T) {
	env, d := daemonEnv(t)
	out, err := runCtx(context.Background(), env, "", "web")
	if err != nil || out != "Opened the browser page on this computer.\n" || d.Opens.Load() != 1 {
		t.Errorf("web = %q, %v, opens %d", out, err, d.Opens.Load())
	}
	d.NoDesktop.Store(true)
	if _, err := runCtx(context.Background(), env, "", "web"); err == nil || !strings.Contains(err.Error(), "/web in the self-chat") {
		t.Errorf("web without a desktop: %v", err)
	}
	if _, err := runCtx(context.Background(), env, "", "web", "--print"); err == nil {
		t.Error("web --print still exists")
	}
	if _, err := runCtx(context.Background(), paths.Env{GOOS: "linux", Home: t.TempDir()}, "", "web"); err == nil {
		t.Error("web without a daemon gave no error")
	}
}
