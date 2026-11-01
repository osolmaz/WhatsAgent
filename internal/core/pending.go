package core

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Approval codes are two-digit numbers, from 10 to 99, that are quick to type on a phone. No two
// open questions share a code, and a code rests for codeRest after its question closes, so a late
// answer to an old question cannot approve a new one.
const (
	codeMin  = 10
	codeMax  = 99
	codeRest = time.Hour
)

// pending is a question to the owner that waits for "yes <code>" or "no <code>".
type pending struct {
	code   string
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

// ask posts prompt in the self-chat and runs decide with the owner's answer. decide returns the
// line to post afterwards. It returns the code to answer with.
func (c *Core) ask(prompt string, decide func(ctx context.Context, allow bool) string) string {
	c.mu.Lock()
	code := c.newCode()
	c.pending[code] = &pending{code: code, decide: decide}
	c.mu.Unlock()
	c.say(fmt.Sprintf("%s\nReply \"yes %s\" or \"no %s\".", prompt, code, code))
	return code
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

// answer applies the owner's answer to the question with code.
func (c *Core) answer(ctx context.Context, code string, allow bool) {
	p := c.close(code)
	if p == nil {
		c.say(fmt.Sprintf("Nothing waits for the code %s.", code))
		return
	}
	if line := p.decide(ctx, allow); line != "" {
		c.say(line)
	}
}

// Approve asks the owner whether a session may use a tool, and waits for the answer for at most
// timeout. It is the permission prompt tool of home and the chat agents.
func (c *Core) Approve(ctx context.Context, caller Caller, tool, input string, timeout time.Duration) bool {
	answer := make(chan bool, 1)
	prompt := fmt.Sprintf("%s wants to use %s:\n%s", caller.Session, tool, clip(input, 1500))
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
	prompt := fmt.Sprintf("%s wants to use %s: %s\n%s", caller.Session, tool, description, clip(preview, 1500))
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
