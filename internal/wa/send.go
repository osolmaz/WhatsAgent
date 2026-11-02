package wa

import (
	"cmp"
	"context"
	"fmt"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// Send sends one message to chat under the message ID id.
func (c *Client) Send(ctx context.Context, chat, id string, out model.Outgoing) error {
	msg, err := outgoingMessage(out)
	if err != nil {
		return err
	}
	return c.sendAs(ctx, chat, id, msg)
}

// sendAs sends msg to chat under the message ID id, or a fresh ID when id is empty.
func (c *Client) sendAs(ctx context.Context, chat, id string, msg *waE2E.Message) error {
	to, err := parseChat(chat)
	if err != nil {
		return err
	}
	_, err = c.cli.SendMessage(ctx, to, msg, whatsmeow.SendRequestExtra{ID: id})
	return err
}

// Edit replaces the text of a message that the owner's account sent.
func (c *Client) Edit(ctx context.Context, chat, id, text string) error {
	to, err := parseChat(chat)
	if err != nil {
		return err
	}
	return c.sendAs(ctx, chat, "", c.cli.BuildEdit(to, id, &waE2E.Message{Conversation: proto.String(text)}))
}

// Revoke deletes a message for everyone. sender is "" for a message of the owner's account, and
// the sender's address for someone else's message in a group that the owner administers.
func (c *Client) Revoke(ctx context.Context, chat, sender, id string) error {
	to, err := parseChat(chat)
	if err != nil {
		return err
	}
	from := types.EmptyJID
	if sender != "" {
		if from, err = parseChat(sender); err != nil {
			return err
		}
	}
	return c.sendAs(ctx, chat, "", c.cli.BuildRevoke(to, from, id))
}

// outgoingMessage builds the WhatsApp message for out: its content, and then the context that
// names the message it answers, the people it mentions and whether it is forwarded.
func outgoingMessage(out model.Outgoing) (*waE2E.Message, error) {
	msg, err := messageContent(out)
	if err != nil {
		return nil, err
	}
	if info := contextFor(out); info != nil {
		msg = withContext(msg, info)
	}
	return wrapDocument(msg), nil
}

func messageContent(out model.Outgoing) (*waE2E.Message, error) {
	switch {
	case out.Forward != nil:
		msg := storedContent(*out.Forward)
		msg.MessageContextInfo = nil
		return msg, nil
	case out.Media != nil:
		msg, err := decode(out.Media.Raw)
		if err != nil {
			return nil, err
		}
		return withCaption(msg, out.Text), nil
	}
	return &waE2E.Message{Conversation: proto.String(out.Text)}, nil
}

func decode(raw []byte) (*waE2E.Message, error) {
	var msg waE2E.Message
	if err := proto.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("decode stored message: %w", err)
	}
	return &msg, nil
}

// storedContent returns what a stored message says: its encoded message when it has one, such
// as a photo, and its text otherwise.
func storedContent(m model.Message) *waE2E.Message {
	if msg, err := decode(m.Raw); err == nil && len(m.Raw) > 0 {
		return msg
	}
	return &waE2E.Message{Conversation: proto.String(m.Text)}
}

// contextFor returns the context of out, or nil when it answers nothing, mentions no one and
// forwards nothing.
func contextFor(out model.Outgoing) *waE2E.ContextInfo {
	if out.ReplyTo == nil && len(out.Mentions) == 0 && out.Forward == nil {
		return nil
	}
	info := &waE2E.ContextInfo{MentionedJID: out.Mentions}
	if r := out.ReplyTo; r != nil {
		info.StanzaID = proto.String(r.ID)
		info.Participant = proto.String(cmp.Or(r.RawSender, r.Sender))
		info.QuotedMessage = storedContent(*r)
		info.QuotedMessage.MessageContextInfo = nil
	}
	if out.Forward != nil {
		info.IsForwarded, info.ForwardingScore = proto.Bool(true), proto.Uint32(1)
	}
	return info
}

// withContext puts info on the content part of msg. Plain text becomes an extended text message,
// because only that kind carries a context; every media kind has its own contextInfo field.
func withContext(msg *waE2E.Message, info *waE2E.ContextInfo) *waE2E.Message {
	if msg.Conversation != nil {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: msg.Conversation, ContextInfo: info}}
	}
	msg.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Message() == nil {
			return true
		}
		field := fd.Message().Fields().ByName("contextInfo")
		if field != nil {
			v.Message().Set(field, protoreflect.ValueOfMessage(info.ProtoReflect()))
		}
		return field == nil
	})
	return msg
}

// wrapDocument puts a document with a caption inside a documentWithCaptionMessage, as WhatsApp
// sends it.
func wrapDocument(msg *waE2E.Message) *waE2E.Message {
	if msg.GetDocumentMessage().GetCaption() == "" {
		return msg
	}
	return &waE2E.Message{DocumentWithCaptionMessage: &waE2E.FutureProofMessage{Message: msg}}
}
