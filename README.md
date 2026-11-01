# WhatsAgent

WhatsAgent is a bridge between a WhatsApp account and the coding agent sessions that the account's owner runs in Claude Code and Codex. You control it by messaging yourself on WhatsApp: messages that start with `/` are commands for WhatsAgent, and every other message goes to home, an agent session that WhatsAgent keeps running. A command such as `/route` sends a chat's new messages to one of your sessions, and send rules decide which sessions may write to which chats.

WhatsAgent runs on Linux and macOS. It is not affiliated with or endorsed by WhatsApp or Meta.

## Install

WhatsAgent needs Go 1.26.10 or later to build:

```sh
go install github.com/osolmaz/WhatsAgent/cmd/whatsagent@latest
```

Link it to your account as a WhatsApp linked device, then run the daemon as a user service (systemd on Linux, launchd on macOS):

```sh
whatsagent link                 # scan the QR code under WhatsApp > Linked devices
whatsagent link --phone +15551234567   # or pair with a code instead
whatsagent service install
```

`whatsagent link --import <session.db>` takes over the login of another whatsmeow-based tool instead of pairing again. Stop that tool first, because two programs on one linked device disconnect each other.

Give your agent sessions the WhatsApp tools:

```sh
whatsagent setup claude   # every Claude Code session gets the whatsagent MCP server
whatsagent setup codex    # the same for Codex
```

## Use

In your WhatsApp "Message Yourself" chat, send `/help` for the commands. The most useful ones:

| Command | Effect |
| --- | --- |
| `/status` | WhatsApp link, home, sessions and waiting messages |
| `/home claude` | Run home on Claude Code; `/home codex`, `/home off` and `/home new` also work |
| `/chats <query>` | Find a chat by name |
| `/route <chat> <session> [<mode>]` | Let a session handle a chat; the mode defaults to `all` |
| `/mode <chat> off\|keyword\|window\|all` | Set what agents see of a chat; `*` sets every chat |
| `/allow <session> <chat>` | Let a session send to a chat without asking; `/ask` and `/deny` set the other rules |

When a session wants to send somewhere its rules do not allow, WhatsAgent asks you in the self-chat, and you answer with its two-digit code, such as `yes 42` or `no 42`. Home and the chat agents approve their own tool calls with Claude Code's auto mode or Codex's auto review, and ask you only when the harness will not decide; `/auto off` makes them ask every time.

Each chat has a mode that decides what an agent sees of it. In the default `window` mode, the agent sees nothing until you wake it in the chat, for example with "Bob, wake up" or "Bob start"; its name alone does nothing. Then it can read the last 30 minutes and every new message for an hour. Naming it again extends that hour, and "Bob stop watching" ends it. The agent says in the chat when it starts monitoring and when it is done. Only your own messages can call it, never other people's. The `keyword` mode shows the agent only your messages that name it, `all` shows everything, and `off` shows nothing. A chat without a route gets its own agent, which can read only what the chat's mode shows. `/window` changes the window length, the lookback, the stop phrases and the two messages. The [chat modes specification](docs/2026-11-01-chat-modes.md) has the details.

Agents write only their message, and WhatsAgent puts a prefix in front of it so people can tell the agent's messages from yours. `/name Bob` names the agents, and the name is also their trigger word. `/prefix` sets the prefix as any text, with `{name}` and `{session}` filled in, `\n` for a line break, and WhatsApp formatting such as `*bold*`. For example, `/prefix 🤖 *{name}*:` makes a message read "🤖 **Bob**: On it.", and `/prefix off` turns the prefix off. A route can set its own prefix for one chat.

A session receives its routed messages in one of three ways:

- `whatsagent claude` starts Claude Code with the WhatsAgent channel, so messages arrive in the session directly.
- Inside any Claude Code session, run `whatsagent watch --session <name> --chat <chat>` with the Monitor tool. Each new message wakes the session.
- `whatsagent codex` starts the Codex TUI on the Codex app-server daemon, and WhatsAgent starts a turn in its thread for each batch of messages.

Home and the chat agents run as normal Claude Code sessions in terminals that the daemon keeps. `whatsagent web` opens them in your browser: a list of sessions on the left and the selected agent's terminal on the right, where you can watch it work and type into it. Closing the tab leaves them running.

Scripts can use the daemon too: `whatsagent send --to <chat> --text <text>`, `react`, `media`, `messages`, `search`, `export` and `changes`. A send goes out exactly as written; add `--session <name>` when an agent sends, and WhatsAgent puts the prefix in front. `whatsagent command "/route Sam billing"` runs any self-chat command.

How it works, and what it does not do yet, is in the [plan](docs/2026-10-31-whatsagent-plan.md).
