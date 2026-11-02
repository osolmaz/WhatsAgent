package cli

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/osolmaz/WhatsAgent/internal/api"
	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/paths"
)

// jsonCommand returns a command that calls tool as the owner, with args filled from the flags,
// and prints the tool's result as JSON.
func jsonCommand(env paths.Env, use, short, tool string, args any, flags ...flag) *cobra.Command {
	cmd := toolCommand(env, use, short, tool, args, flags...)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		var result json.RawMessage
		if err := ownerTool(cmd.Context(), env, tool, args, &result); err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), result)
	}
	return cmd
}

// newGroup returns the group command: info, create and join, and one subcommand for each change
// in model.GroupActions.
func newGroup(env paths.Env) *cobra.Command {
	cmd := &cobra.Command{Use: "group", Short: "Show, create, join and change WhatsApp groups as the owner"}
	var info api.ChatArgs
	var create api.CreateGroupArgs
	var join api.LinkArgs
	createCmd := toolCommand(env, "create --name <name> --member <person>...", "Create a group with the owner and the members",
		"create_group", &create, flag{"name", "the group's name, up to 25 characters", &create.Name, true})
	createCmd.Flags().StringArrayVar(&create.Members, "member", nil, "a member, by phone number or JID (repeatable)")
	cmd.AddCommand(
		jsonCommand(env, "info --chat <group>", "Show a group's details and members as JSON", "group_info", &info,
			flag{"chat", "the group", &info.Chat, true}),
		createCmd,
		toolCommand(env, "join --link <invite link>", "Join a group by its invite link", "join_group", &join,
			flag{"link", "the invite link", &join.Link, true}))
	for _, action := range slices.Sorted(maps.Keys(model.GroupActions)) {
		cmd.AddCommand(newGroupChange(env, action))
	}
	return cmd
}

// newGroupChange returns the subcommand for one group action, with the flag that its argument
// needs.
func newGroupChange(env paths.Env, action string) *cobra.Command {
	a := api.GroupArgs{GroupChange: model.GroupChange{Action: action}}
	cmd := toolCommand(env, strings.ReplaceAll(action, "_", "-")+" --chat <group>",
		"Change a group: "+strings.ReplaceAll(action, "_", " "), "manage_group", &a, flag{"chat", "the group", &a.Chat, true})
	switch model.GroupActions[action] {
	case "text":
		cmd.Flags().StringVar(&a.Text, "text", "", "the new name or description")
		_ = cmd.MarkFlagRequired("text")
	case "members":
		cmd.Flags().StringArrayVar(&a.Members, "member", nil, "a member, by phone number or JID (repeatable)")
		_ = cmd.MarkFlagRequired("member")
	case "on":
		cmd.Flags().BoolVar(&a.On, "on", false, "turn it on; without --on it turns off")
	}
	return cmd
}

func newContacts(env paths.Env) *cobra.Command {
	var asJSON bool
	var limit int
	cmd := &cobra.Command{
		Use:   "contacts <query>",
		Short: "Find people in the address book by name or number; a phone number is also checked on WhatsApp",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var found []model.Contact
			if err := ownerTool(cmd.Context(), env, "find_contacts", api.QueryArgs{Query: strings.Join(args, " "), Limit: limit}, &found); err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), found)
			}
			for _, c := range found {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", c.JID, contactLabel(c))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().IntVar(&limit, "limit", 30, "how many people")
	return cmd
}

func contactLabel(c model.Contact) string {
	label := cmp.Or(c.Name, c.PushName, c.Business)
	if c.OnWhatsApp != nil {
		label = strings.TrimSpace(label + map[bool]string{true: " (on WhatsApp)", false: " (not on WhatsApp)"}[*c.OnWhatsApp])
	}
	return label
}

func newContact(env paths.Env) *cobra.Command {
	var a api.PersonArgs
	return jsonCommand(env, "contact --person <person>", "Show what WhatsApp shows of a person, as JSON", "contact_info", &a,
		flag{"person", "the person, by phone number or JID", &a.Person, true})
}

func newProfile(env paths.Env) *cobra.Command {
	var p model.Profile
	var picture string
	cmd := &cobra.Command{
		Use:   "profile [--name <name>] [--about <text>] [--picture <photo>]",
		Short: "Change the owner's WhatsApp name, about text or picture",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if picture != "" {
				file, err := api.ReadFile(picture)
				if err != nil {
					return err
				}
				p.Picture = file
			}
			return printText(cmd, env, "set_profile", p)
		},
	}
	cmd.Flags().StringVar(&p.Name, "name", "", "the new name")
	cmd.Flags().StringVar(&p.About, "about", "", "the new about text")
	cmd.Flags().StringVar(&picture, "picture", "", "a JPEG or PNG photo; its centered square becomes the picture")
	return cmd
}
