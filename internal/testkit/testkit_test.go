package testkit

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/osolmaz/WhatsAgent/internal/core"
)

func TestDaemon(t *testing.T) {
	d := Start(t, filepath.Join(ShortDir(t), "d.sock"))
	d.Expect("Welcome to WhatsAgent")
	d.Receive(SelfChat, "/rules", true)
	d.Expect("No send rules")
	if _, err := d.Core.Send(context.Background(), core.Owner, "g@g.us", "hi"); err != nil {
		t.Fatal(err)
	}
	if s := d.Expect("hi"); s.Chat != "g@g.us" || s.ID == "" {
		t.Errorf("sent %+v", s)
	}
	if d.WA.OwnName() != "Owner" || d.WA.ChatName(context.Background(), "x") != "x" {
		t.Error("fake account details are wrong")
	}
	if err := d.WA.React(context.Background(), "", "", "", ""); err != nil {
		t.Error(err)
	}
	if data, _ := d.WA.Download(context.Background(), nil); string(data) != "media" {
		t.Errorf("download = %q", data)
	}
}
