package pruning

import (
	"fmt"
	"slices"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Marker is the id prefix the compress tool addresses.
func Marker(ordinal int) string { return fmt.Sprintf("[#%d] ", ordinal) }

// Mark prefixes each user and tool-result message that has an ordinal with
// its marker. ordinals is parallel to messages; 0 means no marker. Messages
// are copied, never mutated. The marker depends only on the entry, so a
// marked message is byte-identical on every request.
func Mark(messages []agent.AgentMessage, ordinals []int) []agent.AgentMessage {
	out := messages
	copied := false
	for i, message := range messages {
		if i >= len(ordinals) || ordinals[i] == 0 {
			continue
		}
		marker := Marker(ordinals[i])
		var replaced agent.AgentMessage
		switch {
		case message.User != nil:
			user := *message.User
			user.Content = markUser(user.Content, marker)
			replaced = agent.AgentMessage{User: &user}
		case message.ToolResult != nil:
			result := *message.ToolResult
			result.Content = markResult(result.Content, marker)
			replaced = agent.AgentMessage{ToolResult: &result}
		default:
			continue
		}
		if !copied {
			out = slices.Clone(messages)
			copied = true
		}
		out[i] = replaced
	}
	return out
}

func markUser(content []ai.UserContentBlock, marker string) []ai.UserContentBlock {
	if len(content) > 0 {
		if text, ok := content[0].(ai.TextContent); ok {
			out := slices.Clone(content)
			text.Text = marker + text.Text
			out[0] = text
			return out
		}
	}
	return append([]ai.UserContentBlock{ai.TextContent{Text: marker}}, content...)
}

func markResult(content []ai.ToolResultMessageContent, marker string) []ai.ToolResultMessageContent {
	if len(content) > 0 {
		if text, ok := content[0].(ai.TextContent); ok {
			out := slices.Clone(content)
			text.Text = marker + text.Text
			out[0] = text
			return out
		}
	}
	return append([]ai.ToolResultMessageContent{ai.TextContent{Text: marker}}, content...)
}
