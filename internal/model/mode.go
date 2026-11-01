package model

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Mode decides what the session that handles a chat receives from it and may read in it.
type Mode string

const (
	// ModeOff hides the chat from agents.
	ModeOff Mode = "off"
	// ModeKeyword shows only triggers: the owner's messages that name the agents or answer an
	// agent message. The handler also reads the agents' own messages.
	ModeKeyword Mode = "keyword"
	// ModeWindow shows the chat while a window is open. A trigger opens the window, and the
	// handler then receives every new message and reads back to the window's start.
	ModeWindow Mode = "window"
	// ModeAll shows the whole chat.
	ModeAll Mode = "all"
)

// ParseMode returns the Mode named s.
func ParseMode(s string) (Mode, error) {
	switch m := Mode(strings.ToLower(s)); m {
	case ModeOff, ModeKeyword, ModeWindow, ModeAll:
		return m, nil
	}
	return "", fmt.Errorf("unknown mode %q: use off, keyword, window or all", s)
}

// ChatMode is the mode of one chat, or of "*" for every chat without its own. Length and Lookback
// apply to the window mode; zero means the owner's defaults.
type ChatMode struct {
	Chat     string        `json:"chat"`
	Mode     Mode          `json:"mode"`
	Length   time.Duration `json:"length,omitempty"`
	Lookback time.Duration `json:"lookback,omitempty"`
}

// Window is the open window of one chat. Its handler may read the messages from From to Until,
// and receives the new ones until Until.
type Window struct {
	Chat    string    `json:"chat"`
	Session string    `json:"session"`
	Opened  time.Time `json:"opened"`
	From    time.Time `json:"from"`
	Until   time.Time `json:"until"`
}

// Covers reports whether a message sent at t is inside the window.
func (w Window) Covers(t time.Time) bool {
	return !t.Before(w.From) && !t.After(w.Until)
}

// ParseDuration reads a positive duration such as "30m", "1h", "1h30m" or "1d".
func ParseDuration(s string) (time.Duration, error) {
	d, err := parseDuration(strings.ToLower(strings.TrimSpace(s)))
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("unknown duration %q: use for example 30m, 1h or 1d", s)
	}
	return d, nil
}

func parseDuration(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		return time.Duration(n) * 24 * time.Hour, err
	}
	return time.ParseDuration(s)
}

// FormatDuration writes d for people, such as "30 minutes", "1 hour" or "2 days".
func FormatDuration(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d.Round(time.Minute)/time.Minute), "minute")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
