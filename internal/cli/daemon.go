package cli

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/osolmaz/WhatsAgent/internal/daemon"
	"github.com/osolmaz/WhatsAgent/internal/mcp"
	"github.com/osolmaz/WhatsAgent/internal/paths"
	"github.com/osolmaz/WhatsAgent/internal/service"
)

// Seams for tests: replacing a process and running a helper command.
var (
	execProcess = syscall.Exec
	runCommand  = func(name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		return cmd.Run()
	}
	executable = os.Executable
)

func getenv(env paths.Env, key string) string {
	if env.Getenv == nil {
		return ""
	}
	return env.Getenv(key)
}

func codexHome(env paths.Env) string {
	if dir := getenv(env, "CODEX_HOME"); dir != "" {
		return dir
	}
	return filepath.Join(env.Home, ".codex")
}

// webFlags are the options of the daemon's browser page, which `service install` passes on to
// the daemon.
type webFlags struct {
	host  string
	names []string
}

func (f *webFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.host, "web-host", "",
		"address the browser page listens on, such as a Tailscale address (default: loopback only)")
	cmd.Flags().StringArrayVar(&f.names, "web-name", nil,
		"host name the browser page may be opened under, such as a Tailscale MagicDNS name (repeatable)")
}

// args are the flags as daemon arguments.
func (f webFlags) args() []string {
	var args []string
	if f.host != "" {
		args = append(args, "--web-host", f.host)
	}
	for _, name := range f.names {
		args = append(args, "--web-name", name)
	}
	return args
}

func newDaemon(env paths.Env) *cobra.Command {
	var verbose bool
	var web webFlags
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the WhatsAgent daemon in the foreground",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dirs, err := paths.Resolve(env)
			if err != nil {
				return err
			}
			level := slog.LevelInfo
			if verbose {
				level = slog.LevelDebug
			}
			log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: level}))
			claude, _ := exec.LookPath("claude")
			self, err := executable()
			if err != nil {
				return err
			}
			return daemon.Run(cmd.Context(), daemon.Options{
				Dirs: dirs, Log: log, Version: version, ClaudeBin: claude, Bin: self,
				CodexSocket: daemon.CodexSocket(codexHome(env)), CodexEvery: 20 * time.Second,
				WebHost: web.host, WebNames: web.names, Tools: mcp.ToolNames(),
			})
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "log debug details")
	web.add(cmd)
	return cmd
}

func newService(env paths.Env) *cobra.Command {
	var web webFlags
	install := &cobra.Command{
		Use:   "install",
		Short: "Install the daemon as a systemd user service or launchd LaunchAgent and start it",
		Long: "Install the daemon as a systemd user service or launchd LaunchAgent and start it. --web-host and\n" +
			"--web-name go to the daemon, for example to open the browser page over Tailscale.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, spec, err := serviceSpec(env)
			if err != nil {
				return err
			}
			spec.Args = web.args()
			if err := service.Install(env.GOOS, path, spec, os.Getuid(), runCommand); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Installed and started", path)
			return err
		},
	}
	web.add(install)
	cmd := &cobra.Command{Use: "service", Short: "Install or remove the daemon as a user service"}
	cmd.AddCommand(install, &cobra.Command{
		Use:   "uninstall",
		Short: "Stop the daemon service and remove it",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _, err := serviceSpec(env)
			if err != nil {
				return err
			}
			if err := service.Uninstall(env.GOOS, path, os.Getuid(), runCommand); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "Removed", path)
			return err
		},
	})
	return cmd
}

func serviceSpec(env paths.Env) (string, service.Spec, error) {
	dirs, err := paths.Resolve(env)
	if err != nil {
		return "", service.Spec{}, err
	}
	bin, err := executable()
	if err != nil {
		return "", service.Spec{}, err
	}
	path, err := service.File(env.GOOS, env.Home, filepath.Dir(dirs.Config))
	spec := service.Spec{Bin: bin, Path: getenv(env, "PATH"), LogFile: filepath.Join(dirs.Data, "daemon.log")}
	return path, spec, err
}

