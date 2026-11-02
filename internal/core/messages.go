package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// editWindow is how long WhatsApp lets a message be edited after it was sent.
const editWindow = 15 * time.Minute

// Draft is a message that a caller wants to send: text, a file with the text as its caption, or
// both, and optionally the ID of the message in the chat that it answers.
type Draft struct {
	Text    string
	File    *model.File
	ReplyTo string
}

// Send sends text to a chat for caller; see SendMessage.
func (c *Core) Send(ctx context.Context, caller Caller, ref, text string) (string, error) {
	return c.SendMessage(ctx, caller, ref, Draft{Text: text})
}

// SendMessage sends a draft to a chat for caller, following the send rules. A mention in the text,
// @ and a phone number, becomes a WhatsApp mention. With the permission ask it returns at once,
// and the message goes out when the owner answers yes. WhatsAgent uploads a file only after the
// rules allow or ask, so a file that a rule denies never leaves the machine. An approval shows
// the uploaded file itself, and a yes sends that same upload, so the owner approves exactly what
// goes out.
func (c *Core) SendMessage(ctx context.Context, caller Caller, ref string, d Draft) (string, error) {
	if err := checkDraft(d); err != nil {
		return "", err
	}
	jid, text, err := c.compose(ctx, caller, ref, d.Text)
	if err != nil {
		return "", err
	}
	out := model.Outgoing{Text: text, Mentions: model.MentionsIn(text)}
	if out.ReplyTo, err = c.replyTarget(ctx, caller, jid, d.ReplyTo); err != nil {
		return "", err
	}
	permission, err := c.allowed(ctx, caller, jid)
	if err != nil {
		return "", err
	}
	if out.Media, err = c.upload(ctx, d.File); err != nil {
		return "", err
	}
	return c.carry(ctx, caller, permission, proposal{action: c.describeSend(ctx, jid, out), media: out.Media,
		act: func(ctx context.Context) (string, error) {
			id, err := c.send(ctx, jid, out, true)
			return "sent, id " + id, err
		}})
}

func checkDraft(d Draft) error {
	switch {
	case d.File == nil && strings.TrimSpace(d.Text) == "":
		return errors.New("nothing to send: give a text, a file or both")
	case d.File == nil:
		return nil
	case len(d.File.Data) == 0:
		return fmt.Errorf("%s is empty", d.File.Name)
	case len(d.File.Data) > model.MaxFileSize:
		return fmt.Errorf("%s has %s; WhatsAgent sends files up to %s", d.File.Name,
			model.SizeLabel(len(d.File.Data)), model.SizeLabel(model.MaxFileSize))
	}
	return nil
}

// compose resolves a chat and puts the agent prefix in front of an agent's text. It refuses
// agent text that would pass for one of WhatsAgent's own messages.
func (c *Core) compose(ctx context.Context, caller Caller, ref, text string) (jid, out string, err error) {
	if jid, err = c.resolveChat(ctx, ref); err != nil {
		return "", "", err
	}
	out = strings.TrimRightFunc(c.prefixed(ctx, caller, jid, text), unicode.IsSpace)
	if !caller.isOwner() && forged(out) {
		return "", "", errForged
	}
	return jid, out, nil
}

// replyTarget returns the message id of chat that caller may read, or nil when id is empty.
func (c *Core) replyTarget(ctx context.Context, caller Caller, chat, id string) (*model.Message, error) {
	if id == "" {
		return nil, nil
	}
	m, err := c.visibleMessage(ctx, caller, chat, id)
	return &m, err
}

// upload uploads file, or returns nil when there is none.
func (c *Core) upload(ctx context.Context, file *model.File) (*model.Media, error) {
	if file == nil {
		return nil, nil
	}
	media, err := c.wa.Upload(ctx, *file)
	return &media, err
}

// describeSend describes a send for the owner, from its real arguments.
func (c *Core) describeSend(ctx context.Context, jid string, out model.Outgoing) string {
	action := fmt.Sprintf("send %s to %s", describeContent(out), c.describeChat(ctx, jid))
	if r := out.ReplyTo; r != nil {
		action += fmt.Sprintf(", in reply to %s's message %q", senderLabel(*r), clip(r.Body(), 200))
	}
	if out.Media != nil && out.Text != "" {
		action += ", with the caption"
	}
	if out.Text != "" {
		action += ":\n" + out.Text
	}
	return action
}

