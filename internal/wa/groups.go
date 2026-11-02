package wa

import (
	"cmp"
	"context"
	"fmt"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"github.com/osolmaz/WhatsAgent/internal/model"
)

// GroupInfo returns what WhatsApp tells about a group, with its members by name.
func (c *Client) GroupInfo(ctx context.Context, chat string) (model.Group, error) {
	jid, err := parseChat(chat)
	if err != nil {
		return model.Group{}, err
	}
	info, err := c.cli.GetGroupInfo(ctx, jid)
	if err != nil {
		return model.Group{}, err
	}
	return c.group(ctx, info), nil
}

func (c *Client) group(ctx context.Context, info *types.GroupInfo) model.Group {
	g := model.Group{JID: info.JID.String(), Name: info.Name, Description: info.Topic, Created: info.GroupCreated.UTC(),
		Announce: info.IsAnnounce, Locked: info.IsLocked}
	for _, p := range info.Participants {
		member := c.canonical(ctx, cmp.Or(p.PhoneNumber, p.JID)).String()
		g.Members = append(g.Members, model.Member{JID: member, Name: c.name(ctx, member), Admin: p.IsAdmin || p.IsSuperAdmin})
	}
	return g
}

// CreateGroup creates a group with the owner and members, and returns its JID.
func (c *Client) CreateGroup(ctx context.Context, name string, members []string) (string, error) {
	jids, err := parseJIDs(members)
	if err != nil {
		return "", err
	}
	info, err := c.cli.CreateGroup(ctx, whatsmeow.ReqCreateGroup{Name: name, Participants: jids})
	if err != nil {
		return "", err
	}
	return info.JID.String(), nil
}

// JoinGroup joins a group by its invite link, and returns its JID.
func (c *Client) JoinGroup(ctx context.Context, link string) (string, error) {
	jid, err := c.cli.JoinGroupWithLink(ctx, strings.TrimPrefix(link, whatsmeow.InviteLinkPrefix))
	if err != nil {
		return "", err
	}
	return jid.String(), nil
}

// ChangeGroup applies one change to a group and says what happened. WhatsApp allows most changes
// only to the group's admins.
func (c *Client) ChangeGroup(ctx context.Context, chat string, change model.GroupChange) (string, error) {
	jid, err := parseChat(chat)
	if err != nil {
		return "", err
	}
	act, ok := groupActions[change.Action]
	if !ok {
		return "", fmt.Errorf("no group action %q", change.Action)
	}
	return act(ctx, c.cli, jid, change)
}

type groupAction func(ctx context.Context, cli *whatsmeow.Client, jid types.JID, change model.GroupChange) (string, error)

// groupActions holds the code for each action of model.GroupActions.
var groupActions = map[string]groupAction{
	"rename": func(ctx context.Context, cli *whatsmeow.Client, jid types.JID, change model.GroupChange) (string, error) {
		return "renamed", cli.SetGroupName(ctx, jid, change.Text)
	},
	"describe": func(ctx context.Context, cli *whatsmeow.Client, jid types.JID, change model.GroupChange) (string, error) {
		return "described", cli.SetGroupTopic(ctx, jid, "", "", change.Text)
	},
	"add":     memberChange(whatsmeow.ParticipantChangeAdd),
	"remove":  memberChange(whatsmeow.ParticipantChangeRemove),
	"promote": memberChange(whatsmeow.ParticipantChangePromote),
	"demote":  memberChange(whatsmeow.ParticipantChangeDemote),
	"announce": func(ctx context.Context, cli *whatsmeow.Client, jid types.JID, change model.GroupChange) (string, error) {
		return fmt.Sprintf("only admins write: %v", change.On), cli.SetGroupAnnounce(ctx, jid, change.On)
	},
	"lock": func(ctx context.Context, cli *whatsmeow.Client, jid types.JID, change model.GroupChange) (string, error) {
		return fmt.Sprintf("only admins change the details: %v", change.On), cli.SetGroupLocked(ctx, jid, change.On)
	},
	"invite_link": func(ctx context.Context, cli *whatsmeow.Client, jid types.JID, _ model.GroupChange) (string, error) {
		return cli.GetGroupInviteLink(ctx, jid, false)
	},
	"reset_invite_link": func(ctx context.Context, cli *whatsmeow.Client, jid types.JID, _ model.GroupChange) (string, error) {
		return cli.GetGroupInviteLink(ctx, jid, true)
	},
	"leave": func(ctx context.Context, cli *whatsmeow.Client, jid types.JID, _ model.GroupChange) (string, error) {
		return "left", cli.LeaveGroup(ctx, jid)
	},
}

// memberChange adds, removes, promotes or demotes members, and lists the members that WhatsApp
// refused with its error code.
func memberChange(kind whatsmeow.ParticipantChange) groupAction {
	return func(ctx context.Context, cli *whatsmeow.Client, jid types.JID, change model.GroupChange) (string, error) {
		members, err := parseJIDs(change.Members)
		if err != nil {
			return "", err
		}
		result, err := cli.UpdateGroupParticipants(ctx, jid, members, kind)
		if err != nil {
			return "", err
		}
		return memberResult(string(kind), result), nil
	}
}

func memberResult(kind string, result []types.GroupParticipant) string {
	var refused []string
	for _, p := range result {
		if p.Error != 0 {
			refused = append(refused, fmt.Sprintf("%s (error %d)", cmp.Or(p.PhoneNumber, p.JID).User, p.Error))
		}
	}
	done := fmt.Sprintf("%s: %d of %d", kind, len(result)-len(refused), len(result))
	if len(refused) > 0 {
		done += "; refused " + strings.Join(refused, ", ")
	}
	return done
}

func parseJIDs(addresses []string) ([]types.JID, error) {
	jids := make([]types.JID, len(addresses))
	for i, a := range addresses {
		jid, err := parseChat(a)
		if err != nil {
			return nil, err
		}
		jids[i] = jid
	}
	return jids, nil
}
