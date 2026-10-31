// Package sqlitedb opens SQLite databases with the settings WhatsAgent uses for every database.
package sqlitedb

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // registers the CGO-free "sqlite" driver
)

// fileMode keeps database files readable by the owner only, because they hold WhatsApp keys
// and messages.
const fileMode = 0o600

// ErrBadPath is returned for a database path that is relative or would change the SQLite URI.
var ErrBadPath = errors.New("bad database path")

// pragmas run on every connection: foreign keys on, WAL journal, a bounded wait for locks, and
// full fsync, because the session store holds keys that must survive a crash.
var pragmas = []string{
	"foreign_keys(1)",
	"journal_mode(WAL)",
	"busy_timeout(10000)",
	"synchronous(FULL)",
}

// Open opens or creates the SQLite database at the absolute path, creating its folder with
// owner-only permissions.
func Open(path string) (*sql.DB, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("%w: %q", ErrBadPath, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create database folder: %w", err)
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := restrict(path); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// OpenReadOnly opens the existing SQLite database at the absolute path without changing it.
func OpenReadOnly(path string) (*sql.DB, error) {
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("%w: %q", ErrBadPath, path)
	}
	query := url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(10000)"}}
	db, err := sql.Open("sqlite", fileURI(path, query))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return db, nil
}

// dsn builds the modernc.org/sqlite file URI for path with the WhatsAgent pragmas.
func dsn(path string) string {
	return fileURI(path, url.Values{"_pragma": pragmas})
}

func fileURI(path string, query url.Values) string {
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

// restrict sets owner-only permissions on the database file and its WAL companions.
func restrict(path string) error {
	for _, file := range []string{path, path + "-wal", path + "-shm"} {
		err := os.Chmod(file, fileMode)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("restrict %s: %w", file, err)
		}
	}
	return nil
}