func describeContent(out model.Outgoing) string {
	switch l := out.Location; {
	case out.Media != nil:
		return "this " + out.Media.Describe()
	case l != nil:
		return strings.TrimSpace(fmt.Sprintf("the location %.5f, %.5f %s", l.Latitude, l.Longitude, l.Name))
	}
	return "this message"
}

// SendLocation sends a place to a chat for caller, following the send rules. WhatsApp shows no
// text with a location, so the text, with the agent prefix, goes first as a message of its own.
func (c *Core) SendLocation(ctx context.Context, caller Caller, ref string, place model.Location, text string) (string, error) {
	if place.Latitude < -90 || place.Latitude > 90 || place.Longitude < -180 || place.Longitude > 180 {
		return "", fmt.Errorf("no place has the latitude %g and the longitude %g", place.Latitude, place.Longitude)
	}
	jid, text, err := c.compose(ctx, caller, ref, text)
	if err != nil {
		return "", err
	}
	out := model.Outgoing{Text: text, Location: &place, Mentions: model.MentionsIn(text)}
	return c.gate(ctx, caller, jid, proposal{action: c.describeSend(ctx, jid, out),
		act: func(ctx context.Context) (string, error) {
			id, err := c.send(ctx, jid, out, true)
			return "sent, id " + id, err
		}})
}

// Typing shows the owner as typing in a chat while an agent prepares an answer. It works only
// where caller may answer at once, because elsewhere no answer may follow the typing.
func (c *Core) Typing(ctx context.Context, caller Caller, ref string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	if permission, err := c.allowed(ctx, caller, jid); err != nil || permission != model.Allow {
		return "", fmt.Errorf("%w: %s shows typing only in a chat where it may answer at once", ErrNotAllowed,
			caller.Session)
	}
	return "typing", c.wa.Typing(ctx, jid)
}

// MarkRead marks a message that caller may read, and the chat's earlier messages, as read. The
// owner's phone shows them as read, and the sender sees blue ticks, so it follows the send rules.
func (c *Core) MarkRead(ctx context.Context, caller Caller, ref, id string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	m, err := c.visibleMessage(ctx, caller, jid, id)
	if err != nil {
		return "", err
	}
	if m.FromMe {
		return "", fmt.Errorf("message %s is the owner's own", id)
	}
	return c.gate(ctx, caller, jid, proposal{
		action: fmt.Sprintf("mark this message from %s in %s, and the ones before it, as read:\n%s", senderLabel(m),
			c.describeChat(ctx, jid), m.Body()),
		act: func(ctx context.Context) (string, error) {
			return "marked as read", c.wa.MarkRead(ctx, jid, cmp.Or(m.RawSender, m.Sender), id, m.Time)
		},
	})
}

// Edit replaces the text of a message, following the send rules. An agent edits only messages
// that WhatsAgent sent for agents, and the owner also their own. WhatsApp allows edits of text
// messages for 15 minutes after they were sent.
func (c *Core) Edit(ctx context.Context, caller Caller, ref, id, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("no text to send")
	}
	jid, text, err := c.compose(ctx, caller, ref, text)
	if err != nil {
		return "", err
	}
	m, err := c.changeable(ctx, caller, jid, id, false)
	if err != nil {
		return "", err
	}
	if m.MediaType != "" || time.Since(m.Time) > editWindow {
		return "", fmt.Errorf("WhatsApp edits only text messages up to %s old, and message %s is a %s from %s",
			editWindow, id, cmp.Or(m.MediaType, "text message"), m.Time.In(c.cfg.Location).Format("15:04"))
	}
	return c.gate(ctx, caller, jid, proposal{
		action: fmt.Sprintf("edit this message in %s:\n%s\nto read:\n%s", c.describeChat(ctx, jid), m.Body(), text),
		act: func(ctx context.Context) (string, error) {
			if err := c.wa.Edit(ctx, jid, id, text); err != nil {
				return "", err
			}
			c.record(ctx, model.Update{Kind: model.Edit, Message: model.Message{Chat: jid, ID: id, Text: text}})
			return "edited", nil
		},
	})
}

