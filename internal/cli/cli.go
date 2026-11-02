// Package cli defines the whatsagent command line.
package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/osolmaz/WhatsAgent/internal/paths"
)

// version is set at build time with
// -ldflags "-X github.com/osolmaz/WhatsAgent/internal/cli.version=<version>".
var version = "dev"

// Main runs the whatsagent command line with args and returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	env, err := paths.FromOS()
	if err != nil {
		fmt.Fprintln(stderr, "whatsagent:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	root := NewRoot(stdout, log, env)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(stderr, "whatsagent:", err)
		return 1
	}
	return 0
}

// NewRoot returns the whatsagent root command for the environment env.
func NewRoot(stdout io.Writer, log *slog.Logger, env paths.Env) *cobra.Command {
	root := &cobra.Command{
		Use:           "whatsagent",
		Short:         "A bridge between a WhatsApp account and Claude Code and Codex sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.AddCommand(newVersion(), newPaths(env), newLink(env, log), newDaemon(env), newService(env),
		newStatus(env), newCommand(env), newSend(env), newEdit(env), newDelete(env), newForward(env),
		newPoll(env), newLocation(env), newRead(env), newReact(env), newMedia(env), newChats(env),
		newMessages(env), newSearch(env), newExport(env), newChanges(env), newWatch(env),
		newChannel(env), newSetup(), newWeb(env))
	root.AddCommand(newLaunches()...)
	return root
}

func newVersion() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the WhatsAgent version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version)
			return err
		},
	}
}

func newPaths(env paths.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "paths",
		Short: "Print the folders and files WhatsAgent uses on this machine",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dirs, err := paths.Resolve(env)
			if err != nil {
				return err
			}
			socket, err := dirs.Socket()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(),
				"data:     %s\nconfig:   %s\nsession:  %s\nstate:    %s\nsocket:   %s\n",
				dirs.Data, dirs.Config, dirs.SessionDB(), dirs.StateDB(), socket)
			return err
		},
	}
}
