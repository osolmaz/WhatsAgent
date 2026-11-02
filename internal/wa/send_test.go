package wa

import (
	"testing"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"google.golang.org/protobuf/proto"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

func encoded(t *testing.T, msg *waE2E.Message) []byte {
	t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func build(t *testing.T, out model.Outgoing) *waE2E.Message {
	t.Helper()
	msg, err := outgoingMessage(out)
	if err != nil {
		t.Fatal(err)
	}
	return msg
}

func TestOutgoingText(t *testing.T) {
	if msg := build(t, model.Outgoing{Text: "hi"}); msg.GetConversation() != "hi" || msg.ExtendedTextMessage != nil {
		t.Errorf("plain text = %v", msg)
	}
	mention := build(t, model.Outgoing{Text: "hi @15551234567", Mentions: []string{"15551234567@s.whatsapp.net"}})
	if x := mention.GetExtendedTextMessage(); x.GetText() != "hi @15551234567" ||
		x.GetContextInfo().GetMentionedJID()[0] != "15551234567@s.whatsapp.net" {
		t.Errorf("mention = %v", mention)
	}
	question := model.Message{ID: "Q1", Sender: "15551234567@s.whatsapp.net", RawSender: "123@lid", Text: "when?"}
	reply := build(t, model.Outgoing{Text: "at 5", ReplyTo: &question}).GetExtendedTextMessage().GetContextInfo()
	if reply.GetStanzaID() != "Q1" || reply.GetParticipant() != "123@lid" || reply.GetQuotedMessage().GetConversation() != "when?" {
		t.Errorf("reply context = %v", reply)
	}
}

func TestOutgoingMedia(t *testing.T) {
	photo := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Mimetype: proto.String("image/png")},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: []byte("secret")}}
	question := model.Message{ID: "Q1", Sender: "1@s.whatsapp.net", MediaType: "image", Raw: encoded(t, photo)}

	// A photo with a caption answers a photo, which WhatsApp shows quoted without its secret.
	out := model.Outgoing{Text: "this one", Media: &model.Media{Raw: encoded(t, &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}})},
		ReplyTo: &question}
	img := build(t, out).GetImageMessage()
	if img.GetCaption() != "this one" || img.GetContextInfo().GetStanzaID() != "Q1" ||
		img.GetContextInfo().GetQuotedMessage().GetImageMessage().GetMimetype() != "image/png" ||
		img.GetContextInfo().GetQuotedMessage().GetMessageContextInfo() != nil {
		t.Errorf("photo reply = %v", img)
	}

	// A document with a caption goes inside documentWithCaptionMessage, with its context.
	doc := encoded(t, &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{FileName: proto.String("r.pdf")}})
	msg := build(t, model.Outgoing{Text: "the report @15551234567", Media: &model.Media{Raw: doc},
		Mentions: []string{"15551234567@s.whatsapp.net"}})
	inner := msg.GetDocumentWithCaptionMessage().GetMessage().GetDocumentMessage()
	if inner.GetCaption() != "the report @15551234567" || len(inner.GetContextInfo().GetMentionedJID()) != 1 || msg.DocumentMessage != nil {
		t.Errorf("document with a caption = %v", msg)
	}
	if plain := build(t, model.Outgoing{Media: &model.Media{Raw: doc}}); plain.GetDocumentMessage().GetFileName() != "r.pdf" {
		t.Errorf("document without a caption = %v", plain)
	}
	if _, err := outgoingMessage(model.Outgoing{Media: &model.Media{Raw: []byte{0xff, 0xff}}}); err == nil {
		t.Error("bad media bytes gave no error")
	}
}

func TestOutgoingForward(t *testing.T) {
	text := build(t, model.Outgoing{Forward: &model.Message{ID: "M1", Text: "news"}})
	if x := text.GetExtendedTextMessage(); x.GetText() != "news" || !x.GetContextInfo().GetIsForwarded() ||
		x.GetContextInfo().GetForwardingScore() != 1 {
		t.Errorf("forwarded text = %v", text)
	}
	// A forwarded photo keeps its media and caption, and drops its old reply and its secret.
	photo := &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("look"),
		ContextInfo: &waE2E.ContextInfo{StanzaID: proto.String("OLD")}},
		MessageContextInfo: &waE2E.MessageContextInfo{MessageSecret: []byte("secret")}}
	msg := build(t, model.Outgoing{Forward: &model.Message{ID: "M2", MediaType: "image", Raw: encoded(t, photo)}})
	img := msg.GetImageMessage()
	if img.GetCaption() != "look" || !img.GetContextInfo().GetIsForwarded() || img.GetContextInfo().GetStanzaID() != "" ||
		msg.MessageContextInfo != nil {
		t.Errorf("forwarded photo = %v", msg)
	}
	// A stored message whose bytes do not decode is forwarded as its text.
	if broken := build(t, model.Outgoing{Forward: &model.Message{Text: "fallback", Raw: []byte{0xff}}}); broken.GetExtendedTextMessage().GetText() != "fallback" {
		t.Errorf("broken stored message = %v", broken)
	}
}
