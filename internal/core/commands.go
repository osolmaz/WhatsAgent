package core

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// command is one self-chat command. The self-chat, the CLI and the MCP command tool all run
// commands through Run.
type command struct {
	name    string
	usage   string
	help    string
	mutates bool
	run     func(ctx context.Context, c *Core, args string) (string, error)
}

var commands []command

func init() {
	commands = []command{
		{"help", "/help", "List the commands.", false, cmdHelp},
		{"status", "/status", "Show the WhatsApp link, home, sessions and waiting messages.", false, cmdStatus},
		{"sessions", "/sessions", "List sessions with their harness, folder and push state.", false, cmdSessions},
		{"chats", "/chats <query>", "Find chats by name.", false, cmdChats},
		{"route", "/route <chat> <session>", "Send a chat's new messages to a session. <chat> can be *.", true, cmdRoute},
		{"unroute", "/unroute <chat>", "Stop sending a chat's messages to any session.", true, cmdUnroute},
		{"routes", "/routes", "List the routes.", false, cmdRoutes},
		{"rename", "/rename <session> <name>", "Give a session a new name.", true, cmdRename},
		{"home", "/home claude|codex|off|new", "Run home on Claude Code or Codex, stop it, or start it fresh.", true, cmdHome},
		{"allow", "/allow <session> <chat>", "Let a session send to a chat. Either can be *.", true, ruleCommand(model.Allow)},
		{"ask", "/ask <session> <chat>", "Make a session's sends to a chat wait for your yes.", true, ruleCommand(model.Ask)},
		{"deny", "/deny <session> <chat>", "Stop a session from sending to a chat.", true, ruleCommand(model.Deny)},
		{"rules", "/rules", "List the send rules.", false, cmdRules},
	}
}

func helpText() string {
	var b strings.Builder
	b.WriteString("Commands:")
	for _, cmd := range commands {
		fmt.Fprintf(&b, "\n%s: %s", cmd.usage, cmd.help)
	}
	b.WriteString("\nAnswer an approval with \"yes <code>\" or \"no <code>\".")
	return b.String()
}

func cmdHelp(context.Context, *Core, string) (string, error) {
	return helpText(), nil
}

// Run runs a command line such as "/route Sam billing" for caller. A session's commands that
// change state wait for the owner's yes in the self-chat.
func (c *Core) Run(ctx context.Context, caller Caller, line string) (string, error) {
	name, args, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/")), " ")
	for _, cmd := range commands {
		if cmd.name != strings.ToLower(name) {
			continue
		}
		if cmd.mutates && !caller.owner {
			code := c.ask("", fmt.Sprintf("%s wants to run: /%s %s", caller.Session, cmd.name, args),
				func(ctx context.Context, allow bool) string {
					return c.runApproved(ctx, cmd, args, allow)
				})
			return fmt.Sprintf("waiting for the owner's approval in the self-chat (code %s)", code), nil
		}
		return cmd.run(ctx, c, strings.TrimSpace(args))
	}
	return "", fmt.Errorf("unknown command /%s; send /help for the list", name)
}

func (c *Core) runApproved(ctx context.Context, cmd command, args string, allow bool) string {
	if !allow {
		return "Not run."
	}
	reply, err := cmd.run(ctx, c, strings.TrimSpace(args))
	if err != nil {
		return "Error: " + err.Error()
	}
	return reply
}

func cmdStatus(ctx context.Context, c *Core, _ string) (string, error) {
	s, err := c.Status(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "WhatsApp: %s\n%s", s.WhatsApp, c.homeLine())
	fmt.Fprintf(&b, "\nSessions: %d online, %d push", s.Online, len(s.Push))
	for _, p := range s.Push {
		fmt.Fprintf(&b, "\n- %s", p)
	}
	for _, w := range s.Waiting {
		fmt.Fprintf(&b, "\nWaiting: %s", w)
	}
	fmt.Fprintf(&b, "\nRoutes: %d, send rules: %d", s.Routes, s.Rules)
	return b.String(), nil
}

// Status is a summary of WhatsAgent's state.
type Status struct {
	WhatsApp  string   `json:"whatsapp"`
	SelfChat  string   `json:"self_chat"`
	OwnerName string   `json:"owner_name"`
	Home      string   `json:"home"`
	Online    int      `json:"online"`
	Push      []string `json:"push"`
	Waiting   []string `json:"waiting"`
	Routes    int      `json:"routes"`
	Rules     int      `json:"rules"`
}

