package wa

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
)

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestOpenNewStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.db")
	var logs bytes.Buffer
	for range 2 { // the second open checks that an existing store opens again
		c, err := Open(context.Background(), path, testLogger(&logs))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if c.Linked() || c.OwnJID() != "" {
			t.Fatalf("new store reports a linked device: %q", c.OwnJID())
		}
		if err := c.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

func TestOpenBadPath(t *testing.T) {
	if _, err := Open(context.Background(), "relative.db", slog.Default()); err == nil {
		t.Fatal("Open accepted a relative path")
	}
}

func TestLinkRefusesLinkedStore(t *testing.T) {
	c, err := Open(context.Background(), filepath.Join(t.TempDir(), "session.db"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.cli.Store.ID = mustJID(t, "15551234567.0:1@s.whatsapp.net")
	if !c.Linked() || c.OwnJID() != "15551234567@s.whatsapp.net" {
		t.Fatalf("Linked = %v, OwnJID = %q", c.Linked(), c.OwnJID())
	}
	if err := c.Link(context.Background(), LinkOptions{}); !errors.Is(err, ErrAlreadyLinked) {
		t.Fatalf("Link err = %v, want ErrAlreadyLinked", err)
	}
}

func TestLinkStopsWithContext(t *testing.T) {
	c, err := Open(context.Background(), filepath.Join(t.TempDir(), "session.db"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = c.Link(ctx, LinkOptions{})
	if err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("Link err = %v, want a connect error from the cancelled context", err)
	}
}

func TestLogger(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(testLogger(&buf), "client").Sub("socket")
	log.Debugf("d %d", 1)
	log.Infof("i %d", 2)
	log.Warnf("w %d", 3)
	log.Errorf("e %d", 4)
	out := buf.String()
	for _, want := range []string{"level=DEBUG msg=\"d 1\"", "level=INFO msg=\"i 2\"", "level=WARN msg=\"w 3\"", "level=ERROR msg=\"e 4\"", "module=client", "sub=socket"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output is missing %q:\n%s", want, out)
		}
	}
}

// feed returns a channel that yields items and then stays open.
func feed(items ...whatsmeow.QRChannelItem) <-chan whatsmeow.QRChannelItem {
	ch := make(chan whatsmeow.QRChannelItem, len(items))
	for _, item := range items {
		ch <- item
	}
	return ch
}

func code(c string) whatsmeow.QRChannelItem {
	return whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventCode, Code: c}
}

func noPair(context.Context, string) (string, error) {
	return "", errors.New("pair must not be called")
}

func TestPairingWithQR(t *testing.T) {
	var shown []string
	opts := LinkOptions{OnQR: func(c string) { shown = append(shown, c) }}
	err := waitForPairing(context.Background(), feed(code("a"), code("b"), whatsmeow.QRChannelSuccess), opts, noPair)
	if err != nil {
		t.Fatalf("waitForPairing: %v", err)
	}
	if strings.Join(shown, ",") != "a,b" {
		t.Fatalf("shown codes = %v", shown)
	}
	if err := waitForPairing(context.Background(), feed(code("a"), whatsmeow.QRChannelSuccess), LinkOptions{}, noPair); err != nil {
		t.Fatalf("waitForPairing without OnQR: %v", err)
	}
}

func TestPairingWithPhone(t *testing.T) {
	calls := 0
	pair := func(_ context.Context, phone string) (string, error) {
		calls++
		if phone != "+15551234567" {
			t.Errorf("phone = %q", phone)
		}
		return "ABCD-EFGH", nil
	}
	var got string
	opts := LinkOptions{Phone: "+15551234567", OnPairCode: func(c string) { got = c }}
	err := waitForPairing(context.Background(), feed(code("a"), code("b"), whatsmeow.QRChannelSuccess), opts, pair)
	if err != nil {
		t.Fatalf("waitForPairing: %v", err)
	}
	if calls != 1 || got != "ABCD-EFGH" {
		t.Fatalf("pair calls = %d, code = %q", calls, got)
	}
	opts.OnPairCode = nil
	if err := waitForPairing(context.Background(), feed(code("a"), whatsmeow.QRChannelSuccess), opts, pair); err != nil {
		t.Fatalf("waitForPairing without OnPairCode: %v", err)
	}
}

func TestPairingPhoneFailure(t *testing.T) {
	pair := func(context.Context, string) (string, error) { return "", errors.New("rate limited") }
	err := waitForPairing(context.Background(), feed(code("a")), LinkOptions{Phone: "+1"}, pair)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v", err)
	}
}

func TestPairingFailures(t *testing.T) {
	cause := errors.New("stream closed")
	tests := []struct {
		item whatsmeow.QRChannelItem
		want string
	}{
		{whatsmeow.QRChannelTimeout, "in time"},
		{whatsmeow.QRChannelClientOutdated, "client version"},
		{whatsmeow.QRChannelScannedWithoutMultidevice, "multi-device"},
		{whatsmeow.QRChannelErrUnexpectedEvent, "out of order"},
		{whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventPasskeyRequest}, "passkey check"},
		{whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventPasskeyResponse}, "passkey confirmation"},
		{whatsmeow.QRChannelItem{Event: whatsmeow.QRChannelEventError, Error: cause}, "stream closed"},
		{whatsmeow.QRChannelItem{Event: "something-new"}, `"something-new"`},
	}
	for _, tt := range tests {
		err := waitForPairing(context.Background(), feed(tt.item), LinkOptions{}, noPair)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("event %q: err = %v, want it to mention %q", tt.item.Event, err, tt.want)
		}
	}
}

func TestPairingEnds(t *testing.T) {
	closed := make(chan whatsmeow.QRChannelItem)
	close(closed)
	if err := waitForPairing(context.Background(), closed, LinkOptions{}, noPair); err == nil {
		t.Error("closed channel: no error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForPairing(ctx, make(chan whatsmeow.QRChannelItem), LinkOptions{}, noPair); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: err = %v", err)
	}
}
