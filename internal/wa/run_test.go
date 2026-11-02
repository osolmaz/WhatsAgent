package wa

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

type recorder struct {
	mu      sync.Mutex
	updates []model.Update
	names   map[string]string
	states  []string
}

func (r *recorder) OnUpdate(u model.Update) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates = append(r.updates, u)
}

func (r *recorder) OnChatName(jid, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.names[jid] = name
}

func (r *recorder) OnState(state string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, state)
}

func linked(t *testing.T) *Client {
	t.Helper()
	c, err := Open(context.Background(), filepath.Join(t.TempDir(), "session.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.cli.Store.ID = mustJID(t, "15551234567.0:1@s.whatsapp.net")
	c.cli.Store.PushName = "Owner"
	c.cli.Store.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{0}, AccountSignature: make([]byte, 64),
		AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	// Saving sets up the device's contact and LID stores, as pairing does.
	if err := c.cli.Store.Save(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

func message(t *testing.T, chat, sender string, msg *waE2E.Message) *events.Message {
	t.Helper()
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: *mustJID(t, chat), Sender: *mustJID(t, sender)},
			ID:            "ID1", PushName: "Sam", Timestamp: time.Unix(1700000000, 0),
		},
		Message: msg,
	}
}

func TestParse(t *testing.T) {
	text := message(t, "1@g.us", "200@s.whatsapp.net", &waE2E.Message{Conversation: proto.String("hi")})
	u, ok := parse(text)
	if !ok || u.Kind != model.New || u.Message.Text != "hi" || u.Message.SenderName != "Sam" || u.Message.Chat != "1@g.us" {
		t.Errorf("text = %+v, %v", u, ok)
	}
	image := message(t, "1@g.us", "200@s.whatsapp.net", &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
		Caption: proto.String("look"), Mimetype: proto.String("image/jpeg"),
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("Q1"), QuotedMessage: &waE2E.Message{Conversation: proto.String("earlier")},
			MentionedJID: []string{"300@s.whatsapp.net"}, IsForwarded: proto.Bool(true)},
	}})
	u, _ = parse(image)
	m := u.Message
	if m.MediaType != "image" || m.Text != "look" || m.ReplyTo != "Q1" || m.Quote != "earlier" || !m.Forwarded || len(m.Raw) == 0 {
		t.Errorf("image = %+v", m)
	}
	if _, ok := m.Mentions["300@s.whatsapp.net"]; !ok {
		t.Errorf("mentions = %v", m.Mentions)
	}
	reaction := message(t, "1@g.us", "200@s.whatsapp.net", &waE2E.Message{ReactionMessage: &waE2E.ReactionMessage{
		Key: &waCommon.MessageKey{ID: proto.String("R1")}, Text: proto.String("👍")}})
	if u, _ := parse(reaction); u.Message.ReactionTo != "R1" || u.Message.Reaction != "👍" {
		t.Errorf("reaction = %+v", u.Message)
	}
	revoke := message(t, "1@g.us", "200@s.whatsapp.net", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_REVOKE.Enum(), Key: &waCommon.MessageKey{ID: proto.String("D1")}}})
	if u, _ := parse(revoke); u.Kind != model.Delete || u.Message.ID != "D1" {
		t.Errorf("revoke = %+v", u)
	}
	edit := message(t, "1@g.us", "200@s.whatsapp.net", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(), Key: &waCommon.MessageKey{ID: proto.String("E1")},
		EditedMessage: &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("fixed")}}}})
	if u, _ := parse(edit); u.Kind != model.Edit || u.Message.ID != "E1" || u.Message.Text != "fixed" {
		t.Errorf("edit = %+v", u)
	}
	other := message(t, "1@g.us", "200@s.whatsapp.net", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum()}})
	if _, ok := parse(other); ok {
		t.Error("an ephemeral setting change was stored")
	}
	if _, ok := parse(message(t, "1@g.us", "200@s.whatsapp.net", &waE2E.Message{})); ok {
		t.Error("an empty message was stored")
	}
	for kind, msg := range map[string]*waE2E.Message{
		"video":    {VideoMessage: &waE2E.VideoMessage{}},
		"audio":    {AudioMessage: &waE2E.AudioMessage{}},
		"document": {DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("a.pdf")}},
		"sticker":  {StickerMessage: &waE2E.StickerMessage{}},
		"location": {LocationMessage: &waE2E.LocationMessage{Name: proto.String("Home")}},
		"contact":  {ContactMessage: &waE2E.ContactMessage{DisplayName: proto.String("Kim")}},
		"poll":     {PollCreationMessageV3: &waE2E.PollCreationMessage{Name: proto.String("Lunch?")}},
	} {
		if u, ok := parse(message(t, "1@g.us", "2@s.whatsapp.net", msg)); !ok || u.Message.MediaType != kind {
			t.Errorf("%s = %+v", kind, u.Message)
		}
	}
}

