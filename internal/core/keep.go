package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// WhatsAgent stores the messages of kept chats in its database, and keeps the messages of other
// chats in memory for Config.Recent; see the message storage specification in docs.

// cmdKeep lists the kept chats, or keeps a chat or stops keeping it.
func cmdKeep(ctx context.Context, c *Core, args string) (string, error) {
	if args == "" {
		return c.keptList(ctx)
	}
	ref, state, err := splitLast(args)
	state = strings.ToLower(state)
	if err != nil || (state != "on" && state != "off") {
		return "", errors.New("use /keep, or /keep <chat> on|off")
	}
	jid, err := c.resolveChat(ctx, ref)
	if err != nil {
		return "", err
	}
	chat, err := c.st.Chat(ctx, jid)
	if err != nil {
		return "", err
	}
	if state == "on" {
		return c.keep(ctx, chat)
	}
	return c.unkeep(ctx, chat)
}

func (c *Core) keep(ctx context.Context, chat model.Chat) (string, error) {
	n, moved, err := c.st.Keep(ctx, chat.JID)
	if err != nil {
		return "", err
	}
	if !moved {
		return chat.Label() + " is kept already.", nil
	}
	return fmt.Sprintf("WhatsAgent now stores the messages of %s, and stored its %d recent ones.", chat.Label(), n), nil
}

func (c *Core) unkeep(ctx context.Context, chat model.Chat) (string, error) {
	if chat.JID == c.wa.SelfChat() {
		return "", errors.New("the self-chat is always kept")
	}
	n, was, err := c.st.Unkeep(ctx, chat.JID)
	if err != nil {
		return "", err
	}
	if !was {
		return chat.Label() + " is not kept.", nil
	}
	return fmt.Sprintf("WhatsAgent no longer stores %s and deleted its %d stored messages. Its new messages "+
		"stay in memory for %s.", chat.Label(), n, model.FormatDuration(c.cfg.Recent)), nil
}

// keptList names the kept chats.
func (c *Core) keptList(ctx context.Context) (string, error) {
	kept, err := c.st.KeptChats(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "WhatsAgent stores the messages of these chats; other chats stay in memory for %s:",
		model.FormatDuration(c.cfg.Recent))
	for _, jid := range kept {
		label := "the self-chat"
		if jid != c.wa.SelfChat() {
			chat, err := c.st.Chat(ctx, jid)
			if err != nil {
				return "", err
			}
			label = chat.Label()
		}
		fmt.Fprintf(&b, "\n- %s", label)
	}
	return b.String(), nil
}

// keptNote warns, for a new route to a session that WhatsAgent does not run, that the session
// gets nothing from a chat that is not kept.
func (c *Core) keptNote(ctx context.Context, route model.Route) string {
	if route.Session == homeName {
		return ""
	}
	if route.Chat == everyChat {
		return fmt.Sprintf(" %s gets messages only from kept chats.", route.Session)
	}
	if kept, err := c.st.Kept(ctx, route.Chat); err == nil && !kept {
		return fmt.Sprintf(" The chat is not kept, so %s gets nothing from it until you /keep it.", route.Session)
	}
	return ""
}

// cmdWeb answers with the browser page's link. It runs only from the phone.
func cmdWeb(_ context.Context, c *Core, _ string) (string, error) {
	if c.cfg.PageURL == "" {
		return "", errors.New("the browser page is not running")
	}
	return "The browser page with the agents' terminals: " + c.cfg.PageURL + "\nKeep the link private.", nil
}
