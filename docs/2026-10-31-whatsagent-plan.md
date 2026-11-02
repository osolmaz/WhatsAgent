---
date: 2026-10-31
author: Onur Solmaz <2453968+osolmaz@users.noreply.github.com>
title: WhatsAgent plan
tags: [plan, architecture, whatsapp, mcp, claude-code, codex]
---

# WhatsAgent plan

## TL;DR

WhatsAgent is a bridge between one WhatsApp account and the coding agent sessions that the account's owner runs in Claude Code and Codex. The owner controls it from their own "Message Yourself" chat, where messages that start with `/` are commands for WhatsAgent and every other message goes to a background session called home. Each harness gets its own adapter, which wakes a session through Claude Code channels, through `whatsagent watch` under Claude Code's Monitor tool, or through the Codex app-server daemon. An existing whatsmeow login can be imported, so an owner can switch from another whatsmeow-based tool without scanning a new QR code. The first version runs on Linux and macOS, and the first live run confirmed that WhatsAgent can post in the owner's self-chat and read the owner's commands there.

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
| session | One running agent session in a harness, such as a Claude Code session or a Codex thread. The owner opens it, or WhatsAgent starts it for home or a chat agent. |
| home | The owner's assistant in the self-chat, which WhatsAgent starts and keeps running. |
| chat agent | A session that WhatsAgent runs for one chat without a route, and that sees only what the chat's mode shows. |
| route | A link from one WhatsApp chat to the session that handles it. |
| mode | What the session that handles a chat receives from it and may read in it: `off`, `keyword`, `window` or `all`. |
| window | A time span in which the window mode shows a chat. The owner opens, extends and ends it with triggers. |
| agent name | The agents' name, such as Bob, which is also the trigger word. |
| trigger | A message that the owner typed in a chat other than the self-chat that names the agents or answers an agent message. |
| prefix | The text that WhatsAgent puts in front of every message that an agent sends. |
| send rule | A rule that decides whether a session may send to a chat. Its permission is `allow`, `ask` or `deny`. |
| command | A self-chat message that starts with `/`. WhatsAgent handles it, and no agent sees it. |
| check-in | A session becoming known to WhatsAgent, through the WhatsAgent MCP server or through the harness's own API. |
| adapter | The WhatsAgent code for one harness. It handles check-in, pushes, approval prompts and home for that harness. |
| kept chat | A chat whose messages WhatsAgent stores in its database, because the owner kept it with `/keep`. The self-chat is always kept. |
| recent buffer | The daemon's in-memory database for the messages of chats that are not kept, which it holds for at most 24 hours. |
| run agent | An agent that WhatsAgent runs in a terminal of its own, home or a chat agent, which the daemon recognizes by its process. Only run agents read chats that are not kept. |
| phone | The self-chat as the source of a command. Commands that change state run only from the phone. |

## Architecture

WhatsAgent links to the owner's account as a WhatsApp linked device, using [whatsmeow](https://github.com/tulir/whatsmeow), the Go library behind several open source WhatsApp tools. It puts every live message in an event log and uses the routes to decide which session gets it. Only kept chats go to disk; the messages of other chats stay in the recent buffer, and the history that WhatsApp sends at linking is never stored (see the [message storage specification](2026-11-02-message-storage.md)). Sessions never talk to WhatsApp directly. They read and send through WhatsAgent's MCP tools, which lets WhatsAgent apply its rules to every send. Everything that differs between harnesses lives in the adapters.

