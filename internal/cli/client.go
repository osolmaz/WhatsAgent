package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/osolmaz/WhatsAgent/internal/api"
	"github.com/osolmaz/WhatsAgent/internal/core"
	"github.com/osolmaz/WhatsAgent/internal/model"
	"github.com/osolmaz/WhatsAgent/internal/paths"
)

// daemonClient returns an API client for the daemon socket of env.
func daemonClient(env paths.Env) (*api.Client, error) {
	dirs, err := paths.Resolve(env)
	if err != nil {
		return nil, err
	}
	socket, err := dirs.Socket()
	if err != nil {
		return nil, err
	}
	return api.NewClient(socket), nil
}

// ownerTool calls a tool as the owner and decodes the result into out.
func ownerTool(ctx context.Context, env paths.Env, tool string, args, out any) error {
	return agentTool(ctx, env, "", tool, args, out)
}

// agentTool calls a tool with the owner's rights for the agent session named session, or as the
// owner when session is empty.
func agentTool(ctx context.Context, env paths.Env, session, tool string, args, out any) error {
	client, err := daemonClient(env)
	if err != nil {
		return err
	}
	return client.As(session).Tool(ctx, "", tool, args, out)
}

func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// oneLine keeps a message on one line, so a Monitor turns each message into one notification.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", " ⏎ ")
}

func printMessages(w io.Writer, msgs []model.Message, withChat bool) {
	for _, m := range msgs {
		chat := ""
		if withChat {
			chat = m.Chat
		}
		fmt.Fprintln(w, oneLine(model.Line(m, chat, time.Local)))
	}
}

func newStatus(env paths.Env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the daemon's state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if asJSON {
				var s core.Status
				if err := ownerTool(cmd.Context(), env, "status", struct{}{}, &s); err != nil {
					return err
				}
				return printJSON(cmd.OutOrStdout(), s)
			}
			return printText(cmd, env, "command", api.CommandArgs{Text: "/status"})
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

// printText calls a tool whose result is text and prints it.
func printText(cmd *cobra.Command, env paths.Env, tool string, args any) error {
	return printAgentText(cmd, env, "", tool, args)
}

func printAgentText(cmd *cobra.Command, env paths.Env, session, tool string, args any) error {
	var t api.Text
	if err := agentTool(cmd.Context(), env, session, tool, args, &t); err != nil {
		return err
	}
	_, err := fmt.Fprintln(cmd.OutOrStdout(), t.Text)
	return err
}

func newCommand(env paths.Env) *cobra.Command {
	return &cobra.Command{
		Use:   "command <command line>",
		Short: "Run a self-chat command, such as \"/route Sam billing\"",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return printText(cmd, env, "command", api.CommandArgs{Text: strings.Join(args, " ")})
		},
	}
}

func newSend(env paths.Env) *cobra.Command {
	var to, text, file, replyTo, session string
	cmd := &cobra.Command{
		Use:   "send --to <chat> [--text <text>] [--file <path>] [--reply-to <id>] [--session <name>]",
		Short: "Send a message or a file as the owner, or for an agent session with --session",
		Long: "Send a message; the text comes from --text or, without --file, from standard input. With\n" +
			"--file, WhatsAgent sends the file with the text as its caption: JPEG and PNG as photos, MP4 as\n" +
			"a video, Ogg/Opus as a voice message, MP3, M4A and AAC as audio, and other files as documents.\n" +
			"@ and a phone number in the text mentions that person. Without --session it goes out exactly\n" +
			"as written. With --session it is an agent's message, and WhatsAgent puts the agent prefix\n" +
			"(see /prefix) in front, so the agent writes only the message. The CLI keeps the owner's\n" +
			"rights either way, so the send rules do not apply.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			args, err := sendInput(cmd.InOrStdin(), to, text, file)
			if err != nil {
				return err
			}
			args.ReplyTo = replyTo
			return printAgentText(cmd, env, session, "send", args)
		},
	}
	cmd.Flags().StringVar(&to, "to", "", "the chat: a JID, a phone number, \"self\" or part of its name")
	cmd.Flags().StringVar(&text, "text", "", "the message text, or the caption of the file")
	cmd.Flags().StringVar(&file, "file", "", "a file to send, up to 100 MB")
	cmd.Flags().StringVar(&replyTo, "reply-to", "", "the ID of the message in the chat to answer")
	cmd.Flags().StringVar(&session, "session", "", "send as this agent session, with the agent prefix")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

