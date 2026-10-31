---
date: 2026-10-31
author: Onur Solmaz <2453968+osolmaz@users.noreply.github.com>
title: WhatsAgent plan
tags: [plan, architecture, whatsapp, mcp, claude-code, codex]
---

# WhatsAgent plan

## TL;DR

WhatsAgent is a bridge between one WhatsApp account and the coding agent sessions that the account's owner runs in Claude Code and Codex. The owner controls it from their own "Message Yourself" chat, where messages that start with `/` are commands for WhatsAgent and every other message goes to a background session called home. Each harness gets its own adapter, which wakes a session through Claude Code channels or through the Codex app-server daemon. An existing whatsmeow login can be imported, so an owner can switch from another whatsmeow-based tool without scanning a new QR code. It runs on Linux and macOS, and the build goes straight to the final setup, checking self-chat delivery and Codex pushes live during the first runs.

## Introduction

This document describes how WhatsAgent works and the order in which to build it. It covers the first version, which the owner controls from WhatsApp itself and which runs on Linux and macOS. Claude Code and Codex are both supported from the first version through harness-specific adapters. The design leaves room for a desktop app later, and for other harnesses once the MCP Events extension is stable.

## Terms

Code and docs use one name for each concept:

| Term | Meaning |
| --- | --- |
| WhatsAgent | The daemon that holds the WhatsApp connection and does all routing. The binary is `whatsagent`. |
| owner | The person whose WhatsApp account WhatsAgent is linked to. |
| self-chat | The owner's "Message Yourself" chat, which is WhatsAgent's control room. |
| harness | An agent program, such as Claude Code or Codex. |
| session | One running agent session in a harness, such as a Claude Code session or a Codex thread. The owner opens it. |
| home | The one session that WhatsAgent starts and keeps running. |
| route | A link from one WhatsApp chat to one session. |
| send rule | A rule that decides whether a session may send to a chat: `allow`, `ask` or `deny`. |
| command | A self-chat message that starts with `/`. WhatsAgent handles it, and no agent sees it. |
| check-in | A session becoming known to WhatsAgent, through the WhatsAgent MCP server or through the harness's own API. |
| adapter | The WhatsAgent code for one harness. It handles check-in, pushes, approval prompts and home for that harness. |

## Architecture

WhatsAgent links to the owner's account as a WhatsApp linked device, using [whatsmeow](https://github.com/tulir/whatsmeow), the Go library behind several open source WhatsApp tools. It stores every message in an event log and uses the routes to decide which session gets it. Sessions never talk to WhatsApp directly. They read and send through WhatsAgent's MCP tools, which lets WhatsAgent apply its rules to every send. Everything that differs between harnesses lives in the adapters.

```text
              owner's phone: self-chat
       /commands -> WhatsAgent    plain text -> home
                        |
+----------- WhatsAgent daemon (Go, whatsmeow) -------------+
| WhatsApp link, message store, event log, routes           |
| command core <- self-chat parser, MCP admin tools, GUI    |
| session registry: who is checked in, who receives pushes  |
| MCP endpoint: WhatsApp tools + events (SEP-3415 shape)    |
| adapters: Claude Code (channels, stream-json)             |
|           Codex (app-server daemon socket)                |
+--------+---------------------------+----------------------+
         | starts, keeps running     | check-in, pushes
       home                    sessions the owner opens:
  (Claude Code or Codex)       Claude Code CLI and Desktop,
                               Codex TUI, other MCP apps
```

## Onboarding

`whatsagent link` prints a QR code in the terminal, which the owner scans in WhatsApp under Linked devices. `--phone <number>` uses WhatsApp's phone-number pairing code instead, and `--qr-format text` prints the raw QR payload for another renderer. If the connection drops before pairing completes, WhatsAgent retries with a fresh code. During pairing the phone sends about three months of recent history by default, and `--history-days` limits it. With `--events`, each step goes out as one JSON line that includes the QR payload or pairing code, so the desktop app can show the same flow later.

`whatsagent link --import <session.db>` copies the login from an existing whatsmeow session store instead of scanning a new code, as the "Login import" section describes.

