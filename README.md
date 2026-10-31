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
| `/route <chat> <session>` | Send a chat's new messages to a session |
| `/allow <session> <chat>` | Let a session send to a chat without asking; `/ask` and `/deny` set the other rules |

When a session wants to send somewhere its rules do not allow, WhatsAgent asks you in the self-chat, and you answer `yes <code>` or `no <code>`.

A session receives its routed messages in one of three ways:

- `whatsagent claude` starts Claude Code with the WhatsAgent channel, so messages arrive in the session directly.
- Inside any Claude Code session, run `whatsagent watch --session <name> --chat <chat>` with the Monitor tool. Each new message wakes the session.
- `whatsagent codex` starts the Codex TUI on the Codex app-server daemon, and WhatsAgent starts a turn in its thread for each batch of messages.

Scripts can use the daemon too: `whatsagent send --to <chat> --text <text>`, `react`, `media`, `messages`, `search`, `export` and `changes`. `whatsagent command "/route Sam billing"` runs any self-chat command.

How it works, and what it does not do yet, is in the [plan](docs/2026-10-31-whatsagent-plan.md).
