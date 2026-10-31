package cli

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/osolmaz/WhatsAgent/internal/paths"
	"github.com/osolmaz/WhatsAgent/internal/wa"
)

func tempEnv(t *testing.T) paths.Env {
	t.Helper()
	return paths.Env{GOOS: "linux", Home: t.TempDir(), Getenv: func(string) string { return "" }}
}

func TestQRPrinter(t *testing.T) {
	var out bytes.Buffer
	show, err := qrPrinter(&out, "text")
	if err != nil {
		t.Fatal(err)
	}
	show("2@abc")
	if out.String() != "2@abc\n" {
		t.Fatalf("text QR printed %q", out.String())
	}

	out.Reset()
	show, err = qrPrinter(&out, "terminal")
	if err != nil {
		t.Fatal(err)
	}
	show("2@abc")
	if !strings.HasPrefix(out.String(), "Scan this code") || strings.Count(out.String(), "\n") < 5 {
		t.Fatalf("terminal QR printed %q", out.String())
	}

	if _, err := qrPrinter(&out, "png"); err == nil {
		t.Fatal("qrPrinter accepted an unknown format")
	}
}

func TestLinkFlagErrors(t *testing.T) {
	if _, err := run(t, tempEnv(t), "link", "--qr-format", "png"); err == nil || !strings.Contains(err.Error(), "--qr-format") {
		t.Fatalf("bad --qr-format: err = %v", err)
	}
	if _, err := run(t, paths.Env{GOOS: "plan9", Home: "/home/sam"}, "link"); err == nil {
		t.Fatal("link accepted an unsupported OS")
	}
}

func TestLinkNewStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dirs, err := paths.Resolve(tempEnv(t))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := linkNew(ctx, io.Discard, dirs, "", func(string) {}, log); err == nil {
		t.Fatal("linkNew succeeded with a cancelled context")
	}
	bad := paths.Dirs{Data: "relative"}
	if err := linkNew(ctx, io.Discard, bad, "", func(string) {}, log); err == nil {
		t.Fatal("linkNew accepted a relative session path")
	}
}

// partialStore creates a session store file with a device row but not the full whatsmeow
// schema, so the import succeeds and whatsmeow's schema upgrade then fails.
func partialStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		"CREATE TABLE whatsmeow_device (jid TEXT PRIMARY KEY)",
		"INSERT INTO whatsmeow_device (jid) VALUES ('15551234567.0:1@s.whatsapp.net')",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLinkImport(t *testing.T) {
	env := tempEnv(t)
	src := partialStore(t)
	_, err := run(t, env, "link", "--import", src)
	if err == nil || !strings.Contains(err.Error(), "open the imported session store") {
		t.Fatalf("err = %v, want the partial store to fail whatsmeow's schema upgrade", err)
	}
	dirs, _ := paths.Resolve(env)
	if _, statErr := os.Stat(dirs.SessionDB()); statErr != nil {
		t.Fatalf("imported session store is missing: %v", statErr)
	}
	if _, err := run(t, env, "link", "--import", src); !errors.Is(err, wa.ErrDestinationExists) {
		t.Fatalf("second import: err = %v, want ErrDestinationExists", err)
	}
}

func TestLinkImportMissingStore(t *testing.T) {
	if _, err := run(t, tempEnv(t), "link", "--import", filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("import from a missing store succeeded")
	}
}