func newChannel(env paths.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "channel",
		Short: "Run the WhatsAgent MCP server on standard input and output",
		Long: "Run the WhatsAgent MCP server that harness sessions load. WHATSAGENT_SESSION names the\n" +
			"session, WHATSAGENT_HARNESS names the harness, and WHATSAGENT_PUSH=channel turns on\n" +
			"Claude Code channel pushes.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := daemonClient(env)
			if err != nil {
				return err
			}
			folder, _ := os.Getwd()
			server := mcp.New(mcp.Options{
				Name:    getenv(env, "WHATSAGENT_SESSION"),
				Harness: daemon.HarnessFromEnv(func(k string) string { return getenv(env, k) }),
				Folder:  folder,
				Push:    getenv(env, "WHATSAGENT_PUSH") == "channel",
				Version: version,
			}, client, cmd.OutOrStdout())
			return server.Run(cmd.Context(), cmd.InOrStdin())
		},
	}
}

// harnessSetup holds the command that registers the MCP server with one harness.
var harnessSetup = map[string]func(bin string) [][]string{
	"claude": func(bin string) [][]string {
		return [][]string{
			{"claude", "mcp", "remove", "--scope", "user", "whatsagent"},
			{"claude", "mcp", "add", "--scope", "user", "whatsagent", "-e", "WHATSAGENT_HARNESS=claude", "--", bin, "channel"},
		}
	},
	"codex": func(bin string) [][]string {
		return [][]string{
			{"codex", "mcp", "remove", "whatsagent"},
			{"codex", "mcp", "add", "whatsagent", "--env", "WHATSAGENT_HARNESS=codex", "--", bin, "channel"},
		}
	},
}

func newSetup() *cobra.Command {
	return &cobra.Command{
		Use:       "setup claude|codex",
		Short:     "Register the WhatsAgent MCP server for every session of a harness",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"claude", "codex"},
		RunE: func(cmd *cobra.Command, args []string) error {
			steps, ok := harnessSetup[args[0]]
			if !ok {
				return fmt.Errorf("unknown harness %q: use claude or codex", args[0])
			}
			bin, err := executable()
			if err != nil {
				return err
			}
			commands := steps(bin)
			_ = runCommand(commands[0][0], commands[0][1:]...)
			if err := runCommand(commands[1][0], commands[1][1:]...); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Registered the WhatsAgent MCP server with %s for your user.\n", args[0])
			return err
		},
	}
}

// launch replaces this process with a harness, passing the extra arguments through.
func launch(bin string, lead []string, extraEnv string, args []string) error {
	path, err := exec.LookPath(bin)
	if err != nil {
		return err
	}
	environ := os.Environ()
	if extraEnv != "" {
		environ = append(environ, extraEnv)
	}
	return execProcess(path, append(append([]string{bin}, lead...), args...), environ)
}

// harnessLaunch is how `whatsagent claude` and `whatsagent codex` start their harness.
type harnessLaunch struct {
	bin      string
	short    string
	lead     []string
	extraEnv string
}

var launches = []harnessLaunch{
	{"claude", "Start Claude Code with the WhatsAgent channel, so routed messages wake the session",
		[]string{"--dangerously-load-development-channels", "server:whatsagent"}, "WHATSAGENT_PUSH=channel"},
	{"codex", "Start the Codex TUI on the Codex app-server daemon, so routed messages reach its thread",
		[]string{"--remote", "unix://"}, ""},
}

func newLaunches() []*cobra.Command {
	commands := make([]*cobra.Command, 0, len(launches))
	for _, l := range launches {
		commands = append(commands, &cobra.Command{
			Use:                l.bin + " [" + l.bin + " arguments]",
			Short:              l.short,
			DisableFlagParsing: true,
			RunE: func(_ *cobra.Command, args []string) error {
				return launch(l.bin, l.lead, l.extraEnv, args)
			},
		})
	}
	return commands
}
