package mcp

// schema builds a JSON Schema object with the given properties and required names.
func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func integer(description string) map[string]any {
	return map[string]any{"type": "integer", "description": description}
}

const chatHelp = "The chat: a JID such as 15551234567@s.whatsapp.net or 1203...@g.us, a phone number, " +
	"\"self\" for the owner's own chat, or a unique part of the chat's name."

var toolList = []map[string]any{
	{
		"name":        "read_chat",
		"description": "Read the latest messages of a WhatsApp chat, oldest first.",
		"inputSchema": schema(map[string]any{"chat": str(chatHelp), "limit": integer("How many messages, default 50.")}, "chat"),
	},
	{
		"name":        "search",
		"description": "Search stored WhatsApp messages for a text, newest first.",
		"inputSchema": schema(map[string]any{
			"query": str("The text to find."),
			"chat":  str("Optional chat to search in. " + chatHelp),
			"limit": integer("How many messages, default 30."),
		}, "query"),
	},
	{
		"name":        "send",
		"description": "Send a text message to a WhatsApp chat. The owner's send rules decide whether it goes out at once, waits for the owner's yes, or is refused; the result says which.",
		"inputSchema": schema(map[string]any{"chat": str(chatHelp), "text": str("The message text.")}, "chat", "text"),
	},
	{
		"name":        "react",
		"description": "React to a WhatsApp message with an emoji. An empty emoji removes the reaction.",
		"inputSchema": schema(map[string]any{
			"chat":       str(chatHelp),
			"message_id": str("The message ID, from the id= field of a message line."),
			"emoji":      str("The emoji, such as 👀."),
		}, "chat", "message_id", "emoji"),
	},
	{
		"name":        "download_media",
		"description": "Save the photo, video, audio or document of a WhatsApp message to a file and return its path.",
		"inputSchema": schema(map[string]any{"chat": str(chatHelp), "message_id": str("The message ID.")}, "chat", "message_id"),
	},
	{
		"name":        "route_here",
		"description": "Ask the owner to route a WhatsApp chat to this session, so its new messages come here. The owner confirms in WhatsApp.",
		"inputSchema": schema(map[string]any{
			"chat":      str(chatHelp),
			"prefix":    str("Optional text added in front of every message this session sends to the chat."),
			"addressed": map[string]any{"type": "boolean", "description": "Only push messages that name the agent or answer its messages."},
		}, "chat"),
	},
	{
		"name":        "list_sessions",
		"description": "List the agent sessions that WhatsAgent knows, with their harness, folder, push state and waiting messages.",
		"inputSchema": schema(map[string]any{}),
	},
	{
		"name":        "list_chats",
		"description": "Find WhatsApp chats by name or JID, most recently active first.",
		"inputSchema": schema(map[string]any{"query": str("Part of the chat name or JID; empty lists all."), "limit": integer("How many chats, default 30.")}),
	},
	{
		"name":        "command",
		"description": "Run a WhatsAgent command such as /status, /routes, /rules or /route <chat> <session>. Commands that change routes or rules wait for the owner's yes.",
		"inputSchema": schema(map[string]any{"text": str("The command line, starting with /.")}, "text"),
	},
	{
		"name":        "approve",
		"description": "Ask the owner in WhatsApp whether a tool call may run. WhatsAgent's home session uses it as its permission prompt tool.",
		"inputSchema": schema(map[string]any{
			"tool_name": str("The tool that wants to run."),
			"input":     map[string]any{"type": "object", "description": "The tool's input."},
		}, "tool_name"),
	},
}