```text
              owner's phone: self-chat
       /commands -> WhatsAgent    plain text -> home
                        |
+----------- WhatsAgent daemon (Go, whatsmeow) -------------+
| WhatsApp link, message store, event log, routes           |
| command core <- self-chat parser, MCP admin tools, GUI    |
| session registry: who is checked in, who receives pushes  |
| local API: WhatsApp tools + event streams (SEP-3415 shape)|
| adapters: Claude Code (channels, terminals)               |
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
| `/route <chat> <session> [<mode>]` | Let a session handle a chat, in a mode that defaults to `all`. |
| `/mode <chat> <mode>` | Set what agents see of a chat. See the [chat modes specification](2026-11-01-chat-modes.md). |
| `/window` | Show or change the window settings, or close a chat's window. |
| `/unroute <chat>` | Stop sending a chat's messages to any session. |
| `/routes` | List the routes. |
| `/rename <session> <name>` | Give a session a stable name. |
| `/home claude`, `/home codex`, `/home off` | Run home on Claude Code or Codex, or stop it. |
| `/home new` | Start a fresh home session in place of the current one. |
| `/allow`, `/ask`, `/deny <session> <chat>` | Set a send rule, with `*` for every session or every chat. |
| `/rules` | List the send rules. |

Plain text goes to home, and home replies in the self-chat.

Status lines come from WhatsAgent itself. The first one is a welcome message when the account links. After that, WhatsAgent posts a line whenever its state changes, for example when it starts or when a session with a route goes online or offline. Every Claude Code session loads the WhatsAgent MCP server, so sessions without a route get no status line. Lines that arrive within a few seconds are merged into one message so the chat stays readable. WhatsAgent's lines start with ⚙️ and home's replies with 🏠, and a session's messages carry the agent prefix described under "Routes".

Approval prompts are the fourth kind. When a session needs approval for a tool call, its adapter can relay the prompt to the self-chat. The owner then answers with `yes 42` or `no 42`. Each open question gets a two-digit code that no other open question uses, and a code rests for an hour after its question closes, so a late answer cannot approve a newer question. A relayed prompt keeps the harness's own request ID inside WhatsAgent. The "Adapters" section says how each harness supports this.

WhatsAgent accepts commands only from the owner and only in the self-chat. The owner's messages and WhatsAgent's own messages both appear there as sent by the owner's account, so WhatsAgent records the ID of every message it sends and never treats one of them as a command or a prompt.

### Self-chat delivery

Sending into Message Yourself from a linked device could have failed silently, because WhatsApp can accept such a message and never show it there. The first live run on 1 November 2026 showed the welcome message and the `/status` reply on the owner's phone, so the control room stays in Message Yourself. If that changes, the control room moves to a WhatsApp group whose only member is the owner. Nothing else in this plan changes.

## Sessions

The owner opens and closes sessions. The only session WhatsAgent starts is home.

Every harness loads the WhatsAgent MCP server, a small stdio server that the `whatsagent channel` subcommand runs. `whatsagent setup claude` registers it with `claude mcp add --scope user`, and `whatsagent setup codex` with `codex mcp add`. Both register it for the owner's user account, so every session of that harness gets the WhatsApp tools, including sessions started without `whatsagent claude` or `whatsagent codex`. When a session starts, the server opens a session stream to the WhatsAgent daemon over a local socket and checks in with the session's harness, folder and name. When the session ends, the stream closes and WhatsAgent marks the session offline, so it needs no process scanning. The environment variable `WHATSAGENT_SESSION` names a session, and otherwise the folder's base name does.

A push session is one that its adapter can wake when a routed message arrives. A tools-only session can read and send through the MCP tools when the owner asks it to, but a new message does not wake it. Which sessions can receive pushes depends on the harness and on how the owner started the session, as the "Adapters" section describes. Each session name has at most one push target at a time, and a newer one replaces the older one.

Routes point at session names, so a route survives when its session restarts. A session's default name is its folder name, and `/rename` or a request inside the session changes it. When the owner closes a session and resumes it later, it checks in under the same name and its routes work again. Messages for an offline session wait in the event log while the self-chat shows how many are waiting.

## Routes

WhatsAgent stores the kept chats and holds the others in memory for a day, as the [message storage specification](2026-11-02-message-storage.md) defines, and home can read them all when asked ("what did Sam say today?"). Which session handles a chat, and what it sees of it, is up to the owner: each chat has a mode, and the [chat modes specification](2026-11-01-chat-modes.md) defines the modes, the window mode's lifecycle and the chat agents.

A route links one chat to one session. Each chat has at most one route, so it is always clear which session answers it. Owners who want everything in one place can send every chat to home with `/route * home`. A chat without a route gets its own chat agent, which WhatsAgent runs. Setting a route also sets the chat's mode, which defaults to `all`. A route can also set its own prefix.

The owner names the agents with `/name`, and the name doubles as their trigger word. Agents write only the message itself. WhatsAgent puts the agent prefix in front of every message that a session sends, through the MCP `send` tool or through `whatsagent send --session <name>`, so no skill or prompt has to tell an agent how to sign. The owner sets the prefix with `/prefix` as any text: `{name}` and `{session}` are filled in, `\n` becomes a line break, and WhatsApp formatting such as `*bold*` passes through. The default is `🤖 *{name}*:`, a route's own prefix wins for its chat, and `/prefix off` turns the prefix off. Text that already starts with the prefix is not prefixed twice. The owner's own sends, from the phone or from `whatsagent send` without `--session`, go out as written.

The owner can create a route in three ways, and all of them run the same code. The first is the `/route` command. The second is a request to home, such as "send my chat with Sam to the billing-api session". The third is a request inside a session, such as "connect this session to my chat with Sam", which makes the agent call the `route_here` tool.

## Home

Home is the only session that WhatsAgent starts. It runs on Claude Code or on Codex, as the owner chooses with `/home claude` or `/home codex`, and its adapter starts it. Home receives the owner's plain-text self-chat messages and has the WhatsApp tools and the admin commands. Home's sends follow the send rules like any other session's, so by default a send from home to a chat without a route waits for the owner's `yes`. Owners who want commands only can turn home off with `/home off`.

Home is one long-lived session that survives restarts. When an adapter starts home for the first time, WhatsAgent stores the session's ID in its database. The Claude Code adapter chooses a UUID and passes it with `--session-id`, and the Codex adapter keeps the thread ID that `thread/start` returns. After a WhatsAgent restart or a reboot, the adapter continues the same session with `claude -p --resume <id>` or `thread/resume`, so home keeps its context. When Claude Code cannot resume the stored session, home starts a fresh one. Both harnesses compact a long conversation on their own, and `/home new` replaces home with a fresh session when the owner wants a clean start. Each harness keeps its own home, so switching to `/home codex` and back to `/home claude` continues the earlier Claude Code home.

Home works in the folder `home` inside WhatsAgent's data folder. On Claude Code, home and the chat agents run as interactive Claude Code sessions in terminals that the daemon owns, the same way as a session started with `whatsagent claude`: each one gets its messages through its WhatsAgent channel and answers with the `send` tool. WhatsAgent answers Claude Code's two startup questions for them, the warning for a channel under development and the trust question for the agent's own folder, and starts an agent again when its process ends, resuming its session once Claude Code keeps a transcript for it. `whatsagent web` opens a browser page with a session list and each agent's terminal, which the owner can watch and type into; closing the tab leaves the agents running. The page and its terminal come from pi-factory-web, with ghostty-web as the terminal, and the daemon serves them on loopback with a random token. On Codex, home is a thread on the app-server daemon, and Codex posts its final answer in each turn to the self-chat, where `NO_REPLY` posts nothing.

Home and the chat agents approve their own tool calls by default, with Claude Code's auto mode (`--permission-mode auto`) or Codex's auto review (`approvalsReviewer: auto_review`). `/auto off` turns that off. A request that the harness will not decide becomes a "yes <code>" question in the self-chat through the channel's permission relay, and it also shows in the agent's terminal. Home drops the variables that mark a Claude Code session's child processes, so a daemon started from inside a session still starts home as a session of its own.

The owner can watch a Codex home live by attaching a TUI to the same daemon thread. A Claude Code home must be stopped with `/home off` before the owner opens it in a terminal, because two Claude Code processes must not write to one session.

## Command core

Each command is defined once, with its name, arguments, who may run it, and its handler. The self-chat parser, the MCP admin tools and the future desktop app all call this one core. Every change of WhatsAgent's state is also an event. The self-chat shows those events as status lines today, and the desktop app will show the same events later, so the app adds a view without adding a second system.

## MCP surface

The WhatsAgent MCP endpoint serves tools and events. The tools act on behalf of the calling session, and WhatsAgent checks each call against that session's routes and the send rules:

| Tool | What it does |
| --- | --- |
| `read_chat` | Read recent messages in a chat. |
| `search` | Search stored messages. |
| `send` | Send text, a file, or a file with a caption to a chat, optionally as a reply to a message, with mentions. |
| `edit_message` | Change the text of a message that WhatsAgent sent for an agent, up to 15 minutes after sending. |
| `delete_message` | Delete a message that WhatsAgent sent for an agent, for everyone. |
| `forward_message` | Forward a message that the session may read to a chat. |
| `send_poll` | Send a poll; each vote arrives as a message that names the picked options. |
| `send_location` | Send a place on the map. |
| `typing` | Show the owner as typing in a chat where the session may answer at once. |
| `mark_read` | Mark a message and the chat's earlier messages as read. |
| `group_info` | Show a group's name, description, settings and members. |
| `manage_group` | Rename, describe, add, remove, promote or demote members, change the settings, get or reset the invite link, or leave. |
| `create_group`, `join_group` | Create a group, or join one by its invite link. |
| `find_contacts`, `contact_info` | Find people in the owner's address book, check a number on WhatsApp, and show a person's about text and picture. |
| `set_profile` | Change the owner's name, about text or picture. |
| `react` | React to a message. |
| `download_media` | Save a media file into a folder that WhatsAgent owns. |
| `route_here` | Ask the owner to route a chat to the calling session, in a mode. |
| `list_sessions` | List checked-in sessions. |

The MCP server also has `list_chats` for finding a chat, `command` for running a self-chat command (commands that change routes or rules wait for the owner's yes), and `approve`, which home uses as its permission prompt tool.

An agent sends a file by giving `send` its path. The MCP server reads the file in the agent's working folder and passes its contents to the daemon, so the daemon never opens a path that a session names, and the path means what it means to the agent. Files go up to 100 MB. WhatsAgent picks how WhatsApp shows the file from its extension, or from its first bytes when the extension is unknown: JPEG and PNG as photos, with a small preview, MP4 as a video, Ogg/Opus as a voice message, MP3, M4A and AAC as audio, and everything else as a document. The text is the caption; WhatsApp shows no caption with audio, so the text goes as a message of its own before it. The daemon uploads a file only after the send rules allow or ask, and it uploads it once. An approval shows the uploaded file itself with the description, and a yes sends that same upload, so the owner approves exactly what goes out. A pending approval holds only the uploaded file's keys, not its contents.

A send can answer one message of the chat, given by its ID; the session must be able to read that message, and WhatsApp shows it quoted above the answer. Agents mention a person by writing `@` and the person's phone number, which WhatsApp shows as the person's name. An agent edits or deletes only messages that WhatsAgent sent, never the owner's own messages or WhatsAgent's approval and status lines. The owner, through the CLI, edits and deletes the owner's own messages too, and deletes other people's messages in groups, which WhatsApp allows for group admins. WhatsApp edits only text messages, for 15 minutes after sending. A forward needs a message that the session may read, and follows the send rules of the chat it goes to. An agent's forward comes after a message with the agent's prefix and an optional comment, so the chat sees which agent forwarded it.

A poll's question carries the agent prefix. WhatsApp sends each vote encrypted with a secret of the poll, which whatsmeow keeps for polls that the account sent or received after linking. The vote names the options only by their SHA-256 hashes, so WhatsAgent finds the names in the stored poll and stores the vote as a message that answers the poll, such as "picked Pizza". A vote holds the voter's whole choice, and a later vote replaces it. WhatsApp shows no text with a location, so a location's text, with the agent prefix, goes first as a message of its own, as for audio. `typing` works only in a chat where the session may answer at once, because elsewhere no answer may follow; it leaves the account's online state alone, because an online linked device stops the phone's notifications. `mark_read` goes through the send rules, because the sender sees the blue ticks and the owner's phone shows the chat as read.

Group changes, new groups, joins and profile changes act on the owner's account rather than on one conversation, so an agent's request always waits for the owner's yes, whatever the send rules allow; a deny rule for the group still refuses it. The owner's own CLI runs them at once. An approval of a new profile picture shows the picture, which WhatsAgent uploads for the question, and the picture becomes the photo's centered square at up to 640 pixels. A session reads a group's details when it may read the group. Only the owner and home look up people, because the address book covers every chat. A phone number given to `find_contacts` is checked on WhatsApp as well. Group names have at most 25 characters, as WhatsApp requires.

Events follow the shape of [SEP-3415](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/3415), the MCP Events extension (`io.modelcontextprotocol/events`) that the [MCP Triggers and Events Working Group](https://modelcontextprotocol.io/community/working-groups/triggers-events) is drafting. The event log numbers every event, and a stream resumes after the number it was given and sends heartbeats while idle. Today the daemon serves that stream on its own socket, as JSON lines of `events`, `heartbeat` and `verdict` frames. The MCP server turns that stream into channel notifications. When the extension is merged and a harness supports it, the MCP server adds `events/list` and `events/stream` over the same log, and the harness subscribes on its own. The event format lives in the `model` and `api` packages, so it can change with the draft.

## Adapters

Claude Code and Codex each have their own way to wake a session and to ask for approval, so WhatsAgent has one adapter per harness. An adapter does up to four jobs. It ties a check-in to a harness session, pushes routed messages into that session, relays approval prompts to the self-chat, and runs home on its harness. Routing and storage work the same way for every harness, and so do the enforced rules.

### Claude Code adapter

The Claude Code adapter pushes through [Claude Code channels](https://code.claude.com/docs/en/channels). The `whatsagent claude` subcommand starts Claude Code with the WhatsAgent channel turned on, passes any other arguments through, and sets an environment variable that tells the MCP server pushes will arrive. The MCP server then declares the `claude/channel` capability and turns each routed event into a `notifications/claude/channel` message, which wakes Claude at once. With the `claude/channel/permission` capability, the same server receives Claude Code's approval prompts and relays them to the self-chat.

A Claude Code session started without the channel flag can still receive pushes through `whatsagent watch`. Run under Claude Code's Monitor tool, `whatsagent watch --session <name> --chat <chat>` checks in as a push session and prints each routed message as one line, which the Monitor turns into a notification that wakes the session. WhatsAgent keeps the watch's cursor under the session name, so a restarted watch misses nothing. `--since 0` replays every stored message. This path needs no development flag and works in any session that has the Monitor tool. Claude Code Desktop loads MCP servers and plugins, but its docs do not mention channels, so it counts as tools-only until tested. Home on Claude Code runs as an interactive session in a terminal with the channel, as the "Home" section describes.

### Codex adapter

Codex runs a managed app-server daemon that listens on a local socket, `$CODEX_HOME/app-server-control/app-server-control.sock`. A Codex TUI started with `codex --remote unix://` keeps its threads in that daemon, and the Codex adapter connects to the same socket as one more app-server client. The `whatsagent codex` subcommand starts the TUI in this mode and passes any other arguments through.