After linking, the owner starts `whatsagent daemon`. On its first start the daemon posts the welcome message in the self-chat, followed by the output of `/help`. `whatsagent setup claude` registers the WhatsAgent plugin with Claude Code, and `whatsagent setup codex` adds the WhatsAgent MCP server to Codex with `codex mcp add`. From then on, the owner starts push sessions with `whatsagent claude` and `whatsagent codex`.

## Self-chat

The self-chat is where the owner controls WhatsAgent and talks to home. Four kinds of message appear there.

Commands start with `/`. WhatsAgent answers them at once without calling a model, so they are free and always behave the same way. The first set is small:

| Command | Effect |
| --- | --- |
| `/help` | List the commands. |
| `/status` | Show the WhatsApp link, home, checked-in sessions and waiting messages. |
| `/sessions` | List sessions with their harness, name, folder and push status. |
| `/chats <query>` | Find a chat by name, for use in `/route`. |
| `/route <chat> <session>` | Send a chat's new messages to a session. |
| `/unroute <chat>` | Stop sending a chat's messages to any session. |
| `/rename <session> <name>` | Give a session a stable name. |
| `/home claude`, `/home codex`, `/home off` | Run home on Claude Code or Codex, or stop it. |
| `/home new` | Start a fresh home session in place of the current one. |
| `/allow`, `/ask`, `/deny <session> <chat>` | Set a send rule, with `*` for every session or every chat. |
| `/rules` | List the send rules. |

Plain text goes to home, and home replies in the self-chat.

Status lines come from WhatsAgent itself. The first one is a welcome message when the account links. After that, WhatsAgent posts a line whenever its state changes, for example when a session checks in or when messages start to wait for an offline session. Lines that arrive close together are merged into one message so the chat stays readable. WhatsAgent's lines and home's replies carry different prefixes, so the owner can tell them apart.

Approval prompts are the fourth kind. When a session needs approval for a tool call, its adapter can relay the prompt to the self-chat. The owner then answers with `yes abcde` or `no abcde`. The "Adapters" section says how each harness supports this.

WhatsAgent accepts commands only from the owner and only in the self-chat. The owner's messages and WhatsAgent's own messages both appear there as sent by the owner's account, so WhatsAgent records the ID of every message it sends and never treats one of them as a command or a prompt.

### Self-chat delivery risk

Sending into Message Yourself from a linked device may not work reliably, because WhatsApp can accept such a message and never show it in Message Yourself. If WhatsAgent sees this behavior, the control room moves to a WhatsApp group whose only member is the owner. Nothing else in this plan changes.

## Sessions

The owner opens and closes sessions. The only session WhatsAgent starts is home.

Every harness loads the WhatsAgent MCP server, a small stdio server that the `whatsagent channel` subcommand runs. Claude Code loads it from the WhatsAgent plugin, and Codex loads it from its `config.toml`, after `whatsagent setup claude` or `whatsagent setup codex` registers it. Both register it for the owner's user account, so every session of that harness gets the WhatsApp tools, including sessions started without `whatsagent claude` or `whatsagent codex`. When a session starts, the server connects to the WhatsAgent daemon over a local socket and checks in with the session's harness, folder and name. When the session ends, the socket closes and WhatsAgent marks the session offline, so it needs no process scanning. An adapter can add more check-in data from its harness's own API, as the Codex adapter does.

A push session is one that its adapter can wake when a routed message arrives. A tools-only session can read and send through the MCP tools when the owner asks it to, but a new message does not wake it. Which sessions can receive pushes depends on the harness and on how the owner started the session, as the "Adapters" section describes.

Routes point at session names, so a route survives when its session restarts. A session's default name is its folder name, and `/rename` or a request inside the session changes it. When the owner closes a session and resumes it later, it checks in under the same name and its routes work again. Messages for an offline session wait in the event log while the self-chat shows how many are waiting.

## Routes

By default, only the self-chat reaches an agent, through home. WhatsAgent stores every other chat, and home can read them when asked ("what did Sam say today?"), but no session gets them pushed.

A route links one chat to one session. Each chat has at most one route, so it is always clear which session answers it. Owners who want everything in one place can send every chat to home with `/route * home`. A route can deliver every message or only the messages addressed to the agent, meaning messages that name the agent or reply to an agent message. It can also set a reply prefix and choose whether the session may reply or only read.

