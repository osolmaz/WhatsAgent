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

// readOnly marks a tool that changes nothing, so a harness such as Codex runs it without asking.
var readOnly = map[string]any{"readOnlyHint": true}

var toolList = []map[string]any{
	{
		"name":        "read_chat",
		"description": "Read the latest messages of a WhatsApp chat that you may read, oldest first. The chat's mode decides which: every message, only the owner's messages that name the agents, or the messages of the open window.",
		"inputSchema": schema(map[string]any{"chat": str(chatHelp), "limit": integer("How many messages, default 50.")}, "chat"),
		"annotations": readOnly,
	},
	{
		"name":        "search",
		"description": "Search the stored WhatsApp messages that you may read for a text, newest first.",
		"inputSchema": schema(map[string]any{
			"query": str("The text to find."),
			"chat":  str("Optional chat to search in. " + chatHelp),
			"limit": integer("How many messages, default 30."),
		}, "query"),
		"annotations": readOnly,
	},
	{
		"name":        "send",
		"description": "Send a text message, a file, or a file with the text as its caption to a WhatsApp chat, optionally as a reply to one of the chat's messages. WhatsAgent puts the agent prefix, such as the agents' name, in front of the text, so write only the message. To mention someone, write @ and their phone number, such as @15551234567; WhatsApp shows their name. An answer in a chat that sent you a message in the last 15 minutes goes out at once; a message to any other chat waits for the owner's yes, which WhatsAgent asks for itself with the exact text and file. The owner's send rules can change both; the result says what happened.",
		"inputSchema": schema(map[string]any{
			"chat":     str(chatHelp),
			"text":     str("The message text, or the caption of the file."),
			"file":     str("Optional path of a file to send, up to 100 MB; a relative path starts in your working folder. JPEG and PNG go as photos, MP4 as a video, Ogg/Opus as a voice message, MP3, M4A and AAC as audio, and anything else as a document. Audio has no caption in WhatsApp, so its text goes as a message before it."),
			"reply_to": str("Optional ID of the message in this chat to answer, from the id= field of a message line. WhatsApp shows it quoted above the reply."),
		}, "chat"),
	},
	{
		"name":        "edit_message",
		"description": "Change the text of a message that WhatsAgent sent for an agent. WhatsApp allows this for text messages up to 15 minutes old. Every edit waits for the owner's yes, which WhatsAgent asks for itself; no send rule skips it.",
		"inputSchema": schema(map[string]any{
			"chat":       str(chatHelp),
			"message_id": str("The message ID."),
			"text":       str("The new text. WhatsAgent puts the agent prefix in front, as for send."),
		}, "chat", "message_id", "text"),
	},
	{
		"name":        "delete_message",
		"description": "Delete a message that WhatsAgent sent for an agent, for everyone in the chat. Every deletion waits for the owner's yes, which WhatsAgent asks for itself; no send rule skips it.",
		"inputSchema": schema(map[string]any{"chat": str(chatHelp), "message_id": str("The message ID.")}, "chat", "message_id"),
	},
	{
		"name":        "forward_message",
		"description": "Forward a message that you may read to a chat, as WhatsApp's forward does. A message with your prefix and the optional text goes first, so the chat sees who forwarded it. The send rules of the target chat apply as for send.",
		"inputSchema": schema(map[string]any{
			"chat":       str("The chat of the message. " + chatHelp),
			"message_id": str("The message ID."),
			"to":         str("The chat to forward it to."),
			"text":       str("Optional comment that goes before the forwarded message."),
		}, "chat", "message_id", "to"),
	},
	{
		"name":        "send_poll",
		"description": "Send a WhatsApp poll to a chat. WhatsAgent puts the agent prefix in front of the question. Each vote arrives as a message that answers the poll and names the options that the voter picked; a later vote of the same person replaces the earlier one. The send rules apply as for send.",
		"inputSchema": schema(map[string]any{
			"chat":     str(chatHelp),
			"question": str("The question."),
			"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 2, "maxItems": 12, "description": "2 to 12 different options."},
			"multiple": map[string]any{"type": "boolean", "description": "Let each voter pick any number of options; by default a voter picks one."},
		}, "chat", "question", "options"),
	},
	{
		"name":        "send_location",
		"description": "Send a place on the map to a chat. WhatsApp shows no text with a location, so the text, with the agent prefix, goes first as a message of its own. The send rules apply as for send.",
		"inputSchema": schema(map[string]any{
			"chat":      str(chatHelp),
			"latitude":  map[string]any{"type": "number", "description": "Degrees north, -90 to 90."},
			"longitude": map[string]any{"type": "number", "description": "Degrees east, -180 to 180."},
			"name":      str("Optional name of the place."),
			"address":   str("Optional address of the place."),
			"text":      str("Optional text that goes before the location."),
		}, "chat", "latitude", "longitude"),
	},
	{
		"name":        "typing",
		"description": "Show the owner as typing in a chat for a few seconds, while you prepare an answer that takes a while. It works only in a chat where you may answer at once, and WhatsApp ends it when your message arrives.",
		"inputSchema": schema(map[string]any{"chat": str(chatHelp)}, "chat"),
	},
	{
		"name":        "mark_read",
		"description": "Mark a message, and the chat's earlier messages, as read: the owner's phone shows them as read and the sender sees blue ticks. The send rules apply as for send.",
		"inputSchema": schema(map[string]any{"chat": str(chatHelp), "message_id": str("The message ID.")}, "chat", "message_id"),
	},
	{
		"name":        "group_info",
		"description": "Show a WhatsApp group that you may read: name, description, settings and members with their admin role.",
		"inputSchema": schema(map[string]any{"chat": str(chatHelp)}, "chat"),
		"annotations": readOnly,
	},
	{
		"name":        "manage_group",
		"description": "Change a WhatsApp group. Every change waits for the owner's yes, and WhatsApp allows most of them only when the owner is a group admin. rename and describe take text; add, remove, promote and demote take members; announce (only admins write) and lock (only admins change the details) take on; invite_link, reset_invite_link and leave take nothing.",
		"inputSchema": schema(map[string]any{
			"chat": str(chatHelp),
			"action": map[string]any{"type": "string", "enum": []string{"rename", "describe", "add", "remove", "promote", "demote",
				"announce", "lock", "invite_link", "reset_invite_link", "leave"}},
			"text":    str("The new name (up to 25 characters) or description."),
			"members": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "People, by phone number or JID."},
			"on":      map[string]any{"type": "boolean", "description": "Turn announce or lock on (true) or off (false)."},
		}, "chat", "action"),
	},
	{
		"name":        "create_group",
		"description": "Create a WhatsApp group with the owner and the given people. It waits for the owner's yes.",
		"inputSchema": schema(map[string]any{
			"name":    str("The group's name, up to 25 characters."),
			"members": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "People, by phone number or JID."},
		}, "name", "members"),
	},
	{
		"name":        "join_group",
		"description": "Join a WhatsApp group by its invite link. It waits for the owner's yes.",
		"inputSchema": schema(map[string]any{"link": str("The invite link, such as https://chat.whatsapp.com/AbCdEf.")}, "link"),
	},
	{
		"name":        "find_contacts",
		"description": "Find people in the owner's address book by name or number. A phone number in international form is also checked on WhatsApp. Only the owner's own sessions and home may use it.",
		"inputSchema": schema(map[string]any{"query": str("Part of a name, or a phone number."), "limit": integer("How many people, default 30.")}, "query"),
		"annotations": readOnly,
	},
	{
		"name":        "contact_info",
		"description": "Show what WhatsApp shows of a person: about text, business name and the address of the profile picture, as far as their privacy settings allow. Only the owner's own sessions and home may use it.",
		"inputSchema": schema(map[string]any{"person": str("The person, by phone number or JID.")}, "person"),
		"annotations": readOnly,
	},
	{
		"name":        "set_profile",
		"description": "Change the owner's WhatsApp profile: name, about text or picture. It waits for the owner's yes, which shows the new picture.",
		"inputSchema": schema(map[string]any{
			"name":    str("Optional new name."),
			"about":   str("Optional new about text."),
			"picture": str("Optional path of a JPEG or PNG photo; WhatsAgent uses its centered square."),
		}),
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
		"description": "Ask the owner to let this session handle a WhatsApp chat, so its new messages come here and you can reply with send. The owner confirms in WhatsApp.",
		"inputSchema": schema(map[string]any{
			"chat":   str(chatHelp),
			"prefix": str("Optional prefix for this chat in place of the owner's /prefix. {name} and {session} are filled in."),
			"mode": map[string]any{"type": "string", "enum": []string{"all", "keyword", "window", "off"},
				"description": "What this session sees of the chat: all (the default) is every message; keyword is only the owner's messages that name the agents; window is every message while a window that the owner opens by naming the agents is open."},
		}, "chat"),
	},
	{
		"name":        "list_sessions",
		"description": "List the agent sessions that WhatsAgent knows, with their harness, folder, push state and waiting messages.",
		"inputSchema": schema(map[string]any{}),
		"annotations": readOnly,
	},
	{
		"name":        "list_chats",
		"description": "Find WhatsApp chats by name or JID, most recently active first.",
		"inputSchema": schema(map[string]any{"query": str("Part of the chat name or JID; empty lists all."), "limit": integer("How many chats, default 30.")}),
		"annotations": readOnly,
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

// ToolNames returns the names of the WhatsAgent MCP tools.
func ToolNames() []string {
	names := make([]string, len(toolList))
	for i, t := range toolList {
		names[i] = t["name"].(string)
	}
	return names
}