// sendInput builds the arguments of a send: the file at path when path is set, and the text
// from stdin when neither the text nor a file is given.
func sendInput(stdin io.Reader, to, text, path string) (api.SendArgs, error) {
	args := api.SendArgs{Chat: to, Text: text}
	if path != "" {
		file, err := api.ReadFile(path)
		args.File = file
		return args, err
	}
	if text != "" {
		return args, nil
	}
	data, err := io.ReadAll(stdin)
	args.Text = strings.TrimRight(string(data), "\n")
	return args, err
}

// flag is one string flag of a tool command, which fills one field of the tool's arguments.
type flag struct {
	name, usage string
	value       *string
	required    bool
}

// toolCommand returns a command that calls tool as the owner, with args filled from the flags,
// and prints the tool's text result.
func toolCommand(env paths.Env, use, short, tool string, args any, flags ...flag) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printText(cmd, env, tool, args)
		}}
	for _, f := range flags {
		cmd.Flags().StringVar(f.value, f.name, "", f.usage)
		if f.required {
			_ = cmd.MarkFlagRequired(f.name)
		}
	}
	return cmd
}

// messageFlags are the flags that name one message.
func messageFlags(chat, id *string) []flag {
	return []flag{{"chat", "the chat of the message", chat, true}, {"id", "the message ID", id, true}}
}

func newReact(env paths.Env) *cobra.Command {
	var a api.MessageArgs
	return toolCommand(env, "react --chat <chat> --id <message id> --emoji <emoji>", "React to a message as the owner",
		"react", &a, append(messageFlags(&a.Chat, &a.MessageID),
			flag{"emoji", "the emoji; empty removes the reaction", &a.Emoji, false})...)
}

func newEdit(env paths.Env) *cobra.Command {
	var a api.EditArgs
	return toolCommand(env, "edit --chat <chat> --id <message id> --text <text>",
		"Change the text of one of the owner's messages, up to 15 minutes after it was sent", "edit_message", &a,
		append(messageFlags(&a.Chat, &a.MessageID), flag{"text", "the new text", &a.Text, true})...)
}

func newDelete(env paths.Env) *cobra.Command {
	var a api.MessageArgs
	return toolCommand(env, "delete --chat <chat> --id <message id>",
		"Delete a message for everyone: the owner's own, or anyone's in a group the owner administers",
		"delete_message", &a, messageFlags(&a.Chat, &a.MessageID)...)
}

func newForward(env paths.Env) *cobra.Command {
	var a api.ForwardArgs
	return toolCommand(env, "forward --chat <chat> --id <message id> --to <chat> [--text <comment>]",
		"Forward a message to another chat, after an optional comment", "forward_message", &a,
		append(messageFlags(&a.Chat, &a.MessageID), flag{"to", "the chat to forward it to", &a.To, true},
			flag{"text", "a comment that goes before the forwarded message", &a.Text, false})...)
}

func newMedia(env paths.Env) *cobra.Command {
	var args api.MessageArgs
	var output string
	cmd := &cobra.Command{
		Use:   "media --chat <chat> --id <message id> [--output <file>]",
		Short: "Download the media of a message",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var t api.Text
			if err := ownerTool(cmd.Context(), env, "download_media", args, &t); err != nil {
				return err
			}
			if output == "" {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), t.Text)
				return err
			}
			data, err := os.ReadFile(t.Text)
			if err != nil {
				return err
			}
			if err := os.WriteFile(output, data, 0o600); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), output)
			return err
		},
	}
	cmd.Flags().StringVar(&args.Chat, "chat", "", "the chat")
	cmd.Flags().StringVar(&args.MessageID, "id", "", "the message ID")
	cmd.Flags().StringVarP(&output, "output", "o", "", "copy the file here")
	_ = cmd.MarkFlagRequired("chat")
	_ = cmd.MarkFlagRequired("id")
	return cmd
}

