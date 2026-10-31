package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func mustJID(t *testing.T, s string) *types.JID {
	t.Helper()
	jid, err := types.ParseJID(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return &jid
}