func TestDispatch(t *testing.T) {
	ctx := context.Background()
	c := linked(t)
	lid := *mustJID(t, "777@lid")
	pn := *mustJID(t, "4915550000@s.whatsapp.net")
	if err := c.cli.Store.LIDs.PutLIDMapping(ctx, lid, pn); err != nil {
		t.Fatal(err)
	}
	if err := c.cli.Store.Contacts.PutContactName(ctx, pn, "Kim", "Kim Park"); err != nil {
		t.Fatal(err)
	}
	r := &recorder{names: map[string]string{}}
	c.dispatch(ctx, r, message(t, "777@lid", "777@lid", &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: proto.String("hey @4915550000"), ContextInfo: &waE2E.ContextInfo{MentionedJID: []string{"777@lid"}}}}))
	own := message(t, "1@g.us", "15551234567@s.whatsapp.net", &waE2E.Message{Conversation: proto.String("mine")})
	own.Info.IsFromMe, own.Info.PushName = true, ""
	c.dispatch(ctx, r, own)
	nameless := message(t, "1@g.us", "4915550000@s.whatsapp.net", &waE2E.Message{Conversation: proto.String("x")})
	nameless.Info.PushName = ""
	c.dispatch(ctx, r, nameless)
	c.dispatch(ctx, r, message(t, "1@g.us", "2@s.whatsapp.net", &waE2E.Message{}))
	if len(r.updates) != 3 {
		t.Fatalf("updates = %+v", r.updates)
	}
	first := r.updates[0].Message
	if first.Chat != pn.String() || first.Sender != pn.String() || first.RawSender != "777@lid" || first.Mentions["777@lid"] != "Kim Park" {
		t.Errorf("LID message = %+v", first)
	}
	if r.updates[1].Message.SenderName != "Owner" || r.updates[2].Message.SenderName != "Kim Park" {
		t.Errorf("names = %q, %q", r.updates[1].Message.SenderName, r.updates[2].Message.SenderName)
	}

	c.dispatch(ctx, r, &events.GroupInfo{JID: *mustJID(t, "9@g.us"), Name: &types.GroupName{Name: "Team"}})
	c.dispatch(ctx, r, &events.GroupInfo{JID: *mustJID(t, "9@g.us")})
	webMsg := &waWeb.WebMessageInfo{Key: &waCommon.MessageKey{RemoteJID: proto.String("8@g.us"), ID: proto.String("H1"),
		Participant: proto.String("2@s.whatsapp.net")}, MessageTimestamp: proto.Uint64(1700000000),
		Message: &waE2E.Message{Conversation: proto.String("old")}}
	before := len(r.updates)
	c.dispatch(ctx, r, &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{
		{ID: proto.String("8@g.us"), Name: proto.String("Old group"), Messages: []*waHistorySync.HistorySyncMsg{{Message: webMsg}}},
		{ID: proto.String("not a jid@@")},
	}}})
	if r.names["9@g.us"] != "Team" || r.names["8@g.us"] != "Old group" {
		t.Errorf("names = %v", r.names)
	}
	// The history gives chat names only; its messages are never passed on.
	if len(r.updates) != before {
		t.Errorf("history passed on %+v", r.updates[before:])
	}
	c.dispatch(ctx, r, &events.Connected{})
	c.dispatch(ctx, r, &events.LoggedOut{})
	c.dispatch(ctx, r, &events.StreamReplaced{})
	c.dispatch(ctx, r, &events.Receipt{})
	time.Sleep(50 * time.Millisecond)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.states) != 3 || r.states[0] != "connected" || r.states[1] != "logged out" {
		t.Errorf("states = %v", r.states)
	}
	if c.ChatName(ctx, "4915550000@s.whatsapp.net") != "Kim Park" || c.name(ctx, "5@s.whatsapp.net") != "5" {
		t.Error("ChatName is wrong")
	}
}

func TestSendWithoutConnection(t *testing.T) {
	ctx := context.Background()
	c := linked(t)
	if c.SelfChat() != "15551234567@s.whatsapp.net" || c.OwnName() != "Owner" || c.NewID() == "" {
		t.Error("account details are wrong")
	}
	if err := c.Send(ctx, "1@g.us", c.NewID(), model.Outgoing{Text: "x"}); err == nil {
		t.Error("Send without a connection gave no error")
	}
	if err := c.Send(ctx, "no-at-sign", "id", model.Outgoing{Text: "x"}); err == nil {
		t.Error("Send to a bad chat gave no error")
	}
	if err := c.Edit(ctx, "1@g.us", "ID", "new"); err == nil {
		t.Error("Edit without a connection gave no error")
	}
	if err := c.Revoke(ctx, "1@g.us", "2@s.whatsapp.net", "ID"); err == nil {
		t.Error("Revoke without a connection gave no error")
	}
	for _, err := range []error{c.Edit(ctx, "bad", "ID", "x"), c.Revoke(ctx, "bad", "", "ID"), c.Revoke(ctx, "1@g.us", "bad", "ID")} {
		if err == nil {
			t.Error("a bad address gave no error")
		}
	}
	if err := c.React(ctx, "1@g.us", "2@s.whatsapp.net", "ID", "👀"); err == nil {
		t.Error("React without a connection gave no error")
	}
	if err := c.React(ctx, "bad", "", "ID", ""); err == nil {
		t.Error("React to a bad chat gave no error")
	}
	if _, err := c.Download(ctx, []byte{0xff, 0xff}); err == nil {
		t.Error("Download of bad bytes gave no error")
	}
	raw, _ := proto.Marshal(&waE2E.Message{Conversation: proto.String("no media")})
	if _, err := c.Download(ctx, raw); err == nil {
		t.Error("Download of a text message gave no error")
	}
	unlinked, _ := Open(ctx, filepath.Join(t.TempDir(), "s.db"), nil)
	defer unlinked.Close()
	if err := unlinked.Run(ctx, &recorder{}); !errors.Is(err, ErrNoAccount) {
		t.Errorf("Run unlinked = %v", err)
	}
}