The owner can create a route in three ways, and all of them run the same code. The first is the `/route` command. The second is a request to home, such as "send my chat with Sam to the billing-api session". The third is a request inside a session, such as "connect this session to my chat with Sam", which makes the agent call the `route_here` tool.

## Home

Home is the only session that WhatsAgent starts. It runs on Claude Code or on Codex, as the owner chooses with `/home claude` or `/home codex`, and its adapter starts it. Home receives the owner's plain-text self-chat messages and has the WhatsApp tools and the admin commands. Home's sends follow the send rules like any other session's, so by default a send from home to a chat without a route waits for the owner's `yes`. Owners who want commands only can turn home off with `/home off`.

Home is one long-lived session that survives restarts. When an adapter starts home for the first time, WhatsAgent stores the session's ID in its database. The Claude Code adapter chooses a UUID and passes it with `--session-id`, and the Codex adapter keeps the thread ID that `thread/start` returns. After a WhatsAgent restart or a reboot, the adapter continues the same session with `claude -p --resume <id>` or `thread/resume`, so home keeps its context. Both harnesses compact a long conversation on their own, and `/home new` replaces home with a fresh session when the owner wants a clean start. Each harness keeps its own home, so switching to `/home codex` and back to `/home claude` continues the earlier Claude Code home.

The owner can watch a Codex home live by attaching a TUI to the same daemon thread. A Claude Code home must be stopped with `/home off` before the owner opens it in a terminal, because two Claude Code processes must not write to one session.

## Command core

Each command is defined once, with its name, arguments, who may run it, and its handler. The self-chat parser, the MCP admin tools and the future desktop app all call this one core. Every change of WhatsAgent's state is also an event. The self-chat shows those events as status lines today, and the desktop app will show the same events later, so the app adds a view without adding a second system.

## MCP surface

The WhatsAgent MCP endpoint serves tools and events. The tools act on behalf of the calling session, and WhatsAgent checks each call against that session's routes and the send rules:

| Tool | What it does |
| --- | --- |
| `read_chat` | Read recent messages in a chat. |
| `search` | Search stored messages. |
| `send` | Send text to a chat. |
| `react` | React to a message. |
| `download_media` | Save a media file into a folder that WhatsAgent owns. |
| `route_here` | Route a chat to the calling session. |
| `list_sessions` | List checked-in sessions. |

