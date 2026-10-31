package core

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// codeAlphabet is the alphabet of approval codes: lowercase letters without "l", so a code never
// reads as a 1 or an I when typed on a phone.
const codeAlphabet = "abcdefghijkmnopqrstuvwxyz"

// pending is a question to the owner that waits for "yes <code>" or "no <code>".
type pending struct {
	code   string
	decide func(ctx context.Context, allow bool) string
}

func newCode() string {
	b := make([]byte, 5)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = codeAlphabet[int(b[i])%len(codeAlphabet)]
	}
	return string(b)
}

// ask posts prompt in the self-chat and runs decide with the owner's answer. decide returns the
// line to post afterwards. code is the code to answer with, or "" for a new one.
func (c *Core) ask(code, prompt string, decide func(ctx context.Context, allow bool) string) string {
	if code == "" {
		code = newCode()
	}
	c.mu.Lock()
	c.pending[code] = &pending{code: code, decide: decide}
	c.mu.Unlock()
	c.say(fmt.Sprintf("%s\nReply \"yes %s\" or \"no %s\".", prompt, code, code))
	return code
}

// answer applies the owner's answer to the question with code.
func (c *Core) answer(ctx context.Context, code string, allow bool) {
	c.mu.Lock()
	p := c.pending[code]
	delete(c.pending, code)
	c.mu.Unlock()
	if p == nil {
		c.say(fmt.Sprintf("Nothing waits for the code %s.", code))
		return
	}
	if line := p.decide(ctx, allow); line != "" {
		c.say(line)
	}
}

// Approve asks the owner whether a session may use a tool, and waits for the answer for at most
// timeout. It is the permission prompt tool of home.
func (c *Core) Approve(ctx context.Context, caller Caller, tool, input string, timeout time.Duration) bool {
	answer := make(chan bool, 1)
	prompt := fmt.Sprintf("%s wants to use %s:\n%s", caller.Session, tool, clip(input, 1500))
	code := c.ask("", prompt, func(_ context.Context, allow bool) string {
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
	c.mu.Lock()
	delete(c.pending, code)
	c.mu.Unlock()
	return false
}

// RelayPermission posts a permission prompt that a session's harness raised, and passes the
// owner's answer back to that session with the harness's own request ID.
func (c *Core) RelayPermission(caller Caller, requestID, tool, description, preview string) {
	prompt := fmt.Sprintf("%s wants to use %s: %s\n%s", caller.Session, tool, description, clip(preview, 1500))
	c.ask(requestID, prompt, func(ctx context.Context, allow bool) string {
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
