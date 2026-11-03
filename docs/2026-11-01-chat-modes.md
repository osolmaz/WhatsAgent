---
date: 2026-11-01
author: Onur Solmaz <2453968+osolmaz@users.noreply.github.com>
title: Chat modes specification
tags: [spec, modes, windows, triggers, prefix, chat-agents]
---

# Chat modes specification

## TL;DR

Each chat has a mode that decides what an agent receives from it and may read in it: `off`, `keyword`, `window` or `all`. The default for every chat is `window`. In that mode the agent sees nothing until the owner names it with a wake phrase, for example "Bob, wake up" or "Bob start"; the name alone does nothing. That message gives the agent one hour of access, including the 30 minutes before the message. Naming it again extends access by one hour, "Bob stop watching" ends it, and the agent says when it starts and when it is done. Only the owner's own messages can call the agent, and each chat without a route gets its own agent, which can only read what the chat's mode shows it.

## Introduction

This document specifies how the owner controls what WhatsAgent's agents see of each WhatsApp chat. It covers the agents' name and trigger word, the prefix on agent messages, the four chat modes, the window mode's lifecycle, the agent that handles each chat, the commands, and where the rules are enforced. The [plan](2026-10-31-whatsagent-plan.md) describes the rest of WhatsAgent.

The idea behind the design is that the owner can give an agent temporary access to a chat from inside that chat, by naming the agent, without opening the self-chat or a terminal. Because the agent acts from the owner's account, only the owner can grant that access. In a group, other people can name the agent as often as they like, and nothing happens.

## Terms

- **Agent name**: the name that the owner gives the agents with `/name`, such as Bob. The default is "Agent".
- **Trigger word**: the agent name. The owner sets one word for both.
- **Trigger**: a message that the owner typed on their phone, in any chat other than the self-chat, that contains the agent name as a whole word in any case ("Bob", "bob,", "@Bob", but not "Bobby"), or that answers a message an agent sent. Messages from other people are never triggers. Messages that an agent or the CLI sent are never triggers either.
- **Wake phrase**: a phrase that, together with the agent name in a trigger, opens a window. The defaults are "start", "wake up", "monitor", "watch" and "turn on".
- **Stop phrase**: a phrase that, together with the agent name in a trigger, ends a window. The defaults are "stop watching", "stop monitoring" and "turn off".
- **Handler**: the session that handles a chat. It is the session of the chat's route, else the session of the route for every chat (`*`), else the chat's own agent.
- **Chat agent**: an agent that WhatsAgent runs for one chat that has no route. Its session is named `chat-<chat name>`, such as `chat-team`.
- **Window**: a time span in which the handler of a chat in the window mode receives the chat's new messages and may read its recent messages.
- **Prefix**: the text that WhatsAgent puts in front of every message an agent sends.

## Agent name and prefix

The owner names the agents once with `/name Bob`. The name is also the trigger word, and `/status` shows it on the `Agent name:` line.

Agents write only their message. WhatsAgent puts the prefix in front of every message that a session sends, so no skill, prompt or system prompt has to tell an agent how to sign its messages. This applies to the MCP `send` tool, to a chat agent's answers, to the window messages, and to `whatsagent send --session <name>` for scripts. The owner's own sends, from the phone or from `whatsagent send` without `--session`, go out exactly as written.

`/prefix <text>` sets the prefix as any text. WhatsAgent fills in `{name}` with the agent name and `{session}` with the sending session's name, turns `\n` into a line break, and passes WhatsApp formatting such as `*bold*` and `_italic_` through unchanged. It adds a space after the prefix unless the prefix ends in white space. `/prefix off` turns the prefix off, and `/prefix` alone shows the current prefix with an example. The default is `🤖 *{name}*:`, which shows the name in bold. A route can set its own prefix for one chat, which replaces the owner's prefix there. Text that already starts with the prefix is not prefixed a second time.

## Modes

`/mode <chat> <mode>` sets a chat's mode, and `/mode * <mode>` sets the mode of every chat that has no mode of its own. The default for every chat is `window`.

| Mode | The handler receives | The handler may read |
| --- | --- | --- |
| `off` | nothing | nothing |
| `keyword` | triggers | triggers and the agents' own messages |
| `window` | every new message while a window is open | the messages from the window's start to its end, while it is open |
| `all` | every new message | the whole chat |

In the `keyword` mode, the handler sees only the owner's messages that name the agent or answer an agent message, and it can respond only to those. It cannot read any other message of the chat, not even an answer from another person to its own question. A conversation with other people needs the `window` or the `all` mode.

