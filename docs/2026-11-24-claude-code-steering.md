---
date: 2026-11-24
author: Onur Solmaz <2453968+osolmaz@users.noreply.github.com>
title: Claude Code steering
tags: [plan, claude-code, adapter, hooks, push]
---

# Claude Code steering

## TL;DR

Claude Code holds channel messages while a session is busy and delivers them only when the turn ends, so an agent in a long turn answers WhatsApp late. With steering, the Claude Code adapter passes a routed message into the running turn after the current tool calls finish. Claude Code hooks call a tool on the session's own WhatsAgent MCP server, which knows whether the session is busy and holds the messages that arrive meanwhile. An idle session still gets its messages through the channel at once. Steering is a Claude Code feature; it lives in the package `internal/claudecode` and changes nothing in the daemon or the core.

## Introduction

The [plan](2026-10-31-whatsagent-plan.md) describes the Claude Code adapter: the session's MCP server turns each routed event into a `notifications/claude/channel` message. Claude Code's [channel reference](https://code.claude.com/docs/en/channels-reference) says: "If several notifications arrive while Claude is busy, they're delivered together on the next turn." A message typed in the terminal reaches Claude between tool calls, but a channel message does not, and a channel has no option to ask for that. We cannot change Claude Code, so WhatsAgent delivers busy-time messages itself through [hooks](https://code.claude.com/docs/en/hooks).

## Terms

- **Steer**: pass a routed message into a running turn of a session, so the agent sees it before its next step.
- **Busy**: the state of a Claude Code session between the start of a turn and its end, as the hooks report it.
- **Held message**: a routed message that the MCP server keeps while its session is busy, until a hook takes it.

## Design

### Hooks

Claude Code supports `mcp_tool` hooks, which call a tool on a configured MCP server and read the tool's text as the hook's output. Every Claude Code session that WhatsAgent starts with the channel gets these hooks through `--settings`: sessions started with `whatsagent claude`, home and chat agents. The hooks call the tool `claude_code_hook` on the `whatsagent` server with the event name and the subagent ID:

| Event | What the MCP server does |
| --- | --- |
| `UserPromptSubmit` | Marks the session busy and returns the held messages, which reach Claude with the prompt. |
| `PreToolUse` | Marks the session busy. |
| `PostToolBatch` | Marks the session busy and returns the held messages, which reach Claude before its next model call. |
| `Stop` | Returns the held messages, and the turn goes on so that Claude can act on them. With no held messages, marks the session idle. |
| `StopFailure` | Marks the session idle and pushes the held messages through the channel. |

A hook from a subagent carries an `agent_id`. The MCP server ignores those hooks, so messages reach the main agent and never a subagent's context, and a background subagent does not mark an idle session busy.

The MCP server lists `claude_code_hook` only when channel pushes are on. Its description tells the agent not to call it; a call by the agent would only return the held messages.

### Delivery

When a routed event reaches the MCP server and the session is idle, the server pushes it through the channel, as before. When the session is busy, the server holds it. The hook output wraps each held message in the same `<channel>` tag that a channel message gets, so the agent sees one format.

Claude Code fires no hook when the owner interrupts a turn with Esc, so the server could think a session is busy when it is idle. A fallback covers this: when no hook has come for 5 minutes and messages are held, the server marks the session idle and pushes them through the channel. A message then never waits longer than it did before steering, apart from those 5 minutes after an interrupt.

The cursor rule in "Delivery guarantees" does not change: the daemon advances the cursor once the event reaches the MCP server. A held message is in memory, as a queued channel message is in Claude Code's memory.

### Limits

- A message reaches the agent only after the running tool calls finish. A long command still makes it wait.
- A session started without the hooks, such as a plain `claude` with the development channel flag, keeps the old behavior: busy-time messages reach it at the end of the turn.

## Changes

- `internal/claudecode`: the hook settings for `--settings`, and the steering state (busy, held messages, fallback).
- `internal/mcp`: the `claude_code_hook` tool, and routing of pushed events through the steering state.
- `internal/cli`: `whatsagent claude` passes the hook settings.
- `internal/home`: home and chat agents get the hook settings.
- `slophammer.yml`: the new package's dependency rules.
- The plan: the term "steer", the Claude Code adapter section and "Delivery guarantees".

## Tests

- The steering state: idle push, holding while busy, delivery on `PostToolBatch`, `UserPromptSubmit` and `Stop`, the idle mark on `Stop` and `StopFailure`, subagent hooks ignored, and the fallback.
- The MCP server: the tool appears only with pushes on, and a pushed event during a busy turn comes back in the hook output in the `<channel>` format.
- The launches: `whatsagent claude` and the agent arguments carry the hook settings.
- A manual check with a real Claude Code session before release.
