package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/core"
)

// tool is one API tool: a core call with JSON arguments.
type tool struct {
	ownerOnly bool
	call      func(ctx context.Context, c *core.Core, caller core.Caller, raw json.RawMessage) (any, error)
}

func define[A any](ownerOnly bool, fn func(ctx context.Context, c *core.Core, caller core.Caller, a A) (any, error)) tool {
	return tool{ownerOnly: ownerOnly, call: func(ctx context.Context, c *core.Core, caller core.Caller, raw json.RawMessage) (any, error) {
		var a A
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &a); err != nil {
				return nil, fmt.Errorf("bad arguments: %w", err)
			}
		}
		return fn(ctx, c, caller, a)
	}}
}

// ChatArgs names a chat, with an optional message limit.
type ChatArgs struct {
	Chat  string `json:"chat"`
	Limit int    `json:"limit,omitempty"`
}

// SearchArgs are the arguments of search.
type SearchArgs struct {
	Query string `json:"query"`
	Chat  string `json:"chat,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// SendArgs are the arguments of send.
type SendArgs struct {
	Chat string `json:"chat"`
	Text string `json:"text"`
}

// MessageArgs name one message, with an emoji for react.
type MessageArgs struct {
	Chat      string `json:"chat"`
	MessageID string `json:"message_id"`
	Emoji     string `json:"emoji,omitempty"`
}

// RouteArgs are the arguments of route_here.
type RouteArgs struct {
	Chat      string `json:"chat"`
	Prefix    string `json:"prefix,omitempty"`
	Addressed bool   `json:"addressed,omitempty"`
}

// QueryArgs are the arguments of list_chats.
type QueryArgs struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// CommandArgs are the arguments of command.
type CommandArgs struct {
	Text string `json:"text"`
}

// ApproveArgs are the arguments that Claude Code passes to a permission prompt tool.
type ApproveArgs struct {
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
}

// ChangesArgs are the arguments of changes.
type ChangesArgs struct {
	Cursor int64 `json:"cursor"`
}

// Text is a result that is one line of text.
type Text struct {
	Text string `json:"text"`
}

func limit(n, fallback int) int {
	if n <= 0 {
		return fallback
	}
	return min(n, 1000)
}

func tools() map[string]tool {
	return map[string]tool{
		"read_chat": define(false, func(ctx context.Context, c *core.Core, caller core.Caller, a ChatArgs) (any, error) {
			return c.ReadChat(ctx, caller, a.Chat, limit(a.Limit, 50))
		}),
		"search": define(false, func(ctx context.Context, c *core.Core, caller core.Caller, a SearchArgs) (any, error) {
			return c.Search(ctx, caller, a.Query, a.Chat, limit(a.Limit, 30))
		}),
		"send": define(false, func(ctx context.Context, c *core.Core, caller core.Caller, a SendArgs) (any, error) {
			text, err := c.Send(ctx, caller, a.Chat, a.Text)
			return Text{text}, err
		}),
		"react": define(false, func(ctx context.Context, c *core.Core, caller core.Caller, a MessageArgs) (any, error) {
			text, err := c.React(ctx, caller, a.Chat, a.MessageID, a.Emoji)
			return Text{text}, err
		}),
		"download_media": define(false, func(ctx context.Context, c *core.Core, caller core.Caller, a MessageArgs) (any, error) {
			path, err := c.DownloadMedia(ctx, caller, a.Chat, a.MessageID)
			return Text{path}, err
		}),
		"route_here": define(false, func(ctx context.Context, c *core.Core, caller core.Caller, a RouteArgs) (any, error) {
			text, err := c.RouteHere(ctx, caller, a.Chat, a.Prefix, a.Addressed)
			return Text{text}, err
		}),
		"list_sessions": define(false, func(ctx context.Context, c *core.Core, _ core.Caller, _ struct{}) (any, error) {
			return c.Sessions(ctx)
		}),
		"list_chats": define(false, func(ctx context.Context, c *core.Core, _ core.Caller, a QueryArgs) (any, error) {
			return c.Chats(ctx, a.Query, limit(a.Limit, 30))
		}),
		"command": define(false, func(ctx context.Context, c *core.Core, caller core.Caller, a CommandArgs) (any, error) {
			text, err := c.Run(ctx, caller, a.Text)
			return Text{text}, err
		}),
		"status": define(false, func(ctx context.Context, c *core.Core, _ core.Caller, _ struct{}) (any, error) {
			return c.Status(ctx)
		}),
		"approve": define(false, approve),
		"export": define(true, func(ctx context.Context, c *core.Core, _ core.Caller, a ChatArgs) (any, error) {
			return c.Export(ctx, a.Chat)
		}),
		"changes": define(true, func(ctx context.Context, c *core.Core, _ core.Caller, a ChangesArgs) (any, error) {
			return c.Changes(ctx, a.Cursor)
		}),
	}
}

// approvalWait is how long the approve tool waits for the owner.
var approvalWait = 30 * time.Minute

// approve answers a Claude Code permission prompt with the owner's answer from the self-chat,
// in the shape that `--permission-prompt-tool` expects.
func approve(ctx context.Context, c *core.Core, caller core.Caller, a ApproveArgs) (any, error) {
	input := a.Input
	if len(input) == 0 {
		input = json.RawMessage("{}")
	}
	if c.Approve(ctx, caller, a.ToolName, string(input), approvalWait) {
		return map[string]any{"behavior": "allow", "updatedInput": input}, nil
	}
	return map[string]any{"behavior": "deny", "message": "The owner denied this in WhatsApp."}, nil
}
