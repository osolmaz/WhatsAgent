---
date: 2026-11-02
author: Onur Solmaz <2453968+osolmaz@users.noreply.github.com>
title: Message storage specification
tags: [spec, storage, privacy, keep, sessions, cli]
---

# Message storage specification

## TL;DR

WhatsAgent stores the messages of a chat on disk only when the owner keeps that chat with `/keep`. The self-chat is always kept. Messages of every other chat stay in the daemon's memory for at most 24 hours, so an agent that the owner wakes in a chat can still read the recent messages, and a restart forgets them. Those messages reach only the agents that WhatsAgent runs in its own terminals, which the daemon recognizes by their process, never by a name they claim. The CLI reads only kept chats and runs only commands that show state, and every change to what is kept, routed or shown is made from the owner's phone.

## Design goals

These goals come from the owner. Each section below serves them.

1. WhatsAgent does not download and store every message by default. A chat's messages go to disk only after the owner chooses that chat.
2. Messages are not available to a program that just calls a CLI. No `whatsagent` command hands out the messages of a chat that is not kept, or a way to see them.
3. WhatsAgent does not encrypt its files, so a program that runs as the same user can always read them, and that cannot be prevented. The aim is to make collecting messages inconvenient, not impossible.
4. The design stays simple. Each boundary that this document draws holds without exceptions, and a test checks it.

The owner also chose these parts of the design: a `/keep` command that marks the chats to store, the self-chat always kept, other chats held in memory for one day, no history when an account is linked, CLI reads limited to kept chats, and changes to access made only from the phone. When an existing installation upgrades, the chats that have a route stay kept and the stored messages of every other chat are deleted.

## Terms

A **kept chat** is a chat whose messages WhatsAgent writes to its database. The owner keeps a chat with `/keep <chat> on` and stops keeping it with `/keep <chat> off`. The self-chat is always kept.

The **recent buffer** is an in-memory database in the daemon. It holds the messages of chats that are not kept, for at most 24 hours.

A **run agent** is an agent that WhatsAgent starts in a terminal that it owns: home on Claude Code and each chat's own agent. A session that starts by itself and checks in, such as one started with `whatsagent claude`, a Codex thread, or an MCP server that a script runs, is not a run agent.

The **phone** means a command that the owner writes in the self-chat.

## Boundaries

### Disk

WhatsAgent writes a message to disk only when its chat is kept. This covers the message itself, its edits and deletion, its entry in the event log, the IDs of messages that WhatsAgent sent, and media files. The history that WhatsApp sends when an account is linked is never stored; WhatsAgent takes only chat names from it.

When the owner stops keeping a chat, WhatsAgent deletes the chat's stored messages, events and sent IDs. The database runs with SQLite's secure delete and WhatsAgent truncates the write-ahead log after the deletion, so the deleted text does not stay behind in free pages or in the log. At every start, WhatsAgent deletes stored messages of any chat that is not kept, so the rule also holds after a crash or a manual change to the database.

`download_media` works only in kept chats, because it writes a file.

A file that a session sends goes to WhatsApp encrypted, and WhatsAgent writes none of its contents to disk. It stores the sent message like any other, so in a kept chat the stored message holds the file's name, type and download keys, and `download_media` can fetch the file again. While an approval waits, the daemon holds only those keys in memory.

### Memory

The recent buffer is an in-memory SQLite database with the same tables as the file. Temporary storage of both databases stays in memory, so a large query does not spill message text into temporary files. Every minute the buffer drops messages older than 24 hours, and a restart empties it. Messages in the file and in the buffer share one event sequence, so a session's cursor works across both.

When the owner keeps a chat, its messages move from the buffer to the file.

### Readers

The messages of a chat that is not kept reach only run agents, and only what each agent's view shows: its own chat as the chat's mode allows, or every chat for home. They reach a run agent as pushes and through `read_chat`, `search` and `react`.

Every other caller gets kept chats only: the CLI and the owner API, `whatsagent watch`, sessions that check in by themselves, and agent tool calls made with `--session`. For these callers, reading a chat that is not kept fails with an error, a search leaves it out, and its events are skipped for good, so keeping the chat later does not deliver them.

The daemon recognizes a run agent by its process. The WhatsAgent MCP server of a run agent is a child of the agent's process, which the daemon started in its terminal. When an MCP server checks in, the daemon reads the process ID of the other end of the Unix socket (`SO_PEERCRED` on Linux, `LOCAL_PEERPID` on macOS) and accepts it as a run agent only when that process descends from the terminal process of the session name it claims. A session name alone proves nothing, so a script that sets `WHATSAGENT_SESSION=home` gets kept chats only.

Agents that WhatsAgent drives through an adapter, such as home on Codex, get their pushes from the daemon directly. Their MCP servers run under the Codex app-server daemon, so their tool calls get kept chats only.

### Commands

A command that changes state runs only from the phone. Through the CLI (`whatsagent command`), only a command that shows state runs: a command without arguments whose bare form only shows, such as `/status`, `/routes` or `/keep`. A session's request to change state waits for the owner's yes in the self-chat, as before.

The browser page shows the terminals of the run agents, so its link is a way to see messages. The link is sent only in the self-chat, as the answer to `/web`, and WhatsAgent does not store that answer, because the self-chat is kept and the CLI reads it. `whatsagent web` asks the daemon to open the page in the default browser of the machine, and the link never reaches the CLI.

## Outside these boundaries

A program that runs as the same user can still read the files. `session.db` holds the WhatsApp keys, so a program that copies it can act as the linked device. The WhatsAgent database holds the kept chats, every chat's name and ID, the modes, windows, routes and settings, and the page's token. A program that can inspect the daemon's memory can read the recent buffer.

A local program can still disturb a session. If it checks in under a run agent's name and asks for pushes, the agent can miss messages until it checks in again. The program itself still gets nothing of a chat that is not kept.

Claude Code and Codex keep their own session files. What a run agent is shown ends up there, so those files hold what the owner let the agent see.

Text that appears in a kept chat is stored as a message of that chat, wherever it came from. An approval question in the self-chat that quotes a message, an answer from home that sums up a chat, or a message that the owner forwards are stored with the self-chat.

## Commands

`/keep` lists the kept chats. `/keep <chat> on` keeps a chat and moves its recent messages into the database. `/keep <chat> off` stops keeping it and deletes its stored messages; the self-chat cannot be turned off.

`/web` answers with the link of the browser page.

`/route` to a session that is not a run agent says so when the chat is not kept, because such a session gets nothing from that chat until the owner keeps it.

## Tests

Each boundary has tests that would fail if it broke:

- After a scenario with messages, edits, deletions, sends and history in a chat that is not kept, the bytes of the database file, its write-ahead log and the media folder do not contain the scenario's text.
- After `/keep <chat> off`, the same check passes for the text that the chat had stored.
- The buffer drops messages after 24 hours, and a new store starts empty with a sequence above every session's cursor.
- The CLI's `read_chat`, `search`, `export`, `download_media` and `watch` return nothing of a chat that is not kept, and an MCP server that only claims a run agent's name gets the same.
- A process that descends from a run agent's terminal process is recognized, and any other process is not.
- Every command with arguments that changes state is refused through the CLI, the bare form of every command changes nothing, and `/web` is refused through the CLI.
- The answer to `/web` reaches the phone, and no stored message holds the link.
