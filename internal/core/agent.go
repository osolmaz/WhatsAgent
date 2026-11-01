package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// The owner names the agents once, with /name. The name is the agents' trigger word: a message
// that contains it as a whole word is addressed to the agents. The prefix, set with /prefix, is
// the text that WhatsAgent puts in front of every message an agent sends, so agents write only
// the message itself.
const (
	settingName   = "agent.name"
	settingPrefix = "agent.prefix"
	settingAuto   = "agent.auto"
	// SettingBrowser is "off" when the daemon may not open the browser page by itself.
	SettingBrowser = "web.open"
	defaultName    = "Agent"
	defaultPrefix  = "🤖 *{name}*:"
	// prefixOff is the /prefix value that turns the prefix off.
	prefixOff = "off"
)

// As returns the caller for the owner's CLI when it sends for an agent session. It keeps the
// owner's rights, and its messages get the agent prefix.
func As(session string) Caller {
	return Caller{Session: session, owner: true}
}

// setting returns a stored setting, or fallback when it is unset or unreadable.
func (c *Core) setting(ctx context.Context, key, fallback string) string {
	if v, err := c.st.Setting(ctx, key); err == nil && v != "" {
		return v
	}
	return fallback
}

// AgentName is the agents' name and trigger word.
func (c *Core) AgentName(ctx context.Context) string {
	return c.setting(ctx, settingName, defaultName)
}

// prefixFor returns the prefix for a message from session, filled in. A route's own prefix wins
// over the owner's prefix.
func (c *Core) prefixFor(ctx context.Context, session, chat string) string {
	template := c.setting(ctx, settingPrefix, defaultPrefix)
	if r, ok := c.routedTo(ctx, session, chat); ok && r.Prefix != "" {
		template = r.Prefix
	}
	return fillPrefix(template, c.AgentName(ctx), session)
}

// fillPrefix replaces {name} and {session} in a prefix template, and \n with a line break.
func fillPrefix(template, name, session string) string {
	if template == prefixOff {
		return ""
	}
	return strings.NewReplacer("{name}", name, "{session}", session, `\n`, "\n").Replace(template)
}

// withPrefix puts prefix in front of text, with a space between them unless the prefix already
// ends in white space. Text that already starts with the prefix stays as it is.
func withPrefix(prefix, text string) string {
	if prefix == "" || strings.HasPrefix(text, prefix) {
		return text
	}
	if strings.TrimRightFunc(prefix, unicode.IsSpace) != prefix {
		return prefix + text
	}
	return prefix + " " + text
}

// mentions reports whether text contains word as a whole word, in any case.
func mentions(text, word string) bool {
	if word == "" {
		return false
	}
	edge := `[^\p{L}\p{N}_]`
	pattern := `(?i)(?:^|` + edge + `)` + regexp.QuoteMeta(word) + `(?:$|` + edge + `)`
	return regexp.MustCompile(pattern).MatchString(text)
}

func cmdName(ctx context.Context, c *Core, args string) (string, error) {
	if args == "" {
		return "The agents are called " + c.AgentName(ctx) + ".", nil
	}
	if strings.ContainsAny(args, "\r\n") {
		return "", errors.New("give the name on one line")
	}
	if err := c.st.SetSetting(ctx, settingName, args); err != nil {
		return "", err
	}
	return fmt.Sprintf("The agents are now called %s. A message that contains %q reaches the routes that "+
		"take only addressed messages.", args, args), nil
}

// auto reports whether the agents that WhatsAgent starts use their harness's automatic approval.
func (c *Core) auto(ctx context.Context) bool {
	return c.setting(ctx, settingAuto, "on") == "on"
}

func cmdAuto(ctx context.Context, c *Core, args string) (string, error) {
	switch arg := strings.ToLower(args); arg {
	case "":
	case "on", "off":
		if err := c.st.SetSetting(ctx, settingAuto, arg); err != nil {
			return "", err
		}
		if err := c.restartAgents(ctx); err != nil {
			return "", err
		}
	default:
		return "", errors.New("use /auto on or /auto off")
	}
	if c.auto(ctx) {
		return "Auto approval is on: home and the chat agents decide their own tool calls with Claude Code's " +
			"auto mode or Codex's auto review, and ask you only when the harness will not decide.", nil
	}
	return "Auto approval is off: home and the chat agents ask you before each tool call that needs approval.", nil
}

func cmdBrowser(ctx context.Context, c *Core, args string) (string, error) {
	arg := strings.ToLower(args)
	if arg != "" && arg != "on" && arg != "off" {
		return "", errors.New("use /browser on or /browser off")
	}
	if arg != "" {
		if err := c.st.SetSetting(ctx, SettingBrowser, arg); err != nil {
			return "", err
		}
	}
	if c.setting(ctx, SettingBrowser, "on") == "off" {
		return "WhatsAgent does not open the browser page by itself; `whatsagent web` opens it.", nil
	}
	return "When WhatsAgent starts a chat's agent and no browser page is open, it opens one on this computer.", nil
}

// restartAgents starts home again and stops the running chat agents, so they pick up a changed
// setting. A chat agent starts again with its chat's next message.
func (c *Core) restartAgents(ctx context.Context) error {
	c.mu.Lock()
	harness := c.homeOn
	agents := make(map[string]*chatAgent, len(c.agents))
	for name, agent := range c.agents {
		agents[name] = agent
	}
	c.mu.Unlock()
	for name, agent := range agents {
		c.dropAgent(name, agent)
	}
	if harness == "" {
		return nil
	}
	return c.startHome(ctx, harness)
}

func cmdPrefix(ctx context.Context, c *Core, args string) (string, error) {
	if args != "" {
		if err := c.st.SetSetting(ctx, settingPrefix, args); err != nil {
			return "", err
		}
	}
	template := c.setting(ctx, settingPrefix, defaultPrefix)
	example := withPrefix(fillPrefix(template, c.AgentName(ctx), "billing"), "On it.")
	return fmt.Sprintf("Prefix: %s\nA message from the session billing looks like this:\n%s", template, example), nil
}
