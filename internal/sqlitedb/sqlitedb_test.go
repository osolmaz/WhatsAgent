package sqlitedb

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "with space", "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	checks := map[string]string{
		"PRAGMA foreign_keys":  "1",
		"PRAGMA journal_mode":  "wal",
		"PRAGMA busy_timeout":  "10000",
		"PRAGMA synchronous":   "2",
		"PRAGMA secure_delete": "1",
		"PRAGMA temp_store":    "2",
	}
	for query, want := range checks {
		var got string
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if got != want {
			t.Errorf("%s = %s, want %s", query, got, want)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != fileMode {
		t.Errorf("file mode = %o, want %o", mode, fileMode)
	}
	folder, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat folder: %v", err)
	}
	if mode := folder.Mode().Perm(); mode != 0o700 {
		t.Errorf("folder mode = %o, want 700", mode)
	}
}

// TestOpenMemory checks that a memory database keeps its tables across queries, keeps temporary
// storage in memory, and is separate from every other memory database.
func TestOpenMemory(t *testing.T) {
	db, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('x')"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		var v string
		if err := db.QueryRow("SELECT v FROM t").Scan(&v); err != nil || v != "x" {
			t.Fatalf("read %d = %q, %v", i, v, err)
		}
	}
	var temp string
	if err := db.QueryRow("PRAGMA temp_store").Scan(&temp); err != nil || temp != "2" {
		t.Errorf("temp_store = %s, %v", temp, err)
	}
	other, err := OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec("SELECT v FROM t"); err == nil {
		t.Error("a second memory database sees the first one's table")
	}
}

func TestOpenRejectsBadPaths(t *testing.T) {
	for _, path := range []string{"relative.db", "/tmp/a?mode=memory", "/tmp/a#b"} {
		if _, err := Open(path); !errors.Is(err, ErrBadPath) {
			t.Errorf("Open(%q) err = %v, want ErrBadPath", path, err)
		}
	}
}

func TestOpenFailures(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(blocker, "test.db")); err == nil {
		t.Error("Open succeeded under a regular file")
	}
	if _, err := Open(dir); err == nil {
		t.Error("Open succeeded on a folder")
	}
}

func TestOpenReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('x')"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()
	var v string
	if err := ro.QueryRow("SELECT v FROM t").Scan(&v); err != nil || v != "x" {
		t.Fatalf("read = %q, %v", v, err)
	}
	if _, err := ro.Exec("INSERT INTO t VALUES ('y')"); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
}

func TestOpenReadOnlyFailures(t *testing.T) {
	if _, err := OpenReadOnly("relative.db"); !errors.Is(err, ErrBadPath) {
		t.Errorf("relative path: err = %v", err)
	}
	if _, err := OpenReadOnly(filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Error("missing file: no error")
	}
}

func TestRestrictMissingFiles(t *testing.T) {
	if err := restrict(filepath.Join(t.TempDir(), "missing.db")); err != nil {
		t.Fatalf("restrict: %v", err)
	}
}
