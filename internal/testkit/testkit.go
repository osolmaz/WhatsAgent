// Package testkit runs a WhatsAgent daemon API on a temporary socket, with a fake WhatsApp
// connection, for the tests of the packages that talk to the daemon.
package testkit

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/api"
	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/daemon"
	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/store"
)

// SelfChat is the fake owner's own chat.
const SelfChat = "100@s.whatsapp.net"

// ShortDir returns a new folder under /tmp for Unix sockets, which must stay under 104 bytes. On
// macOS the folder of t.TempDir is too long for that.
func ShortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "wa")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// Sent is one message, edit or deletion that the fake connection sent. File is the name of the
// file that a media message carries, and Text is its caption. Kind is "edit", "revoke", "typing"
// or "read" for an edit, a deletion, a typing state or a read receipt, and empty for a message.
type Sent struct {
	Chat, ID, Text, File, Kind string
	Out                        model.Outgoing
}

// WhatsApp is a fake WhatsApp connection.
type WhatsApp struct {
	mu   sync.Mutex
	n    int
	Sent chan Sent
}

// SelfChat implements core.WhatsApp.
func (f *WhatsApp) SelfChat() string { return SelfChat }

// OwnName implements core.WhatsApp.
func (f *WhatsApp) OwnName() string { return "Owner" }

// NewID implements core.WhatsApp.
func (f *WhatsApp) NewID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	return fmt.Sprintf("S%d", f.n)
}

// Send implements core.WhatsApp.
func (f *WhatsApp) Send(_ context.Context, chat, id string, out model.Outgoing) error {
	s := Sent{Chat: chat, ID: id, Text: out.Text, Out: out}
	if out.Media != nil {
		s.File = out.Media.Filename
	}
	f.Sent <- s
	return nil
}

// Edit implements core.WhatsApp.
func (f *WhatsApp) Edit(_ context.Context, chat, id, text string) error {
	f.Sent <- Sent{Chat: chat, ID: id, Text: text, Kind: "edit"}
	return nil
}

// Typing implements core.WhatsApp.
func (f *WhatsApp) Typing(_ context.Context, chat string) error {
	f.Sent <- Sent{Chat: chat, Kind: "typing"}
	return nil
}

// MarkRead implements core.WhatsApp.
func (f *WhatsApp) MarkRead(_ context.Context, chat, _, id string, _ time.Time) error {
	f.Sent <- Sent{Chat: chat, ID: id, Kind: "read"}
	return nil
}

// GroupInfo implements core.WhatsApp with one member, Sam.
func (f *WhatsApp) GroupInfo(_ context.Context, chat string) (model.Group, error) {
	return model.Group{JID: chat, Name: "Team", Members: []model.Member{{JID: "200@s.whatsapp.net", Name: "Sam"}}}, nil
}

// Groups implements core.WhatsApp with one group, Team.
func (f *WhatsApp) Groups(context.Context) (map[string]string, error) {
	return map[string]string{"team@g.us": "Team"}, nil
}

// CreateGroup implements core.WhatsApp.
func (f *WhatsApp) CreateGroup(_ context.Context, name string, _ []string) (string, error) {
	f.Sent <- Sent{Text: name, Kind: "create"}
	return "new@g.us", nil
}

// ChangeGroup implements core.WhatsApp.
func (f *WhatsApp) ChangeGroup(_ context.Context, chat string, change model.GroupChange) (string, error) {
	f.Sent <- Sent{Chat: chat, Text: change.Text, Kind: change.Action}
	return change.Action + " done", nil
}

// JoinGroup implements core.WhatsApp.
func (f *WhatsApp) JoinGroup(_ context.Context, link string) (string, error) {
	f.Sent <- Sent{Text: link, Kind: "join"}
	return "joined@g.us", nil
}

// Contacts implements core.WhatsApp with one contact, Sam.
func (f *WhatsApp) Contacts(context.Context, string) ([]model.Contact, error) {
	return []model.Contact{{JID: "200@s.whatsapp.net", Name: "Sam"}}, nil
}

