package core

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// maxGroupName is the longest group name that WhatsApp accepts, in characters.
const maxGroupName = 25

// administer runs p at once for the owner. An agent always needs the owner's yes, whatever the
// send rules allow, because p changes a group or the owner's account, and a deny rule for chat
// still refuses it.
func (c *Core) administer(ctx context.Context, caller Caller, chat string, p proposal) (string, error) {
	if caller.isOwner() {
		return p.act(ctx)
	}
	return c.confirm(ctx, caller, chat, p)
}

// groupChat resolves a reference to a group.
func (c *Core) groupChat(ctx context.Context, ref string) (string, error) {
	jid, err := c.resolveChat(ctx, ref)
	if err == nil && !isGroup(jid) {
		err = fmt.Errorf("%s is not a group", jid)
	}
	return jid, err
}

// GroupInfo returns what WhatsApp tells about a group that caller may read: its name,
// description, settings and members.
func (c *Core) GroupInfo(ctx context.Context, caller Caller, ref string) (model.Group, error) {
	jid, err := c.groupChat(ctx, ref)
	if err != nil {
		return model.Group{}, err
	}
	if _, ok := c.viewFor(ctx, caller, jid); !ok {
		return model.Group{}, errHidden(caller, jid)
	}
	return c.wa.GroupInfo(ctx, jid)
}

// ChangeGroup applies a change to a group, such as a new name or new members. The owner's change
// runs at once, and an agent's waits for the owner's yes.
func (c *Core) ChangeGroup(ctx context.Context, caller Caller, ref string, change model.GroupChange) (string, error) {
	jid, err := c.groupChat(ctx, ref)
	if err != nil {
		return "", err
	}
	if change.Members, err = c.checkChange(ctx, change); err != nil {
		return "", err
	}
	return c.administer(ctx, caller, jid, proposal{
		action: fmt.Sprintf("change the group %s: %s", c.describeChat(ctx, jid), describeChange(change)),
		act: func(ctx context.Context) (string, error) {
			return c.wa.ChangeGroup(ctx, jid, change)
		},
	})
}

// checkChange checks that a change has the argument that its action needs, and returns its
// members as JIDs.
func (c *Core) checkChange(ctx context.Context, change model.GroupChange) ([]string, error) {
	needs, ok := model.GroupActions[change.Action]
	switch {
	case !ok:
		return nil, fmt.Errorf("no group action %q", change.Action)
	case needs == "text" && strings.TrimSpace(change.Text) == "":
		return nil, fmt.Errorf("%s needs a text", change.Action)
	case needs == "text" && change.Action == "rename" && len([]rune(change.Text)) > maxGroupName:
		return nil, fmt.Errorf("WhatsApp allows group names of up to %d characters", maxGroupName)
	case needs == "members":
		return c.people(ctx, change.Members)
	}
	return nil, nil
}

func describeChange(change model.GroupChange) string {
	switch model.GroupActions[change.Action] {
	case "text":
		return fmt.Sprintf("%s it to %q", change.Action, change.Text)
	case "members":
		return fmt.Sprintf("%s %s", change.Action, strings.Join(change.Members, ", "))
	case "on":
		return fmt.Sprintf("turn %s %s", change.Action, map[bool]string{true: "on", false: "off"}[change.On])
	}
	return strings.ReplaceAll(change.Action, "_", " ")
}

// people resolves references to people, such as phone numbers, to JIDs. It needs at least one.
func (c *Core) people(ctx context.Context, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, errors.New("no people given")
	}
	jids := make([]string, len(refs))
	for i, ref := range refs {
		jid, err := c.resolveChat(ctx, ref)
		if err != nil {
			return nil, err
		}
		if isGroup(jid) {
			return nil, fmt.Errorf("%s is a group, not a person", ref)
		}
		jids[i] = jid
	}
	return jids, nil
}

// CreateGroup creates a group with the owner and members. The owner's group is created at once,
// and an agent's waits for the owner's yes.
func (c *Core) CreateGroup(ctx context.Context, caller Caller, name string, members []string) (string, error) {
	if strings.TrimSpace(name) == "" || len([]rune(name)) > maxGroupName {
		return "", fmt.Errorf("a group needs a name of 1 to %d characters", maxGroupName)
	}
	jids, err := c.people(ctx, members)
	if err != nil {
		return "", err
	}
	return c.administer(ctx, caller, "", proposal{
		action: fmt.Sprintf("create the group %q with %s", name, strings.Join(jids, ", ")),
		act: func(ctx context.Context) (string, error) {
			jid, err := c.wa.CreateGroup(ctx, name, jids)
			if err == nil {
				c.OnChatName(jid, name)
			}
			return "created " + jid, err
		},
	})
}

// JoinGroup joins a group by its invite link. The owner joins at once, and an agent waits for the
// owner's yes.
func (c *Core) JoinGroup(ctx context.Context, caller Caller, link string) (string, error) {
	if link = strings.TrimSpace(link); link == "" {
		return "", errors.New("no invite link given")
	}
	return c.administer(ctx, caller, "", proposal{
		action: "join the group of this invite link: " + link,
		act: func(ctx context.Context) (string, error) {
			jid, err := c.wa.JoinGroup(ctx, link)
			return "joined " + jid, err
		},
	})
}
