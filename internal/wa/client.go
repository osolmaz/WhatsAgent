// Package wa wraps whatsmeow, the Go library for the WhatsApp multi-device protocol. whatsmeow
// types stay inside this package.
package wa

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"

	"github.com/osolmaz/WhatsAgent/internal/sqlitedb"
)

// Client is one linked-device session backed by a whatsmeow session store.
type Client struct {
	cli       *whatsmeow.Client
	container *sqlstore.Container
	db        *sql.DB
}

// Open opens the whatsmeow session store at path, creating it when it is missing, and prepares
// a client for the first device in it, or for a new device when the store has none.
func Open(ctx context.Context, path string, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	db, err := sqlitedb.Open(path)
	if err != nil {
		return nil, err
	}
	container := sqlstore.NewWithDB(db, "sqlite3", newLogger(log, "store"))
	if err := container.Upgrade(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("upgrade session store: %w", err)
	}
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("read session store: %w", err)
	}
	cli := whatsmeow.NewClient(device, newLogger(log, "client"))
	// Recovery snapshots must return every app-state mutation, so contacts and chat settings
	// can be rebuilt from them.
	cli.EmitAppStateEventsOnFullSync = true
	// Keep recently sent messages so whatsmeow can answer retry receipts after a restart.
	// Without this, other devices of a recipient can show "Waiting for this message" forever.
	cli.UseRetryMessageStore = true
	// Let whatsmeow ask the phone again for messages that failed to decrypt.
	cli.AutomaticMessageRerequestFromPhone = true
	return &Client{cli: cli, container: container, db: db}, nil
}

// Close disconnects from WhatsApp and closes the session store.
func (c *Client) Close() error {
	c.cli.Disconnect()
	return c.db.Close()
}

// Linked reports whether the session store holds a linked device.
func (c *Client) Linked() bool {
	return c.cli.Store.ID != nil
}

// OwnJID returns the phone-number JID of the linked account, such as
// "15551234567@s.whatsapp.net", or "" before linking.
func (c *Client) OwnJID() string {
	if !c.Linked() {
		return ""
	}
	return c.cli.Store.ID.ToNonAD().String()
}