Every 20 seconds the adapter reads the daemon's loaded threads (`thread/loaded/list` and `thread/read`) and makes each one a push session, leaving out sub-agent and ephemeral threads and home's own thread. A thread's session name is its name, or its folder's base name with `-codex` when it has none. When a routed message arrives for an idle thread, the adapter sends it with `turn/start`. Messages that arrive during a running turn wait and go out together after `turn/completed`. `turn/start` steers into a turn that starts in the moment before it arrives, and an idle-only variant is still a feature request ([openai/codex#38289](https://github.com/openai/codex/issues/38289)), so the adapter checks the thread's status right before each push and accepts that small race. The app-server also sends approval requests to the client that started a turn. The adapter relays command and file-change approvals to the self-chat and answers `accept` or `decline`, and it refuses the other request types. Home on Codex is a thread that the adapter starts on the daemon with home's instructions as developer instructions, and the adapter posts the last agent message of each turn to the self-chat.

Codex Desktop and a TUI started without `--remote` run their own private app-server, which no other process can reach ([openai/codex#33556](https://github.com/openai/codex/issues/33556), [#47193](https://github.com/openai/codex/issues/47193)). Sessions there are tools-only.

### Support matrix

| Harness and launch | Push | Approval relay | Home |
| --- | --- | --- | --- |
| Claude Code CLI started with `whatsagent claude` | channel | permission relay | terminal with the channel |
| Claude Code CLI without the flag | `whatsagent watch` under the Monitor tool | none | none |
| Claude Code Desktop | none until channels are tested | none | none |
| Codex TUI started with `whatsagent codex` | `turn/start` on the daemon | app-server approval requests, if the test passes | daemon thread |
| Codex Desktop, or a TUI without `--remote` | none | none | none |
| Other MCP apps | none until MCP Events | none | none |

Every row can use the MCP tools. When a harness supports MCP Events, its adapter stops pushing and the harness subscribes itself, while the adapter keeps its other jobs, because MCP Events does not cover approvals or starting home.

## Send rules

Send rules decide which sessions may send to which chats through the `send`, `react`, `edit_message`, `delete_message`, `forward_message`, `send_poll`, `send_location`, `typing` and `mark_read` tools; a forward follows the rules of the chat it goes to. A rule names a session and a chat, either of which can be `*` to mean all of them, plus a permission. The permission `allow` sends the message. `ask` posts it in the self-chat first and sends it only after the owner answers `yes`. `deny` makes the tool return an error.

When several rules match a send, the most specific one wins, in this order: the session and the chat, the session and `*`, `*` and the chat, and finally `*` and `*`. Without any rule, a session may send to the self-chat, and answer in a conversation: a chat it handles, whose mode lets it see the chat, and that sent it a message in the last 15 minutes. Every other send uses `ask`, in the chat where the owner last wrote to that session, with a question that WhatsAgent writes from the real action; the [chat modes specification](2026-11-01-chat-modes.md) defines this in its "Answers and actions" section. So `/allow * <chat>` lets every session message one chat, and `/allow * *` lets any session message any chat. Send rules decide only who may write. Reading follows routes and modes: a session reads only the chats it handles, as their modes show them, and never the self-chat.

The owner can set rules in the self-chat or from the CLI with the matching `whatsagent` subcommands. Sessions use the MCP admin tools, and the desktop app will offer the same later. All of these call the command core. A session that asks for a rule covering itself, such as "let this session message Sam", gets the rule only after the owner confirms it in the self-chat.

## Enforced rules

These rules live in the WhatsAgent daemon, so no prompt can change them.

- Every send goes through the send rules, so a session cannot reach a chat that the rules keep from it.
- Only the owner's own messages count as instructions. Anyone in a routed group can write text that tries to steer the agent, so every event carries a trust field that marks text from other people as data.
- WhatsAgent puts the agent prefix in front of every send from a session.
- Sends are spaced at least 2 seconds apart. whatsmeow is an unofficial client, and WhatsApp can ban numbers that send automated messages.
- Only the owner's user can open the daemon's socket, and each checked-in session gets its own token.

These rules bind the MCP tools. The `whatsagent` CLI acts as the owner, so a session that may run shell commands as the owner's user, such as a chat agent, can read the kept chats and send as the owner through it, as it could read the session store itself. Send rules keep a well-behaved agent and a prompt injection inside its tools in bounds; a session the owner does not trust needs a sandbox without shell access to the owner's account.

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
| Migrations | `embed` and `PRAGMA user_version` | Only for the WhatsAgent database, because whatsmeow migrates its own store. Each migration is an embedded SQL file, and the schema version counts the applied ones. |
| MCP | A hand-written stdio JSON-RPC server | The server needs custom notifications (`notifications/claude/channel`), a pinned protocol revision and permission relay. The Go MCP SDK cannot send custom notifications ([#1146](https://github.com/modelcontextprotocol/go-sdk/pull/1146) was closed), and the few methods the server answers fit in one file. |
| Codex app-server client | `github.com/gorilla/websocket` | The daemon's control socket speaks JSON-RPC over WebSocket. The adapter decodes only the fields it uses from `thread/*`, `turn/*` and approval messages, checked against `codex app-server generate-json-schema` for Codex 0.166. |
| HTTP | `net/http` | The daemon serves a small JSON API on its Unix socket: session streams, tool calls, permission relay and watch streams, with streams as JSON lines. |
| CLI | `github.com/spf13/cobra` | Subcommands `link`, `daemon`, `service`, `status`, `command`, `send`, `edit`, `delete`, `forward`, `poll`, `location`, `read`, `react`, `media`, `group`, `contacts`, `contact`, `profile`, `chats`, `messages`, `search`, `export`, `changes`, `watch`, `channel`, `setup`, `claude` and `codex`. Rules and routes change through `whatsagent command "/allow ..."`, which runs the command core. |
| Logging | `log/slog` | A small adapter implements whatsmeow's `waLog.Logger` on top of it, so there is one logging stack. |
| Concurrency | The standard library | A mutex and the time of the last send space out sends, and plain goroutines with one context run the daemon. |
| Session tokens | `crypto/rand` | Opaque random tokens, because a token grants access to WhatsApp. |
| Terminals | `github.com/creack/pty` | Runs the agents that WhatsAgent starts in pseudo-terminals on Linux and macOS. A Windows port would add ConPTY. |
| Browser page | pi-factory-web's page with `ghostty-web` and Monaspace Argon | Copied into the binary with their MIT and OFL licenses; the daemon reimplements the page's small protocol in Go. |
| Tests | `testing` and hand-written fakes | A fake WhatsApp connection serves a real daemon API on a temporary socket, and stand-ins for `claude` and the Codex app-server let end-to-end tests run without a phone or a model. |

Routes and sessions live in the WhatsAgent database, and only the command core changes them. A JSON export lets the owner inspect or back them up.

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

The build went straight to the final setup. Steps 1 to 9 are built and covered by tests, and the owner's account moved to WhatsAgent on 1 November 2026, with self-chat delivery checked live.

1. Set up the Go module, with Slophammer for Go and CI on Linux and macOS.
2. Build the daemon core, which covers linking with QR codes, `--phone` and `--import`, plus the stores, the event log, the command core and the first self-chat commands.
3. Move the owner's account to WhatsAgent and run the daemon as a service, checking self-chat delivery on the way.
4. Build the Claude Code adapter with the MCP server's check-in and tools and with pushes through `whatsagent claude` and `whatsagent watch`.
5. Build the Codex adapter, which finds threads and pushes with `turn/start`.
6. Add home on both harnesses.
7. Add approval relay for both harnesses, and merged status lines.
8. Add `whatsagent send`, `export`, `changes`, `react` and `media` for scripts.
9. Add the agent name and prefix, the chat modes with the window mode, and chat agents, as the [chat modes specification](2026-11-01-chat-modes.md) defines them. Run home and the chat agents on Claude Code as interactive sessions in terminals, with a browser page to watch them.
10. Check live that an attached Codex TUI shows pushed turns, and that Claude Code Desktop registers the channel.
11. Build the desktop app on the daemon API.
12. Move pushes to MCP Events for each harness that implements the extension once it is merged.

## References

- [Claude Code channels](https://code.claude.com/docs/en/channels) and the [channels reference](https://code.claude.com/docs/en/channels-reference)
- [SEP-3415: Events Extension](https://github.com/modelcontextprotocol/modelcontextprotocol/pull/3415)
- [MCP Triggers and Events Working Group](https://modelcontextprotocol.io/community/working-groups/triggers-events)
- [whatsmeow](https://github.com/tulir/whatsmeow)
- [openai/codex#33556](https://github.com/openai/codex/issues/33556) and [#47193](https://github.com/openai/codex/issues/47193), on pushing events into live Codex sessions, and [#38289](https://github.com/openai/codex/issues/38289), on idle-only turn admission