The mode also limits where the handler may write without asking the owner; the "Answers and actions" section below has the full rule. In the `keyword` and `all` modes the mode allows it. In the `window` mode it allows it only while a window is open, and in the `off` mode never. Send rules (`/allow`, `/ask`, `/deny`) still override this default, as before. A send rule decides only whether a session may write to a chat. Reading a chat needs the handler role.

## Answers and actions

An agent answers without asking only in a conversation: a chat that it handles, whose mode lets it see the chat, and that sent it a message in the last 15 minutes. Any other message from an agent is an action on the owner's behalf, and waits for the owner's yes. That covers a message to a chat the agent does not handle, to a chat it handles that has been quiet, and a reaction in such a chat. Home is no exception: it answers in the self-chat, and asks before it writes anywhere else. The self-chat and the owner's own `/allow` rules stay as they are.

WhatsAgent asks in the chat where the owner last wrote to that agent in the last 15 minutes, so a request that the owner makes in a group is approved in that group; otherwise it asks in the self-chat. `/approvals self` makes it always ask in the self-chat, for an owner who does not want a draft for one chat shown in another; `/approvals here` restores the default. The owner answers with `yes 42` or `no 42` in the chat where WhatsAgent asked, or in the self-chat. Another person's answer counts for nothing, and so does an answer in an unrelated chat. The answer itself is never passed to an agent.

WhatsAgent writes each question itself, from the real arguments of the action: the agent's session, the kind of action, the target chat with its kind (group, direct chat or channel), and the exact text that will go out, prefix included. A prompt-injected agent can still try to send something harmful, but it cannot make the question describe something else. For tool approvals that Claude Code or Codex relay, the question shows the tool and its real input first; a description or reason that the agent wrote itself comes last and is labeled as the agent's own words. WhatsAgent's questions start with ⚙️, and an agent's message that would start with ⚙️ is refused, so an agent cannot fake a question even with the prefix turned off.

## Window mode

### Opening

A trigger with a wake phrase in a chat whose mode is `window` opens the chat's window, when none is open: "Bob start", "bob wake up" and "Bob, monitor this" all open it. A trigger without a wake phrase opens nothing, so mentioning the agent in passing, or writing to a person who happens to share its name, does not give it access. The window starts at the lookback before the trigger and ends at the window length after it. The defaults are a lookback of 30 minutes and a length of 1 hour. While the window is open, the handler receives every new message of the chat and may browse everything from the window's start on with `read_chat` and `search`. The handler does not receive the lookback messages as pushes. They only become readable, so the agent can read them when it needs the context.

When the window opens, WhatsAgent sends a start message to the chat, with the handler's prefix. By default it says how long the window lasts, how far back the agent can read, that naming the agent extends the window, and how to end it:

> 🤖 **Bob**: Now monitoring this chat until 11:30. I can read the messages of the last 30 minutes and everything new. Mention Bob to extend it by 1 hour, or say "Bob stop watching" to end it.

### Extending

Each later trigger while the window is open, with or without a wake phrase, moves the window's end to the window length after that trigger, and never earlier than the current end. The start stays where it was, so the agent keeps the same lookback. Extending sends no message, and the agent receives the trigger like any other message.

A new window length, set with `/mode <chat> window <length>` or `/window length`, applies to an open window at once: the window then ends at the new length after the command, as if the owner had named the agent, and the answer to `/mode` says when. A length of `1d` set while a window is open therefore keeps the agent for a day from that moment.

### Ending

A window ends in three ways:

- A trigger that contains a stop phrase. The defaults are "stop watching", "stop monitoring" and "turn off", so "Bob stop watching", "Bob, stop monitoring" and "Bob turn off" all end the window.
- `/window close <chat>` from the self-chat.
- The window's end time. WhatsAgent checks every 10 seconds.

When a window ends, WhatsAgent sends an end message to the chat, by default "I am done here. Mention Bob when you need me again." The agent then sleeps. It receives nothing more from the chat, it can no longer read the chat, and its sends to the chat ask the owner first. The next trigger opens a new window, with a new lookback from that trigger. Messages from an earlier window do not become readable again, but a chat agent keeps its harness session, so it remembers what it saw before.

Switching a chat to another mode removes its open window without an end message, because the new mode replaces the window.

### Settings

`/window` shows the window settings and the open windows. The owner changes the defaults with these commands:

