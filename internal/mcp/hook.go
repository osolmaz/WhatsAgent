package mcp

import (
	"encoding/json"
	"html"
	"sort"
	"strings"
	"time"

	"github.com/osolmaz/WhatsAgent/internal/claudecode"
	"github.com/osolmaz/WhatsAgent/internal/model"
)

// hookTool is the tool that Claude Code's steering hooks call. Only the hooks need it, but MCP has
// no hidden tools, so its description keeps the agent away.
var hookTool = map[string]any{
	"name": claudecode.HookTool,
	"description": "Claude Code's WhatsAgent hooks call this tool to pass WhatsApp messages into a running turn. " +
		"Do not call it yourself.",
	"inputSchema": schema(map[string]any{
		"event":    str("The Claude Code hook event."),
		"agent_id": str("The subagent ID, empty for the main agent."),
	}, "event"),
}

// tools lists the WhatsAgent tools, with the hook tool when channel pushes are on.
func (s *Server) tools() []map[string]any {
	if !s.opts.Push {
		return toolList
	}
	return append(append([]map[string]any{}, toolList...), hookTool)
}

// hook answers a steering hook call with the hook output that Claude Code reads: the held messages
// as additional context, or an empty object when none are held.
func (s *Server) hook(raw json.RawMessage) map[string]any {
	var in struct {
		Event   string `json:"event"`
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return toolError(err)
	}
	held := s.steering.Hook(in.Event, in.AgentID)
	if len(held) == 0 {
		return toolText("{}")
	}
	tags := make([]string, len(held))
	for i, e := range held {
		tags[i] = channelTag(e, s.opts.Location)
	}
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName":     in.Event,
		"additionalContext": "WhatsApp messages that arrived while you worked:\n" + strings.Join(tags, "\n"),
	}})
	return toolText(string(out))
}

// channelTag writes an event in the tag that Claude Code gives a channel message, so the agent
// sees a steered message in the same form as a pushed one. A closing tag inside the text is
// broken up, so that someone else's message cannot end the tag and pose as the owner.
func channelTag(e model.Event, loc *time.Location) string {
	params := channelEvent(e, loc)
	meta := params["meta"].(map[string]string)
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(`<channel source="whatsagent"`)
	for _, k := range keys {
		b.WriteString(" " + k + `="` + html.EscapeString(meta[k]) + `"`)
	}
	content := strings.ReplaceAll(params["content"].(string), "</channel", `<\/channel`)
	b.WriteString(">\n" + content + "\n</channel>")
	return b.String()
}