// Status returns a summary of WhatsAgent's state.
func (c *Core) Status(ctx context.Context) (Status, error) {
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return Status{}, err
	}
	routes, err := c.st.Routes(ctx)
	if err != nil {
		return Status{}, err
	}
	rules, err := c.st.Rules(ctx)
	if err != nil {
		return Status{}, err
	}
	c.mu.Lock()
	s := Status{WhatsApp: c.state, SelfChat: c.wa.SelfChat(), OwnerName: c.wa.OwnName(), Home: c.homeOn,
		Routes: len(routes), Rules: len(rules)}
	c.mu.Unlock()
	for _, info := range sessions {
		s.addSession(info)
	}
	return s, nil
}

func (s *Status) addSession(info model.Session) {
	if info.Online {
		s.Online++
	}
	if info.Push != "" {
		s.Push = append(s.Push, fmt.Sprintf("%s (%s, %s)", info.Name, info.Harness, info.Push))
	}
	if info.Waiting > 0 && info.Push == "" {
		s.Waiting = append(s.Waiting, fmt.Sprintf("%d messages for %s", info.Waiting, info.Name))
	}
}

func cmdSessions(ctx context.Context, c *Core, _ string) (string, error) {
	sessions, err := c.Sessions(ctx)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, s := range sessions {
		if !s.Online && s.Waiting == 0 {
			continue
		}
		state := "tools only"
		if s.Push != "" {
			state = "push: " + s.Push
		}
		lines = append(lines, fmt.Sprintf("%s: %s, %s, %s, %d waiting", s.Name, s.Harness, s.Folder, state, s.Waiting))
	}
	if len(lines) == 0 {
		return "No session is online.", nil
	}
	return strings.Join(lines, "\n"), nil
}

func cmdChats(ctx context.Context, c *Core, query string) (string, error) {
	chats, err := c.st.Chats(ctx, query, 15)
	if err != nil {
		return "", err
	}
	if len(chats) == 0 {
		return fmt.Sprintf("No chat matches %q.", query), nil
	}
	lines := make([]string, len(chats))
	for i, chat := range chats {
		lines[i] = fmt.Sprintf("%s: %s", chat.Label(), chat.JID)
	}
	return strings.Join(lines, "\n"), nil
}

// splitLast splits "a b c" into "a b" and "c".
func splitLast(args string) (string, string, error) {
	i := strings.LastIndex(args, " ")
	if i < 0 {
		return "", "", fmt.Errorf("give a chat and a session")
	}
	return strings.TrimSpace(args[:i]), strings.TrimSpace(args[i+1:]), nil
}

// chatRef resolves a chat reference, allowing "*" for every chat.
func (c *Core) chatRef(ctx context.Context, ref string) (string, error) {
	if ref == "*" {
		return "*", nil
	}
	return c.resolveChat(ctx, ref)
}

func cmdRoute(ctx context.Context, c *Core, args string) (string, error) {
	ref, session, err := splitLast(args)
	if err != nil {
		return "", err
	}
	jid, err := c.chatRef(ctx, ref)
	if err != nil {
		return "", err
	}
	if err := c.st.SetRoute(ctx, model.Route{Chat: jid, Session: session}); err != nil {
		return "", err
	}
	c.wakeAll()
	chat, _ := c.st.Chat(ctx, jid)
	return fmt.Sprintf("%s now goes to %s.", chat.Label(), session), nil
}

func cmdUnroute(ctx context.Context, c *Core, ref string) (string, error) {
	jid, err := c.chatRef(ctx, ref)
	if err != nil {
		return "", err
	}
	removed, err := c.st.DeleteRoute(ctx, jid)
	if err != nil || !removed {
		return fmt.Sprintf("%s had no route.", jid), err
	}
	return fmt.Sprintf("%s no longer goes to a session.", jid), nil
}

func cmdRoutes(ctx context.Context, c *Core, _ string) (string, error) {
	routes, err := c.st.Routes(ctx)
	if err != nil || len(routes) == 0 {
		return "No routes. Only the self-chat reaches an agent, through home.", err
	}
	model.SortRoutes(routes)
	lines := make([]string, len(routes))
	for i, r := range routes {
		chat, _ := c.st.Chat(ctx, r.Chat)
		lines[i] = fmt.Sprintf("%s -> %s", chat.Label(), r.Session)
	}
	return strings.Join(lines, "\n"), nil
}

