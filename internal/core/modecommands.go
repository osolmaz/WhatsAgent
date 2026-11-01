package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

const modeUsage = "use /mode <chat> off|keyword|window|all [<length> [<lookback>]]"

func cmdMode(ctx context.Context, c *Core, args string) (string, error) {
	if args == "" {
		return c.modesText(ctx)
	}
	ref, mode, err := parseModeArgs(args)
	if err != nil {
		return "", err
	}
	jid, err := c.chatRef(ctx, ref)
	if err != nil {
		return "", err
	}
	mode.Chat = jid
	if err := c.st.SetChatMode(ctx, mode); err != nil {
		return "", err
	}
	if mode.Mode != model.ModeWindow {
		// The window belongs to the window mode; another mode replaces it without a goodbye.
		if _, err := c.st.DeleteWindow(ctx, jid); err != nil {
			return "", err
		}
	}
	c.wakeAll()
	chat, _ := c.st.Chat(ctx, jid)
	return fmt.Sprintf("%s: %s.", chatLabel(chat), c.modeText(ctx, jid)), nil
}

// parseModeArgs reads "<chat> <mode> [<length> [<lookback>]]". The chat can contain spaces.
func parseModeArgs(args string) (string, model.ChatMode, error) {
	fields := strings.Fields(args)
	var durations []time.Duration
	for len(fields) > 2 && len(durations) < 2 {
		d, err := model.ParseDuration(fields[len(fields)-1])
		if err != nil {
			break
		}
		durations = append([]time.Duration{d}, durations...)
		fields = fields[:len(fields)-1]
	}
	if len(fields) < 2 {
		return "", model.ChatMode{}, errors.New(modeUsage)
	}
	mode, err := model.ParseMode(fields[len(fields)-1])
	if err != nil {
		return "", model.ChatMode{}, err
	}
	m := model.ChatMode{Mode: mode}
	if len(durations) > 0 {
		m.Length = durations[0]
	}
	if len(durations) > 1 {
		m.Lookback = durations[1]
	}
	return strings.Join(fields[:len(fields)-1], " "), m, nil
}

// modeText describes the mode of chat for the owner.
func (c *Core) modeText(ctx context.Context, chat string) string {
	m := c.chatMode(ctx, chat)
	if m.Mode != model.ModeWindow {
		return string(m.Mode)
	}
	return fmt.Sprintf("window of %s that reads back %s", model.FormatDuration(m.Length), model.FormatDuration(m.Lookback))
}

func (c *Core) modesText(ctx context.Context) (string, error) {
	modes, err := c.st.ChatModes(ctx)
	if err != nil {
		return "", err
	}
	lines := []string{}
	if len(modes) == 0 || modes[0].Chat != everyChat {
		lines = append(lines, "Every chat: "+c.modeText(ctx, everyChat)+" (the default)")
	}
	for _, m := range modes {
		chat, _ := c.st.Chat(ctx, m.Chat)
		lines = append(lines, fmt.Sprintf("%s: %s", chatLabel(chat), c.modeText(ctx, m.Chat)))
	}
	windows, err := c.windowLines(ctx)
	for _, w := range windows {
		lines = append(lines, "Open window: "+w)
	}
	return strings.Join(lines, "\n"), err
}

// windowLines describes the open windows.
func (c *Core) windowLines(ctx context.Context) ([]string, error) {
	windows, err := c.st.Windows(ctx)
	if err != nil {
		return nil, err
	}
	lines := make([]string, len(windows))
	for i, w := range windows {
		chat, _ := c.st.Chat(ctx, w.Chat)
		lines[i] = fmt.Sprintf("%s, until %s, for %s", chat.Label(), w.Until.In(c.cfg.Location).Format("15:04"), w.Session)
	}
	return lines, nil
}

// windowSetting is one owner setting of the window mode.
type windowSetting struct {
	key, fallback string
	duration      bool
}

var windowSettings = map[string]windowSetting{
	"length":   {settingLength, "1h", true},
	"lookback": {settingLookback, "30m", true},
	"stop":     {settingStop, defaultStop, false},
	"start":    {settingStart, defaultStart, false},
	"end":      {settingEnd, defaultEnd, false},
}

func cmdWindow(ctx context.Context, c *Core, args string) (string, error) {
	name, value, _ := strings.Cut(args, " ")
	name, value = strings.ToLower(name), strings.TrimSpace(value)
	if name == "close" {
		return c.closeWindowCommand(ctx, value)
	}
	if name != "" {
		s, ok := windowSettings[name]
		if !ok || value == "" {
			return "", errors.New("use /window, /window length|lookback|stop|start|end <value>, or /window close <chat>")
		}
		if err := c.setWindowSetting(ctx, s, value); err != nil {
			return "", err
		}
	}
	return c.windowText(ctx)
}

// setWindowSetting stores value, or forgets the setting for "default".
func (c *Core) setWindowSetting(ctx context.Context, s windowSetting, value string) error {
	if strings.EqualFold(value, "default") {
		value = ""
	} else if _, err := model.ParseDuration(value); s.duration && err != nil {
		return err
	}
	return c.st.SetSetting(ctx, s.key, value)
}

func (c *Core) closeWindowCommand(ctx context.Context, ref string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	chat, _ := c.st.Chat(ctx, jid)
	if !c.closeWindow(ctx, jid) {
		return chat.Label() + " has no open window.", nil
	}
	return "Closed the window of " + chat.Label() + ".", nil
}

func (c *Core) windowText(ctx context.Context) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "Window length: %s\nLookback: %s\nStop phrases: %s\nStart message: %s\nEnd message: %s",
		model.FormatDuration(c.duration(ctx, settingLength, defaultLength)),
		model.FormatDuration(c.duration(ctx, settingLookback, defaultLookback)),
		strings.Join(c.stopPhrases(ctx), ", "), c.setting(ctx, settingStart, defaultStart),
		c.setting(ctx, settingEnd, defaultEnd))
	windows, err := c.windowLines(ctx)
	for _, w := range windows {
		fmt.Fprintf(&b, "\nOpen window: %s", w)
	}
	return b.String(), err
}
