package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// maxPollOptions is the most options that a WhatsApp poll has.
const maxPollOptions = 12

// SendPoll sends a poll to a chat for caller, following the send rules. The agent prefix goes in
// front of the question. Each vote arrives as a message that answers the poll and names the
// options that the voter picked.
func (c *Core) SendPoll(ctx context.Context, caller Caller, ref string, p model.Poll) (string, error) {
	if err := checkPoll(p); err != nil {
		return "", err
	}
	jid, question, err := c.compose(ctx, caller, ref, p.Question)
	if err != nil {
		return "", err
	}
	p.Question = question
	return c.gate(ctx, caller, jid, proposal{
		action: fmt.Sprintf("send this poll to %s:\n%s", c.describeChat(ctx, jid), p.Text()),
		act: func(ctx context.Context) (string, error) {
			id, err := c.send(ctx, jid, model.Outgoing{Poll: &p}, true)
			return "sent, id " + id, err
		},
	})
}

func checkPoll(p model.Poll) error {
	if strings.TrimSpace(p.Question) == "" {
		return errors.New("a poll needs a question")
	}
	if len(p.Options) < 2 || len(p.Options) > maxPollOptions {
		return fmt.Errorf("a poll has 2 to %d options, not %d", maxPollOptions, len(p.Options))
	}
	seen := map[string]bool{}
	for _, option := range p.Options {
		if strings.TrimSpace(option) == "" || strings.Contains(option, "\n") || seen[option] {
			return fmt.Errorf("the poll option %q is empty, has a line break or repeats another", option)
		}
		seen[option] = true
	}
	return nil
}

// nameVote writes which options a vote picks, from the stored poll that it answers, and quotes
// the poll's question. WhatsApp sends a vote as hashes of the option names, so a vote on a poll
// that WhatsAgent did not store keeps no names.
func (c *Core) nameVote(ctx context.Context, m model.Message) model.Message {
	poll, found, err := c.st.Message(ctx, m.Chat, m.ReplyTo)
	if err != nil || !found {
		m.Text = "a vote on a poll that WhatsAgent did not store"
		return m
	}
	m.Quote, _, _ = strings.Cut(poll.Text, "\n")
	var picked []string
	for _, option := range model.PollOptions(poll.Text) {
		sum := sha256.Sum256([]byte(option))
		if slices.Contains(m.PollVote, hex.EncodeToString(sum[:])) {
			picked = append(picked, option)
		}
	}
	m.Text = "took the vote back"
	if len(picked) > 0 {
		m.Text = "picked " + strings.Join(picked, ", ")
	}
	return m
}