func cmdRename(ctx context.Context, c *Core, args string) (string, error) {
	fields := strings.Fields(args)
	if len(fields) != 2 {
		return "", fmt.Errorf("use /rename <session> <name>")
	}
	if err := c.st.RenameSession(ctx, fields[0], fields[1]); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s is now %s. Its routes and rules moved with it.", fields[0], fields[1]), nil
}

func ruleCommand(mode model.Mode) func(ctx context.Context, c *Core, args string) (string, error) {
	return func(ctx context.Context, c *Core, args string) (string, error) {
		session, ref, ok := strings.Cut(strings.TrimSpace(args), " ")
		if !ok {
			return "", fmt.Errorf("use /%s <session> <chat>", mode)
		}
		jid, err := c.chatRef(ctx, ref)
		if err != nil {
			return "", err
		}
		if err := c.st.SetRule(ctx, model.Rule{Session: session, Chat: jid, Mode: mode}); err != nil {
			return "", err
		}
		return fmt.Sprintf("Rule: %s %s -> %s.", mode, session, jid), nil
	}
}

func cmdRules(ctx context.Context, c *Core, _ string) (string, error) {
	rules, err := c.st.Rules(ctx)
	if err != nil || len(rules) == 0 {
		return "No send rules. Sessions may send to their routed chats and the self-chat, and every other send asks you.", err
	}
	lines := make([]string, len(rules))
	for i, r := range rules {
		lines[i] = fmt.Sprintf("%s %s -> %s", r.Mode, r.Session, r.Chat)
	}
	return strings.Join(lines, "\n"), nil
}

func cmdHome(ctx context.Context, c *Core, arg string) (string, error) {
	switch arg = strings.ToLower(strings.TrimSpace(arg)); arg {
	case "off":
		c.stopHome()
		return "Home is off. Plain messages here now get no answer.", c.st.SetSetting(ctx, "home", "")
	case "new":
		return c.newHome(ctx)
	}
	if _, ok := c.cfg.Homes[arg]; !ok {
		return "", fmt.Errorf("use /home %s|off|new", strings.Join(c.harnesses(), "|"))
	}
	if err := c.startHome(ctx, arg); err != nil {
		return "", err
	}
	return fmt.Sprintf("Home runs on %s.", arg), c.st.SetSetting(ctx, "home", arg)
}

func (c *Core) harnesses() []string {
	names := make([]string, 0, len(c.cfg.Homes))
	for name := range c.cfg.Homes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// newHome forgets the current home session of the running harness and starts a fresh one.
func (c *Core) newHome(ctx context.Context) (string, error) {
	c.mu.Lock()
	harness := c.homeOn
	c.mu.Unlock()
	if harness == "" {
		return "", fmt.Errorf("home is off; start it with /home claude or /home codex")
	}
	if err := c.st.SetSetting(ctx, "home."+harness+".session", ""); err != nil {
		return "", err
	}
	if err := c.startHome(ctx, harness); err != nil {
		return "", err
	}
	return fmt.Sprintf("Started a fresh home session on %s.", harness), nil
}

func (c *Core) homeLine() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.homeOn == "" {
		return "Home: off"
	}
	return "Home: " + c.homeOn
}

// startHome stops a running home and starts it on harness.
func (c *Core) startHome(ctx context.Context, harness string) error {
	c.stopHome()
	factory, ok := c.cfg.Homes[harness]
	if !ok {
		return fmt.Errorf("no home adapter for %q", harness)
	}
	if err := c.st.TouchSession(ctx, model.Session{Name: homeName, Harness: harness}); err != nil {
		return err
	}
	key := "home." + harness + ".session"
	id, err := c.st.Setting(ctx, key)
	if err != nil {
		return err
	}
	homeCtx, cancel := context.WithCancel(c.ctx)
	pusher, err := factory(homeCtx, HomeHost{
		Post:          c.PostHome,
		SessionID:     id,
		SaveSessionID: func(id string) error { return c.st.SetSetting(c.ctx, key, id) },
		Approve: func(ctx context.Context, tool, input string) bool {
			return c.Approve(ctx, Caller{Session: homeName}, tool, input, approvalTimeout)
		},
	})
	if err != nil {
		cancel()
		return err
	}
	c.mu.Lock()
	c.homeStop, c.homeOn = cancel, harness
	c.mu.Unlock()
	go func() {
		if err := c.serve(homeCtx, &subscriber{name: homeName, push: harness, pusher: pusher}); err != nil {
			c.say("Home stopped: " + err.Error())
		}
	}()
	return nil
}

func (c *Core) stopHome() {
	c.mu.Lock()
	stop := c.homeStop
	c.homeStop, c.homeOn = nil, ""
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
}
