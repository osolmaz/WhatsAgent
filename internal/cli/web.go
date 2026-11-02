package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/osolmaz/WhatsAgent/internal/paths"
)

func newWeb(env paths.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "web",
		Short: "Open the browser page with the agents that WhatsAgent runs, on this computer",
		Long: "Ask the daemon to open its browser page in this computer's default browser. The page shows each\n" +
			"agent that WhatsAgent runs, such as home and the chat agents, in a terminal you can watch and type\n" +
			"into. Closing the tab leaves the agents running. The page's link is a way to see messages, so\n" +
			"WhatsAgent sends it only in the self-chat: send /web there to open the page on another device.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := daemonClient(env)
			if err != nil {
				return err
			}
			opened, err := client.OpenWeb(cmd.Context())
			if err != nil {
				return err
			}
			if !opened {
				return errors.New("this computer shows no browser; send /web in the self-chat to get the page's link")
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Opened the browser page on this computer.")
			return err
		},
	}
}
