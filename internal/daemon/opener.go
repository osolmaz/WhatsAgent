package daemon

import (
	"sync"
	"time"
)

// pageOpener opens the browser page in the default browser when WhatsAgent starts a chat's agent
// and no page is open, at most once per every, unless the owner turned it off with /browser off.
// Home starting with the daemon opens nothing, so a restart does not open a page.
type pageOpener struct {
	url     string
	viewers func() int
	enabled func() bool
	open    func(url string) bool
	every   time.Duration

	mu   sync.Mutex
	last time.Time
}

// agentStarted opens the page for a chat agent that just started, when no one watches the page.
func (o *pageOpener) agentStarted() {
	if o == nil || o.url == "" || o.viewers() > 0 || !o.enabled() {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.last.IsZero() && time.Since(o.last) < o.every {
		return
	}
	o.last = time.Now()
	o.open(o.url)
}