The event API copies the shape of [SEP-3415](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/3415), the MCP Events extension (`io.modelcontextprotocol/events`) that the [MCP Triggers and Events Working Group](https://modelcontextprotocol.io/community/working-groups/triggers-events) is drafting. That means `events/list`, plus `events/stream` with a cursor and a heartbeat, and each event's `eventId` is the WhatsApp message ID. The adapters read routed events from this API like any other client. When a harness supports MCP Events, it can connect to the endpoint and subscribe on its own, and its adapter no longer needs to push. The draft is not merged yet, so the event format lives in one module that can change with it.

## Adapters

Claude Code and Codex each have their own way to wake a session and to ask for approval, so WhatsAgent has one adapter per harness. An adapter does up to four jobs. It ties a check-in to a harness session, pushes routed messages into that session, relays approval prompts to the self-chat, and runs home on its harness. Routing and storage work the same way for every harness, and so do the enforced rules.

### Claude Code adapter

The Claude Code adapter pushes through [Claude Code channels](https://code.claude.com/docs/en/channels). The `whatsagent claude` subcommand starts Claude Code with the WhatsAgent channel turned on, passes any other arguments through, and sets an environment variable that tells the MCP server pushes will arrive. The MCP server then declares the `claude/channel` capability and turns each routed event into a `notifications/claude/channel` message, which wakes Claude at once. With the `claude/channel/permission` capability, the same server receives Claude Code's approval prompts and relays them to the self-chat.

A Claude Code session started without the flag is tools-only. Claude Code Desktop loads MCP servers and plugins, but its docs do not mention channels, so it counts as tools-only until tested. Home on Claude Code runs the CLI headless in stream-json mode (`claude -p --input-format stream-json --output-format stream-json`), the protocol that the Claude Agent SDKs use, and the adapter writes each self-chat message to its input.

### Codex adapter

Codex runs a managed app-server daemon that listens on a local socket, `$CODEX_HOME/app-server-control/app-server-control.sock`. A Codex TUI started with `codex --remote unix://` keeps its threads in that daemon, and the Codex adapter connects to the same socket as one more app-server client. The `whatsagent codex` subcommand starts the TUI in this mode and passes any other arguments through.

The adapter reads the daemon's thread list to find Codex sessions and their folders, and it matches them with the MCP server's check-ins. When a routed message arrives for an idle thread, the adapter sends it with `turn/start`. Messages that arrive during a running turn wait and go out together after `turn/completed`. `turn/start` steers into a turn that starts in the moment before it arrives, and an idle-only variant is still a feature request ([openai/codex#38289](https://github.com/openai/codex/issues/38289)), so the adapter checks the thread's status right before each push and accepts that small race. The app-server also sends approval requests to its clients, and the adapter relays them to the self-chat if the daemon sends them to it, which the first live run of the Codex adapter checks. Home on Codex is a thread that the adapter starts on the daemon.

Codex Desktop and a TUI started without `--remote` run their own private app-server, which no other process can reach ([openai/codex#33556](https://github.com/openai/codex/issues/33556), [#47193](https://github.com/openai/codex/issues/47193)). Sessions there are tools-only.

### Support matrix

| Harness and launch | Push | Approval relay | Home |
| --- | --- | --- | --- |
| Claude Code CLI started with `whatsagent claude` | channel | permission relay | stream-json |
| Claude Code CLI without the flag | none | none | none |
| Claude Code Desktop | none until channels are tested | none | none |
| Codex TUI started with `whatsagent codex` | `turn/start` on the daemon | app-server approval requests, if the test passes | daemon thread |
| Codex Desktop, or a TUI without `--remote` | none | none | none |
| Other MCP apps | none until MCP Events | none | none |

Every row can use the MCP tools. When a harness supports MCP Events, its adapter stops pushing and the harness subscribes itself, while the adapter keeps its other jobs, because MCP Events does not cover approvals or starting home.

## Send rules

Send rules decide which sessions may send to which chats through the `send` and `react` tools. A rule names a session and a chat, either of which can be `*` to mean all of them, plus a mode. The mode `allow` sends the message. `ask` posts it in the self-chat first and sends it only after the owner answers `yes`. `deny` makes the tool return an error.

When several rules match a send, the most specific one wins, in this order: the session and the chat, the session and `*`, `*` and the chat, and finally `*` and `*`. Without any rule, a session may send to the chats routed to it and to the self-chat, and every other send uses `ask`. So `/allow * <chat>` lets every session message one chat, and `/allow * *` lets any session message any chat. A session can read its routed chats and the self-chat, plus every chat it has an `allow` rule for.

The owner can set rules in the self-chat or from the CLI with the matching `whatsagent` subcommands. Sessions use the MCP admin tools, and the desktop app will offer the same later. All of these call the command core. A session that asks for a rule covering itself, such as "let this session message Sam", gets the rule only after the owner confirms it in the self-chat.

## Enforced rules

These rules live in the WhatsAgent daemon, so no prompt can change them.

- Every send goes through the send rules, so a session cannot reach a chat that the rules keep from it.
- Only the owner's own messages count as instructions. Anyone in a routed group can write text that tries to steer the agent, so every event carries a trust field that marks text from other people as data.
- WhatsAgent adds each route's reply prefix to every send on that route.
- Sends are spaced out. whatsmeow is an unofficial client, and WhatsApp can ban numbers that send automated messages.
- Only the owner's user can open the daemon's socket, and each checked-in session gets its own token.

## Delivery guarantees

WhatsAgent delivers each message at least once. It keeps a cursor for each session name, and a session that checks in again gets every event after its cursor. Neither harness confirms that the agent read a pushed message. The Claude Code adapter advances the cursor once it has written the event to the session's MCP server, and the Codex adapter advances it once `turn/start` succeeds. Each push carries the WhatsApp message IDs, so a repeated delivery can be recognized and dropped. Events that arrive while the agent is busy wait and reach it together on its next turn.

## Login import

Owners who already run a whatsmeow-based tool can move to WhatsAgent without linking again. Every such tool keeps its login in a whatsmeow session store, so `whatsagent link --import <session.db>` copies that file into WhatsAgent's store, and whatsmeow upgrades the schema on first open. The import opens the source read-only, writes a consistent snapshot with SQLite's `VACUUM INTO`, and refuses to overwrite an existing WhatsAgent store.

The owner stops the other tool before starting the WhatsAgent daemon, because two programs that use one linked device at the same time disconnect each other. Messages that arrive while neither program runs wait on WhatsApp's servers and reach WhatsAgent when it connects. The other tool's message history stays behind. Going back to the old tool after WhatsAgent has run can cause some undecryptable messages, because the encryption state moves on after the copy, so linking that tool again is the clean way back. Scripts that called the old tool move to `whatsagent send` and `whatsagent export`, which hand the work to the running daemon.

## Stack

WhatsAgent is one Go module that builds one binary, `whatsagent`, with subcommands for onboarding, the daemon, the MCP server, starting harness sessions and scripts. Each concern below has one library.

| Concern | Choice | Notes |
| --- | --- | --- |
| WhatsApp protocol | `go.mau.fi/whatsmeow` | MPL-2.0. One internal package wraps it, so whatsmeow types stay out of the routing and MCP code. |
| QR codes | `github.com/mdp/qrterminal/v3` | Prints QR codes in the terminal for `whatsagent link`. |
| Storage | `database/sql` with `modernc.org/sqlite` | Two database files. whatsmeow's session store holds the device keys, and the WhatsAgent database holds messages, the event log, routes, sessions and cursors. `sqlstore.NewWithDB` accepts a `*sql.DB` that WhatsAgent opens, so both files can use this CGO-free driver, which the first link confirms. Every open sets WAL mode, foreign keys and a busy timeout. |
| Migrations | `github.com/pressly/goose/v3` | Only for the WhatsAgent database, because whatsmeow migrates its own store. |
| MCP | `github.com/modelcontextprotocol/go-sdk` v1.7.0 or later | v1.7.0 can register custom JSON-RPC methods ([#956](https://github.com/modelcontextprotocol/go-sdk/pull/956)), which the SEP-3415 methods need. |
| Codex app-server client | `github.com/gorilla/websocket`, with Go types generated from `codex app-server generate-json-schema` | The daemon's control socket speaks JSON-RPC over WebSocket. The generated types are committed and regenerated when the supported Codex version changes. |
| HTTP | `net/http` | The daemon serves only the MCP handler and a small admin API on a local socket, so a web framework would add nothing. |
| CLI | `github.com/spf13/cobra` | Subcommands `link`, `daemon`, `setup`, `claude`, `codex`, `channel`, `send`, `export`, `allow`, `ask`, `deny` and `status`. |
| Logging | `log/slog` | A small adapter implements whatsmeow's `waLog.Logger` on top of it, so there is one logging stack. |
| Concurrency | `golang.org/x/time/rate` and `golang.org/x/sync/errgroup` | The rate limiter spaces out sends, and the errgroup runs the daemon's goroutines. |
| Session tokens | `crypto/rand` | Opaque random tokens, because a token grants access to WhatsApp. |
| Tests | `testing`, `testify` and hand-written fakes of the WhatsApp wrapper and both harnesses | The fakes let end-to-end tests run without a phone or a model. |

Routes and sessions live in the WhatsAgent database, and only the command core changes them. A JSON export lets the owner inspect or back them up.

The Go MCP SDK has one gap that matters here. Claude Code channels need the server to send a custom notification, `notifications/claude/channel`, and the SDK pull request for custom outbound notifications ([#1146](https://github.com/modelcontextprotocol/go-sdk/pull/1146)) was closed without merging. The Claude Code adapter step checks whether the custom-method support from #956 can send it. If it cannot, the MCP server implements its small stdio MCP layer by hand, following the [channels reference](https://code.claude.com/docs/en/channels-reference).

Slophammer for Go, `govulncheck` and race tests run locally and in CI from the first code commit.

## Platforms

WhatsAgent runs on Linux and macOS. The SQLite driver needs no C compiler, so CI can cross-build `linux` and `darwin` binaries for amd64 and arm64 from one runner. The tests run on Ubuntu and macOS runners, and each GitHub Release carries the binaries.

| Concern | Linux | macOS |
| --- | --- | --- |
| Data | `$XDG_STATE_HOME/whatsagent`, default `~/.local/state/whatsagent` | `~/Library/Application Support/WhatsAgent` |
| Config | `$XDG_CONFIG_HOME/whatsagent`, default `~/.config/whatsagent` | `~/Library/Application Support/WhatsAgent` |
| Daemon supervision | systemd user service | launchd LaunchAgent |

`whatsagent service install` writes the systemd unit or the LaunchAgent and starts it. The daemon's socket sits in the data folder under a short name, because macOS limits a Unix socket path to 104 bytes, and the long per-user `$TMPDIR` on macOS would break that limit. Claude Code channels and the Codex app-server daemon work the same way on both systems. Codex Desktop on macOS stays tools-only, because its app-server is private.

## Open risks

The live checks during the first runs cover the biggest risks, which are self-chat delivery and Codex pushes. The other risks are known and can be planned around.

Claude Code channels are a research preview. A channel outside Anthropic's allowlist needs `--dangerously-load-development-channels`, which shows a confirmation screen at every start and has no effect with `-p`. Publishing the plugin to an allowlisted marketplace removes that step. Claude Code also does not register a channel server that negotiates MCP protocol revision 2026-07-28, so the WhatsAgent MCP server pins an earlier revision.

The Codex app-server and its daemon are marked experimental, and the protocol changes between Codex releases. The Codex adapter therefore checks the daemon's version at startup (`codex app-server daemon version`) and refuses a version it has no generated types for. The start-or-steer race described above stays until Codex ships idle-only turn admission.

Home drives the harness with the owner's own sign-in. Before WhatsAgent ships to other people, check Anthropic's and OpenAI's terms for products built on their agents, because Anthropic asks for API-key sign-in unless it approves claude.ai sign-in.

whatsmeow covers the WhatsApp protocol only. WhatsAgent has to build the message store, history sync, app-state recovery, the mapping between phone numbers and LIDs (WhatsApp's private user IDs), media handling and reconnects itself. SEP-3415 can still change before it is merged, which is why its format stays in one module.

## Build order

The build goes straight to the final setup. The self-chat and Codex questions are checked live during the first runs, and a setting moves the control room to a one-member group if self-chat delivery fails.

1. Set up the Go module, with Slophammer for Go and CI on Linux and macOS.
2. Build the daemon core, which covers linking with QR codes, `--phone` and `--import`, plus the stores, the event log, the command core and the first self-chat commands.
3. Move the owner's account to WhatsAgent and run the daemon as a service, checking self-chat delivery on the way.
4. Build the Claude Code adapter, starting with the plugin, check-in and tools, then adding pushes through `whatsagent claude`.
5. Build the Codex adapter, starting with thread discovery and check-in, then adding pushes through `whatsagent codex`, and check that an attached TUI shows the pushed turns.
6. Add home on both harnesses.
7. Add approval relay for both harnesses, and merged status lines.
8. Add `whatsagent send` and `whatsagent export` for scripts.
9. Build the desktop app on the admin API.
10. Move pushes to MCP Events for each harness that implements the extension once it is merged.

## References

- [Claude Code channels](https://code.claude.com/docs/en/channels) and the [channels reference](https://code.claude.com/docs/en/channels-reference)
- [SEP-3415: Events Extension](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/3415)
- [MCP Triggers and Events Working Group](https://modelcontextprotocol.io/community/working-groups/triggers-events)
- [whatsmeow](https://github.com/tulir/whatsmeow)
- [openai/codex#33556](https://github.com/openai/codex/issues/33556) and [#47193](https://github.com/openai/codex/issues/47193), on pushing events into live Codex sessions, and [#38289](https://github.com/openai/codex/issues/38289), on idle-only turn admission
