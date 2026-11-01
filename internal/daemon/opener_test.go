package daemon

import (
	"testing"
	"time"
)

func TestPageOpener(t *testing.T) {
	var opened []string
	viewers, enabled := 0, true
	o := &pageOpener{url: "http://page", every: time.Hour,
		viewers: func() int { return viewers },
		enabled: func() bool { return enabled },
		open:    func(url string) bool { opened = append(opened, url); return true }}

	o.agentStarted()
	o.agentStarted() // within the hour: no second page
	if len(opened) != 1 || opened[0] != "http://page" {
		t.Fatalf("opened %v", opened)
	}
	o.last = time.Time{}
	viewers = 1
	o.agentStarted() // a page is open already
	viewers, enabled = 0, false
	o.agentStarted() // the owner turned it off
	if len(opened) != 1 {
		t.Errorf("opened %v", opened)
	}
	var none *pageOpener
	none.agentStarted()
	(&pageOpener{}).agentStarted()
}
