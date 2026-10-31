package wa

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/osolmaz/WhatsAgent/internal/sqlitedb"
)

var (
	// ErrDestinationExists is returned when WhatsAgent already has a session store.
	ErrDestinationExists = errors.New("WhatsAgent already has a session store")
	// ErrNotLinked is returned when the store to import holds no linked device.
	ErrNotLinked = errors.New("the session store to import holds no linked device")
)

// ImportStore copies an existing whatsmeow session store at src to dest, so WhatsAgent can use
// a login without pairing again. It opens src read-only, writes a consistent snapshot with
// VACUUM INTO, and refuses to overwrite an existing dest.
func ImportStore(src, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%w: %s", ErrDestinationExists, dest)
	}
	db, err := sqlitedb.OpenReadOnly(src)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := requireDevice(db); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("create session folder: %w", err)
	}
	if _, err := db.Exec("VACUUM INTO ?", dest); err != nil {
		return fmt.Errorf("copy session store: %w", err)
	}
	return os.Chmod(dest, 0o600)
}

// requireDevice checks that a whatsmeow session store holds a linked device.
func requireDevice(db *sql.DB) error {
	var jid string
	err := db.QueryRow("SELECT jid FROM whatsmeow_device LIMIT 1").Scan(&jid)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotLinked
	}
	if err != nil {
		return fmt.Errorf("read session store: %w", err)
	}
	return nil
}
