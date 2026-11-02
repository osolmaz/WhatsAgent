package wa

import (
	"cmp"
	"encoding/hex"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// content is the readable part of a message: its text and, for media, the file details.
type content struct {
	text      string
	mediaType string
	mimeType  string
	filename  string
}

// extractor reads one kind of message content and reports whether m holds that kind.
type extractor func(m *waE2E.Message) (content, bool)

var extractors = []extractor{
	func(m *waE2E.Message) (content, bool) {
		return content{text: m.GetConversation()}, m.Conversation != nil
	},
	func(m *waE2E.Message) (content, bool) {
		return content{text: m.GetExtendedTextMessage().GetText()}, m.ExtendedTextMessage != nil
	},
	func(m *waE2E.Message) (content, bool) {
		x := m.GetImageMessage()
		return content{x.GetCaption(), "image", x.GetMimetype(), ""}, x != nil
	},
	func(m *waE2E.Message) (content, bool) {
		x := m.GetVideoMessage()
		return content{x.GetCaption(), "video", x.GetMimetype(), ""}, x != nil
	},
	func(m *waE2E.Message) (content, bool) {
		x := m.GetAudioMessage()
		return content{"", "audio", x.GetMimetype(), ""}, x != nil
	},
	func(m *waE2E.Message) (content, bool) {
		x := m.GetDocumentMessage()
		return content{x.GetCaption(), "document", x.GetMimetype(), x.GetFileName()}, x != nil
	},
	func(m *waE2E.Message) (content, bool) {
		x := m.GetStickerMessage()
		return content{"", "sticker", x.GetMimetype(), ""}, x != nil
	},
	func(m *waE2E.Message) (content, bool) {
		x := m.GetLocationMessage()
		return content{x.GetName() + " " + x.GetAddress(), "location", "", ""}, x != nil
	},
	func(m *waE2E.Message) (content, bool) {
		x := m.GetContactMessage()
		return content{x.GetDisplayName(), "contact", "", ""}, x != nil
	},
	func(m *waE2E.Message) (content, bool) {
		x := pollOf(m)
		options := make([]string, len(x.GetOptions()))
		for i, o := range x.GetOptions() {
			options[i] = o.GetOptionName()
		}
		return content{model.Poll{Question: x.GetName(), Options: options}.Text(), "poll", "", ""}, x != nil
	},
}

// pollOf returns the poll of a message in any of the versions that WhatsApp sends, or nil.
func pollOf(m *waE2E.Message) *waE2E.PollCreationMessage {
	return cmp.Or(m.GetPollCreationMessage(), m.GetPollCreationMessageV2(), m.GetPollCreationMessageV3(),
		m.GetPollCreationMessageV5(), m.GetPollCreationMessageV6())
}

func extract(m *waE2E.Message) (content, bool) {
	for _, read := range extractors {
		if c, ok := read(m); ok {
			return c, true
		}
	}
	return content{}, false
}

// contextOf returns the context info of the first message part that carries one.
func contextOf(m *waE2E.Message) *waE2E.ContextInfo {
	for _, info := range []*waE2E.ContextInfo{
		m.GetExtendedTextMessage().GetContextInfo(),
		m.GetImageMessage().GetContextInfo(),
		m.GetVideoMessage().GetContextInfo(),
		m.GetAudioMessage().GetContextInfo(),
		m.GetDocumentMessage().GetContextInfo(),
		m.GetStickerMessage().GetContextInfo(),
	} {
		if info != nil {
			return info
		}
	}
	return nil
}

// parse turns a whatsmeow message event into an update. It reports false for messages that
// carry nothing to store, such as key distribution messages.
func parse(evt *events.Message) (model.Update, bool) {
	m := evt.Message
	base := baseMessage(evt)
	if p := m.GetProtocolMessage(); p != nil {
		return parseProtocol(base, p)
	}
	if r := m.GetReactionMessage(); r != nil {
		base.ReactionTo, base.Reaction = r.GetKey().GetID(), r.GetText()
		return model.Update{Kind: model.New, Message: base}, true
	}
	c, ok := extract(m)
	if !ok {
		return model.Update{}, false
	}
	base.Text, base.MediaType, base.MimeType, base.Filename = c.text, c.mediaType, c.mimeType, c.filename
	addContext(&base, contextOf(m))
	if base.MediaType != "" {
		base.Raw, _ = proto.Marshal(m)
	}
	return model.Update{Kind: model.New, Message: base}, true
}

// baseMessage returns the parts of a message that every kind has: chat, ID, sender and time.
func baseMessage(evt *events.Message) model.Message {
	return model.Message{
		Chat:       evt.Info.Chat.ToNonAD().String(),
		ID:         evt.Info.ID,
		Sender:     evt.Info.Sender.ToNonAD().String(),
		RawSender:  evt.Info.Sender.ToNonAD().String(),
		SenderName: evt.Info.PushName,
		Time:       evt.Info.Timestamp.UTC(),
		FromMe:     evt.Info.IsFromMe,
	}
}

// voteUpdate turns a decrypted vote into a message that answers the poll and carries the hashes of
// the options that the voter picked. A vote holds the voter's whole choice, and an empty choice
// takes the vote back.
func voteUpdate(evt *events.Message, vote *waE2E.PollVoteMessage) model.Update {
	m := baseMessage(evt)
	m.MediaType = "poll vote"
	m.ReplyTo = evt.Message.GetPollUpdateMessage().GetPollCreationMessageKey().GetID()
	m.PollVote = []string{}
	for _, hash := range vote.GetSelectedOptions() {
		m.PollVote = append(m.PollVote, hex.EncodeToString(hash))
	}
	return model.Update{Kind: model.New, Message: m}
}

func parseProtocol(base model.Message, p *waE2E.ProtocolMessage) (model.Update, bool) {
	switch p.GetType() {
	case waE2E.ProtocolMessage_REVOKE:
		base.ID = p.GetKey().GetID()
		return model.Update{Kind: model.Delete, Message: base}, true
	case waE2E.ProtocolMessage_MESSAGE_EDIT:
		base.ID = p.GetKey().GetID()
		c, _ := extract(p.GetEditedMessage())
		base.Text = c.text
		return model.Update{Kind: model.Edit, Message: base}, true
	}
	return model.Update{}, false
}

func addContext(m *model.Message, info *waE2E.ContextInfo) {
	if info == nil {
		return
	}
	m.ReplyTo = info.GetStanzaID()
	if quoted := info.GetQuotedMessage(); quoted != nil {
		c, _ := extract(quoted)
		m.Quote = c.text
	}
	m.Forwarded = info.GetIsForwarded()
	if jids := info.GetMentionedJID(); len(jids) > 0 {
		m.Mentions = make(map[string]string, len(jids))
		for _, jid := range jids {
			m.Mentions[jid] = ""
		}
	}
}
