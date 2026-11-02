package core

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// Approval codes are two-digit numbers, from 10 to 99, that are quick to type on a phone. No two
// open questions share a code, and a code rests for codeRest after its question closes, so a late
// answer to an old question cannot approve a new one.
const (
	codeMin  = 10
	codeMax  = 99
	codeRest = time.Hour
)

// pending is a question to the owner that waits for "yes <code>" or "no <code>". The owner
// answers it in the chat where WhatsAgent asked, or in the self-chat.
type pending struct {
	code   string
	chat   string
	decide func(ctx context.Context, allow bool) string
}

// newCode picks a random code that no open question uses and that did not close within
// codeRest. When every code is taken, it takes the code that rested longest, and a number above
// codeMax when all of them are open. The caller holds c.mu.
func (c *Core) newCode() string {
	var free, resting []string
	for n := codeMin; n <= codeMax; n++ {
		code := strconv.Itoa(n)
		if _, open := c.pending[code]; open {
			continue
		}
		if closed, ok := c.closed[code]; ok && time.Since(closed) < codeRest {
			resting = append(resting, code)
			continue
		}
		free = append(free, code)
	}
	if len(free) > 0 {
		return free[rand.IntN(len(free))]
	}
	if len(resting) > 0 {
		sort.Slice(resting, func(i, j int) bool { return c.closed[resting[i]].Before(c.closed[resting[j]]) })
		return resting[0]
	}
	return strconv.Itoa(codeMax + 1 + len(c.pending))
}

// ask asks in the self-chat; see askIn.
func (c *Core) ask(prompt string, decide func(ctx context.Context, allow bool) string) string {
	return c.askIn(c.wa.SelfChat(), prompt, nil, decide)
}

// askIn posts prompt in chat, with media when media is not nil, and runs decide with the owner's
// answer. decide returns the line to post afterwards. It returns the code to answer with.
func (c *Core) askIn(chat, prompt string, media *model.Media, decide func(ctx context.Context, allow bool) string) string {
	c.mu.Lock()
	code := c.newCode()
	c.pending[code] = &pending{code: code, chat: chat, decide: decide}
	c.mu.Unlock()
	c.showIn(chat, fmt.Sprintf("%s\nReply \"yes %s\" or \"no %s\".", prompt, code, code), media)
	return code
}

// sayIn posts a WhatsAgent line in chat.
func (c *Core) sayIn(chat, text string) {
	c.showIn(chat, text, nil)
}

// showIn posts a WhatsAgent line in chat, as the caption of media when media is not nil.
func (c *Core) showIn(chat, text string, media *model.Media) {
	text = prefixWhatsAgent + text
	if chat == c.wa.SelfChat() {
		c.queue(outgoing{text: text, media: media})
		return
	}
	go func() {
		if _, err := c.send(c.ctx, chat, model.Outgoing{Text: text, Media: media}, true); err != nil {
			c.log.Warn("send a WhatsAgent message", "chat", chat, "err", err)
		}
	}()
}

// close removes the question with code and returns it, or nil when none waits.
func (c *Core) close(code string) *pending {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pending[code]
	if p != nil {
		delete(c.pending, code)
		c.closed[code] = time.Now()
	}
	return p
}

// verdict reports whether m is the owner's answer to a question that WhatsAgent asked in m's
// chat.
func (c *Core) verdict(m model.Message) bool {
	v := verdictPattern.FindStringSubmatch(m.Text)
	if v == nil || !m.FromMe {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pending[v[2]]
	return p != nil && p.chat == m.Chat
}

// answer applies the owner's answer, given in chat, to the question with code.
func (c *Core) answer(ctx context.Context, chat, code string, allow bool) {
	c.mu.Lock()
	p := c.pending[code]
	c.mu.Unlock()
	if p == nil || (chat != p.chat && chat != c.wa.SelfChat()) {
		c.sayIn(chat, fmt.Sprintf("Nothing waits for the code %s here.", code))
		return
	}
	if c.close(code) == nil {
		return
	}
	if line := p.decide(ctx, allow); line != "" {
		c.sayIn(p.chat, line)
	}
}

// Approve asks the owner whether a session may use a tool, and waits for the answer for at most
// timeout. It is the permission prompt tool for sessions that use one.
func (c *Core) Approve(ctx context.Context, caller Caller, tool, input string, timeout time.Duration) bool {
	answer := make(chan bool, 1)
	prompt := fmt.Sprintf("Approval: the agent session %s wants to use the tool %s with this input:\n%s",
		caller.Session, tool, clip(input, 1500))
	code := c.ask(prompt, func(_ context.Context, allow bool) string {
		answer <- allow
		return ""
	})
	select {
	case allow := <-answer:
		return allow
	case <-ctx.Done():
	case <-time.After(timeout):
		c.say(fmt.Sprintf("No answer for %s, so %s was denied.", code, tool))
	}
	c.close(code)
	return false
}

// RelayPermission posts a permission prompt that a session's harness raised, and passes the
// owner's answer back to that session with the harness's own request ID.
func (c *Core) RelayPermission(caller Caller, requestID, tool, description, preview string) {
	// The harness reads the tool and the input from the real tool call; the description can be
	// the agent's own words, so it comes last and says so.
	prompt := fmt.Sprintf("Approval: the agent session %s wants to use the tool %s with this input:\n%s",
		caller.Session, tool, clip(preview, 1500))
	if description = strings.TrimSpace(description); description != "" {
		prompt += "\nThe agent's own description: " + clip(description, 300)
	}
	c.ask(prompt, func(ctx context.Context, allow bool) string {
		c.mu.Lock()
		sub := c.subs[caller.Session]
		c.mu.Unlock()
		if sub == nil {
			return fmt.Sprintf("%s is offline, so the answer was not delivered.", caller.Session)
		}
		if err := sub.pusher.Verdict(ctx, requestID, allow); err != nil {
			return fmt.Sprintf("The answer did not reach %s: %v", caller.Session, err)
		}
		return ""
	})
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
