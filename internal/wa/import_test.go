package wa

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeStore creates a session store file whose device table holds the given JIDs.
func fakeStore(t *testing.T, jids ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE whatsmeow_device (jid TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	for _, jid := range jids {
		if _, err := db.Exec("INSERT INTO whatsmeow_device (jid) VALUES (?)", jid); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestImportStore(t *testing.T) {
	src := fakeStore(t, "15551234567.0:1@s.whatsapp.net")
	dest := filepath.Join(t.TempDir(), "whatsagent", "session.db")
	if err := ImportStore(src, dest); err != nil {
		t.Fatalf("ImportStore: %v", err)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("dest mode = %o", info.Mode().Perm())
	}
	db, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var jid string
	if err := db.QueryRow("SELECT jid FROM whatsmeow_device").Scan(&jid); err != nil || jid != "15551234567.0:1@s.whatsapp.net" {
		t.Fatalf("copied jid = %q, %v", jid, err)
	}
	if err := ImportStore(src, dest); !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("second import err = %v, want ErrDestinationExists", err)
	}
}

func TestImportStoreFailures(t *testing.T) {
	if err := ImportStore(fakeStore(t), filepath.Join(t.TempDir(), "s.db")); !errors.Is(err, ErrNotLinked) {
		t.Errorf("store without a device: err = %v, want ErrNotLinked", err)
	}
	if err := ImportStore(filepath.Join(t.TempDir(), "missing.db"), filepath.Join(t.TempDir(), "s.db")); err == nil {
		t.Error("missing store: no error")
	}
	empty := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ImportStore(empty, filepath.Join(t.TempDir(), "s.db")); err == nil {
		t.Error("store without the device table: no error")
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	src := fakeStore(t, "15551234567.0:1@s.whatsapp.net")
	if err := ImportStore(src, filepath.Join(blocker, "s.db")); err == nil {
		t.Error("dest under a regular file: no error")
	}
}