func newChats(env paths.Env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "chats [query]",
		Short: "List chats whose name or JID contains the query",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var chats []model.Chat
			if err := ownerTool(cmd.Context(), env, "list_chats", api.QueryArgs{Query: strings.Join(args, " "), Limit: 1000}, &chats); err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), chats)
			}
			for _, c := range chats {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", c.JID, c.Name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newMessages(env paths.Env) *cobra.Command {
	var args api.ChatArgs
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "messages --chat <chat> [--limit n]",
		Short: "Print the latest messages of a chat",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var r core.ChatMessages
			if err := ownerTool(cmd.Context(), env, "read_chat", args, &r); err != nil {
				return err
			}
			if asJSON {
				return printJSON(cmd.OutOrStdout(), r)
			}
			printMessages(cmd.OutOrStdout(), r.Messages, false)
			return nil
		},
	}
	cmd.Flags().StringVar(&args.Chat, "chat", "", "the chat")
	cmd.Flags().IntVar(&args.Limit, "limit", 50, "how many messages")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("chat")
	return cmd
}

func newSearch(env paths.Env) *cobra.Command {
	var args api.SearchArgs
	cmd := &cobra.Command{
		Use:   "search <text>",
		Short: "Search stored messages",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, words []string) error {
			args.Query = strings.Join(words, " ")
			var msgs []model.Message
			if err := ownerTool(cmd.Context(), env, "search", args, &msgs); err != nil {
				return err
			}
			printMessages(cmd.OutOrStdout(), msgs, true)
			return nil
		},
	}
	cmd.Flags().StringVar(&args.Chat, "chat", "", "search in this chat only")
	cmd.Flags().IntVar(&args.Limit, "limit", 30, "how many messages")
	return cmd
}

func newExport(env paths.Env) *cobra.Command {
	var chat string
	cmd := &cobra.Command{
		Use:   "export --chat <chat>",
		Short: "Print every stored message of a chat as JSON lines, oldest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var msgs []model.Message
			if err := ownerTool(cmd.Context(), env, "export", api.ChatArgs{Chat: chat}, &msgs); err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetEscapeHTML(false)
			for _, m := range msgs {
				if err := enc.Encode(m); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&chat, "chat", "", "the chat")
	_ = cmd.MarkFlagRequired("chat")
	return cmd
}

func newChanges(env paths.Env) *cobra.Command {
	var cursor int64
	cmd := &cobra.Command{
		Use:   "changes [--since <cursor>]",
		Short: "Print the chats with stored changes after a cursor, and the next cursor, as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ch core.Changes
			if err := ownerTool(cmd.Context(), env, "changes", api.ChangesArgs{Cursor: cursor}, &ch); err != nil {
				return err
			}
			return printJSON(cmd.OutOrStdout(), ch)
		},
	}
	cmd.Flags().Int64Var(&cursor, "since", 0, "the cursor from the last call; 0 lists every chat")
	return cmd
}

func newWatch(env paths.Env) *cobra.Command {
	var session string
	var chats []string
	var since int64
	cmd := &cobra.Command{
		Use:   "watch [--session <name>] [--chat <chat>]...",
		Short: "Print each new message of a session's routed chats and the given chats as one line",
		Long: "Print each new message of a session's routed chats, plus the --chat chats, as one line,\n" +
			"until stopped. WhatsAgent keeps the session's place, so a restart misses nothing. Run it with\n" +
			"Claude Code's Monitor tool to wake a session that runs without the WhatsAgent channel.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := daemonClient(env)
			if err != nil {
				return err
			}
			if session == "" {
				session = "watch-" + strings.Join(chats, "-")
			}
			out := cmd.OutOrStdout()
			return client.Watch(cmd.Context(), session, chats, since, func(f api.Frame) error {
				for _, e := range f.Events {
					fmt.Fprintln(out, oneLine(model.Line(e.Message, e.ChatName, time.Local)))
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "the session name whose routes to watch")
	cmd.Flags().StringArrayVar(&chats, "chat", nil, "a chat to watch as well (repeatable)")
	cmd.Flags().Int64Var(&since, "since", -1, "start after this event number; 0 replays every stored message")
	return cmd
}