// ContactInfo implements core.WhatsApp.
func (f *WhatsApp) ContactInfo(_ context.Context, person string) (model.Contact, error) {
	return model.Contact{JID: person, Name: "Sam", About: "Hey there"}, nil
}

// SetProfile implements core.WhatsApp.
func (f *WhatsApp) SetProfile(_ context.Context, p model.Profile) error {
	f.Sent <- Sent{Text: p.Name, Kind: "profile"}
	return nil
}

// Revoke implements core.WhatsApp.
func (f *WhatsApp) Revoke(_ context.Context, chat, _, id string) error {
	f.Sent <- Sent{Chat: chat, ID: id, Kind: "revoke"}
	return nil
}

// Upload implements core.WhatsApp. Every file is a document.
func (f *WhatsApp) Upload(_ context.Context, file model.File) (model.Media, error) {
	return model.Media{Type: "document", MimeType: "application/octet-stream", Filename: file.Name,
		Size: len(file.Data), Raw: file.Data}, nil
}

// React implements core.WhatsApp.
func (f *WhatsApp) React(context.Context, string, string, string, string) error { return nil }

// Download implements core.WhatsApp.
func (f *WhatsApp) Download(context.Context, []byte) ([]byte, error) { return []byte("media"), nil }

// ChatName implements core.WhatsApp.
func (f *WhatsApp) ChatName(_ context.Context, jid string) string { return jid }

// Daemon is a running test daemon.
type Daemon struct {
	t      *testing.T
	Core   *core.Core
	Store  *store.Store
	WA     *WhatsApp
	Socket string
	n      int
	// Opens counts the requests to open the browser page, and NoDesktop makes them fail.
	Opens     atomic.Int32
	NoDesktop atomic.Bool
}

// Start runs a daemon API on socket until the test ends.
func Start(t *testing.T, socket string) *Daemon {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	wa := &WhatsApp{Sent: make(chan Sent, 100)}
	c := core.New(core.Config{Store: st, WhatsApp: wa, MediaDir: t.TempDir(), Location: time.UTC,
		StatusDelay: 10 * time.Millisecond, Heartbeat: 50 * time.Millisecond})
	c.Start(ctx)
	listener, err := daemon.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{t: t, Core: c, Store: st, WA: wa, Socket: socket}
	openPage := func() bool {
		d.Opens.Add(1)
		return !d.NoDesktop.Load()
	}
	server := &http.Server{Handler: api.NewServer(c, openPage), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Close()
		cancel()
		if !c.Wait(5 * time.Second) {
			t.Error("the core's goroutines did not end")
		}
		st.Close()
	})
	c.OnState("connected")
	return d
}

// Receive delivers a live message from WhatsApp.
func (d *Daemon) Receive(chat, text string, fromMe bool) model.Message {
	d.n++
	m := model.Message{Chat: chat, ID: fmt.Sprintf("M%d", d.n), Sender: "200@s.whatsapp.net", SenderName: "Sam",
		Time: time.Now().UTC(), FromMe: fromMe, Text: text}
	d.Core.OnUpdate(model.Update{Kind: model.New, Message: m})
	return m
}

// Keep makes chat kept, so callers that are not run agents may read it.
func (d *Daemon) Keep(chat string) {
	d.t.Helper()
	if _, _, err := d.Store.Keep(context.Background(), chat); err != nil {
		d.t.Fatal(err)
	}
}

// Phone runs a command as the owner writes it in the self-chat, the only source of changes, and
// waits for WhatsAgent's reply that contains want.
func (d *Daemon) Phone(line, want string) {
	d.t.Helper()
	d.Receive(SelfChat, line, true)
	d.Expect(want)
}

// Expect waits for a sent message that contains want.
func (d *Daemon) Expect(want string) Sent {
	d.t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case s := <-d.WA.Sent:
			if strings.Contains(s.Text, want) {
				return s
			}
		case <-timeout:
			d.t.Fatalf("no message containing %q was sent", want)
		}
	}
}