// Delete deletes a message for everyone, following the send rules. An agent deletes only messages
// that WhatsAgent sent for agents. The owner also deletes their own messages, and other people's
// messages in a group, which WhatsApp allows when the owner is one of its admins.
func (c *Core) Delete(ctx context.Context, caller Caller, ref, id string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	m, err := c.changeable(ctx, caller, jid, id, strings.HasSuffix(jid, "@g.us"))
	if err != nil {
		return "", err
	}
	sender := ""
	if !m.FromMe {
		sender = cmp.Or(m.RawSender, m.Sender)
	}
	return c.gate(ctx, caller, jid, proposal{
		action: fmt.Sprintf("delete this message from %s in %s for everyone:\n%s", senderLabel(m),
			c.describeChat(ctx, jid), m.Body()),
		act: func(ctx context.Context) (string, error) {
			if err := c.wa.Revoke(ctx, jid, sender, id); err != nil {
				return "", err
			}
			c.record(ctx, model.Update{Kind: model.Delete, Message: model.Message{Chat: jid, ID: id}})
			return "deleted", nil
		},
	})
}

// changeable returns the message id of chat when caller may change it: an agent only a message
// that WhatsAgent sent for agents, and the owner also their own, or anyone's when others is true.
func (c *Core) changeable(ctx context.Context, caller Caller, chat, id string, others bool) (model.Message, error) {
	m, err := c.visibleMessage(ctx, caller, chat, id)
	switch {
	case err != nil:
		return m, err
	case m.Deleted:
		return m, fmt.Errorf("message %s is deleted", id)
	case forged(m.Text):
		return m, fmt.Errorf("%w: message %s is one of WhatsAgent's own", ErrNotAllowed, id)
	case !mayChange(caller, m, others):
		return m, fmt.Errorf("%w: %s may not change message %s, which %s wrote", ErrNotAllowed, caller.Session, id,
			senderLabel(m))
	}
	return m, nil
}

func mayChange(caller Caller, m model.Message, others bool) bool {
	if caller.isOwner() {
		return m.FromMe || others
	}
	return m.Role == model.RoleAgent
}

// Forward forwards a message that caller may read to a chat, following the send rules of that
// chat. The comment goes first, as a message of its own; an agent's comment carries the prefix,
// so the chat sees which agent forwarded the message even without a comment.
func (c *Core) Forward(ctx context.Context, caller Caller, from, id, to, comment string) (string, error) {
	src, err := c.resolveChat(ctx, from)
	if err != nil {
		return "", err
	}
	m, err := c.visibleMessage(ctx, caller, src, id)
	if err != nil {
		return "", err
	}
	if m.Deleted || m.ReactionTo != "" {
		return "", fmt.Errorf("message %s is a deletion or a reaction, which cannot be forwarded", id)
	}
	jid, comment, err := c.compose(ctx, caller, to, comment)
	if err != nil {
		return "", err
	}
	action := fmt.Sprintf("forward this message from %s in %s to %s:\n%s", senderLabel(m), c.describeChat(ctx, src),
		c.describeChat(ctx, jid), m.Body())
	if comment != "" {
		action += "\nafter this message:\n" + comment
	}
	return c.gate(ctx, caller, jid, proposal{action: action, act: func(ctx context.Context) (string, error) {
		return c.forwardTo(ctx, jid, comment, m)
	}})
}

// forwardTo sends the comment, when there is one, and then forwards m to chat.
func (c *Core) forwardTo(ctx context.Context, chat, comment string, m model.Message) (string, error) {
	if comment != "" {
		if _, err := c.send(ctx, chat, model.Outgoing{Text: comment, Mentions: model.MentionsIn(comment)}, true); err != nil {
			return "", err
		}
	}
	id, err := c.send(ctx, chat, model.Outgoing{Forward: &m}, true)
	return "forwarded, id " + id, err
}

// record stores a change that WhatsAgent made itself, such as an edit.
func (c *Core) record(ctx context.Context, u model.Update) {
	if _, err := c.st.Apply(ctx, u); err != nil {
		c.log.Warn("store a change", "id", u.Message.ID, "err", err)
	}
}