| Command | Effect |
| --- | --- |
| `/window length <duration>` | How long a window lasts after each trigger. The default is 1 hour. |
| `/window lookback <duration>` | How far back a new window starts. The default is 30 minutes; 5m, 10m, 1h or 1d also work. |
| `/window wake <phrase>, <phrase>, ...` | The wake phrases. The defaults are "start", "wake up", "monitor", "watch" and "turn on". |
| `/window stop <phrase>, <phrase>, ...` | The stop phrases. The defaults are "stop watching", "stop monitoring" and "turn off". |
| `/window start <text>` | The start message. `{name}`, `{length}`, `{lookback}` and `{until}` are filled in. |
| `/window end <text>` | The end message, with the same placeholders. |
| `/window close <chat>` | Ends the chat's window now. |

`default` in place of a value restores the default, as in `/window start default`. A chat can also have its own length and lookback, as in `/mode Team window 2h 1d`. Durations accept minutes, hours and days, such as `30m`, `1h`, `1h30m` and `1d`.

## Handlers and chat agents

A route links a chat to a session, as before. `/route <chat> <session> [<mode>]` sets the route and the chat's mode in one step, and the mode defaults to `all`, because routing a chat to a session usually means that session should get the chat. `/unroute <chat>` removes the route and the chat's own mode, so the chat's own agent handles it again in the mode of every chat. A session can ask for a route with the MCP `route_here` tool, which takes a `mode` too; the owner confirms in the self-chat.

A chat without a route gets its own chat agent. WhatsAgent starts it when the chat's mode first shows the agent a message, for example when the owner opens a window. A chat agent runs on Claude Code with its own folder under the WhatsAgent data folder, keeps its harness session across restarts, and stops after 15 minutes without new messages and without an open window. The next message that its mode shows starts it again on the same session.

The owner made two decisions about this lifecycle:

1. A chat agent may stop at any time: when it is idle as above, when the daemon restarts, or when its process ends. WhatsAgent does not keep an agent running for its background jobs, such as a Monitor task or a background command, and those jobs end with the agent. The daemon also does not start chat agents when it starts.
2. When a message brings a chat agent back, it continues the same harness session, so it keeps its conversation and can pick up its work from there.

Each chat gets a separate agent, so what one chat lets the agents see never reaches another chat. A chat agent has all of Claude Code's tools, such as Bash and Monitor, so it can check a flight or a web page on a schedule while it runs, and WhatsAgent is its only MCP server. Its WhatsAgent tools check in as its own session, so `read_chat`, `search`, `react` and `download_media` show it only what its chat's mode shows, and it cannot read the self-chat or any other chat. It runs as an interactive Claude Code session in a terminal, gets its messages through its WhatsAgent channel, and answers in its chat with the `send` tool, which adds the prefix. The owner can watch it and type into it on the browser page that `whatsagent web` opens.

## Enforcement and limits

The daemon enforces the modes for every session that is not the owner:

- Delivery, reading, searching, reacting and media downloads all go through the same view of the chat, so a session never gets a message by one path that another path hides.
- WhatsAgent handles a trigger before it stores the message. A session therefore never sees the message that closes a window, and it never misses the message that opens one.
- A message reaches a window's handler only while the window is open and covers the message's time. A message that a session would get after the window ended stays hidden.
- Windows are stored, so they survive a daemon restart. A window whose end passed while the daemon was down closes at the next check, with its end message.
- A message that WhatsApp delivers twice runs its trigger once.

These guarantees bind sessions through their WhatsAgent tools. The owner, the `whatsagent` CLI and home keep the owner's rights. Home is the owner's assistant in the self-chat, so it can answer questions such as "what did Sam say today?". A session that can run shell commands as the owner's user, which includes every chat agent, can still read the kept chats and send as the owner through the CLI, as any program of the owner's user can. The [message storage specification](2026-11-02-message-storage.md) keeps the other chats out of the CLI and the database, so the modes are a hard limit for them.

## Risks

The agent name is the trigger word, and a common name such as Bob is also the name of real people. Because a window opens only for a wake phrase, "Hey Bob!" to a friend called Bob opens nothing, but "Bob, start the car" would. In an open window every mention extends it, so a chat with a real Bob in it keeps its window open for as long as people talk to him. A chat where that can happen should get `/mode <chat> off`. An owner who prefers to opt chats in one by one can set `/mode * off` and then `/mode <chat> window` for each chat where the agent may help.

## Migration

Routes used to have an "addressed" flag. The schema migration turns each route with the flag into a chat in the `keyword` mode, and every other route into a chat in the `all` mode. Send rules now call their outcome a permission (allow, ask or deny), so "mode" means only the chat mode. The fixed word "claude" no longer calls the agents; the agent name does, and only when the owner writes it.
