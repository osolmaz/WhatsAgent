package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/osolmaz/WhatsAgent/internal/browser"
	"github.com/osolmaz/WhatsAgent/internal/paths"
)

// openBrowser opens url in the default browser and reports whether it could.
func openBrowser(env paths.Env, url string) bool {
	args := browser.Command(env.GOOS, func(k string) string { return getenv(env, k) }, url)
	if args == nil {
		return false
	}
	return startCommand(args[0], args[1:]...) == nil
}

func newWeb(env paths.Env) *cobra.Command {
	var print bool
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Open the sessions that WhatsAgent started, such as home, in the browser",
		Long: "Open the daemon's browser page in the default browser. It shows each session that WhatsAgent\n" +
			"started itself, such as home and the chat agents, in a terminal you can watch and type into.\n" +
			"Closing the tab leaves the sessions running. The address holds an access token, so keep it private.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := daemonClient(env)
			if err != nil {
				return err
			}
			url, err := client.WebURL(cmd.Context())
			if err != nil {
				return err
			}
			if !print && openBrowser(env, url) {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Opened %s\n", url)
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), url)
			return err
		},
	}
	cmd.Flags().BoolVar(&print, "print", false, "print the address without opening a browser")
	return cmd
}
