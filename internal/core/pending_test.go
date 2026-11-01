package core

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestApprovalCodes(t *testing.T) {
	c := &Core{pending: map[string]*pending{}, closed: map[string]time.Time{}}
	seen := map[string]bool{}
	for i := 0; i < codeMax-codeMin+1; i++ {
		code := c.newCode()
		n, err := strconv.Atoi(code)
		if err != nil || n < codeMin || n > codeMax || seen[code] {
			t.Fatalf("code %q after %d codes", code, i)
		}
		seen[code] = true
		c.pending[code] = &pending{code: code}
	}
	// Every code is open: a longer number keeps them apart.
	if code := c.newCode(); code != "190" {
		t.Errorf("code with every code open = %q", code)
	}

	// Closed codes rest, and the one that rested longest goes first.
	c.pending = map[string]*pending{}
	for n := codeMin; n <= codeMax; n++ {
		c.closed[strconv.Itoa(n)] = time.Now().Add(-time.Duration(n) * time.Second)
	}
	if code := c.newCode(); code != "99" {
		t.Errorf("code with every code resting = %q", code)
	}
	c.closed["42"] = time.Now().Add(-2 * codeRest)
	if code := c.newCode(); code != "42" {
		t.Errorf("code after its rest = %q", code)
	}
}

func TestLateAnswerFindsNothing(t *testing.T) {
	h := newHarness(t)
	code := h.core.ask("first question", func(context.Context, bool) string { return "first answered" })
	h.expectCode("first question")
	h.receive(self, "yes "+code, true)
	h.expect("first answered")
	if _, ok := h.core.closed[code]; !ok {
		t.Fatal("an answered code does not rest")
	}
	h.receive(self, "yes "+code, true)
	h.expect("Nothing waits for the code " + code)
}
