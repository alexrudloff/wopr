package agent

import (
	"encoding/json"
	"fmt"

	"github.com/alexrudloff/wopr/ai"
)

// ConvertToLLM converts AgentMessages to the wire format expected by the AI provider.
// It calls NormalizeMessages first to filter errored/aborted assistant messages and
// insert synthetic tool results for orphaned tool calls.
func ConvertToLLM(msgs []AgentMessage, model *ai.Model) []ai.Message {
	msgs = NormalizeMessages(msgs, model)
	out := make([]ai.Message, 0, len(msgs))
	for _, m := range msgs {
		if message, ok := m.LLMMessage(); ok {
			out = append(out, message)
		}
	}
	return out
}

// LLMMessage returns the provider-facing form of m, or false when m is not sent
// to the provider (an unknown role, or a bash execution excluded from context).
// Every known custom role becomes a user message whose content is a text-block
// array; "custom" block-array content passes through.
func (m AgentMessage) LLMMessage() (ai.Message, bool) {
	switch {
	case m.System != nil:
		return *m.System, true
	case m.User != nil:
		return ai.UserMessage{Content: ai.UserContentBlocks(m.User.Content), Timestamp: m.User.Timestamp}, true
	case m.Assistant != nil:
		message := m.Assistant.LLMMessage()
		if len(message.Content) == 0 {
			message.Content = []ai.AssistantContentBlock{ai.TextContent{Text: ""}}
		}
		return message, true
	case m.ToolResult != nil:
		return ai.ToolResultMessage{
			ToolCallID: m.ToolResult.ToolCallID, ToolName: m.ToolResult.ToolName,
			Content: m.ToolResult.Content, Details: m.ToolResult.Details,
			Usage: m.ToolResult.Usage, IsError: m.ToolResult.IsError,
			Timestamp: m.ToolResult.Timestamp,
		}, true
	case m.Custom != nil:
		role, _ := m.Custom["role"].(string)
		switch role {
		case RoleBashExecution, RoleBranchSummary, RoleCompactionSummary:
			if excluded, _ := m.Custom["excludeFromContext"].(bool); excluded && role == RoleBashExecution {
				return nil, false
			}
			return ai.UserMessage{
				Content:   ai.UserContentBlocks{ai.TextContent{Text: customMessageText(m.Custom)}},
				Timestamp: customTimestamp(m.Custom),
			}, true
		case RoleCustom:
			if content, ok := customMessageContent(m.Custom["content"]); ok {
				return ai.UserMessage{Content: content, Timestamp: customTimestamp(m.Custom)}, true
			}
		}
	}
	return nil, false
}

// customMessageContent converts a "custom" message's content to user content
// blocks: a string becomes one text block and a block array passes through,
// whether built in memory or decoded from JSON. Missing content is an empty
// array, as session projection stores it.
func customMessageContent(content any) (ai.UserContentBlocks, bool) {
	switch content := content.(type) {
	case string:
		return ai.UserContentBlocks{ai.TextContent{Text: content}}, true
	case nil:
		return ai.UserContentBlocks{}, true
	case ai.UserContentBlocks:
		return content, true
	case []ai.UserContentBlock:
		return ai.UserContentBlocks(content), true
	}
	raw, err := json.Marshal(map[string]any{"role": RoleUser, "content": content})
	if err != nil {
		return nil, false
	}
	var decoded AgentMessage
	if json.Unmarshal(raw, &decoded) != nil || decoded.User == nil {
		return nil, false
	}
	if decoded.User.Content == nil {
		return ai.UserContentBlocks{}, true
	}
	return ai.UserContentBlocks(decoded.User.Content), true
}

// customTimestamp returns a custom message's timestamp in Unix milliseconds,
// as built in memory (int64) or decoded from JSON (float64), or 0.
func customTimestamp(m map[string]any) int64 {
	switch timestamp := m["timestamp"].(type) {
	case int64:
		return timestamp
	case int:
		return int64(timestamp)
	case float64:
		return int64(timestamp)
	case json.Number:
		value, _ := timestamp.Int64()
		return value
	}
	return 0
}

// LLMMessage returns the provider-facing form of an assistant message. It
// shares the content and diagnostics slices with m.
func (m *AssistantMessage) LLMMessage() ai.AssistantMessage {
	usage := ai.Usage{}
	if m.Usage != nil {
		usage = *m.Usage
	}
	return ai.AssistantMessage{
		Content: m.Content, API: m.API, Provider: m.Provider,
		Model: m.ModelID, ResponseModel: m.ResponseModel,
		ResponseID: m.ResponseID, ProviderThinkingLevel: m.ProviderThinkingLevel,
		Diagnostics: m.Diagnostics,
		Usage:       usage, StopReason: m.StopReason, Deferred: m.Deferred,
		ErrorMessage: m.ErrorMessage, RawStopReason: m.RawStopReason,
		EndTurn: m.EndTurn, Timestamp: m.Timestamp,
	}
}

// BranchSummaryContextText wraps a branch summary for LLM context.
func BranchSummaryContextText(summary string) string {
	return "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n" + summary + "</summary>"
}

// CompactionSummaryContextText wraps a compaction summary for LLM context.
func CompactionSummaryContextText(summary string) string {
	return "The conversation history before this point was compacted into the following summary:\n\n<summary>\n" + summary + "\n</summary>"
}

// customMessageText renders a bash execution, branch summary, or compaction
// summary custom message as a plain text string for LLM context.
func customMessageText(m map[string]any) string {
	summary, _ := m["summary"].(string)
	switch m["role"] {
	case RoleBashExecution:
		return bashExecutionToText(m)
	case RoleBranchSummary:
		return BranchSummaryContextText(summary)
	default:
		return CompactionSummaryContextText(summary)
	}
}

// bashExecutionToText converts a BashExecutionMessage to user message text for
// LLM context. The caller drops excludeFromContext executions.
func bashExecutionToText(m map[string]any) string {
	cmd, _ := m["command"].(string)
	output, _ := m["output"].(string)
	cancelled, _ := m["cancelled"].(bool)
	truncated, _ := m["truncated"].(bool)
	fullOutputPath, _ := m["fullOutputPath"].(string)

	text := "Ran `" + cmd + "`\n"
	if output != "" {
		text += "```\n" + output + "\n```"
	} else {
		text += "(no output)"
	}
	if cancelled {
		text += "\n\n(command cancelled)"
	} else if exitCode, ok := m["exitCode"].(float64); ok && exitCode != 0 {
		text += fmt.Sprintf("\n\nCommand exited with code %d", int(exitCode))
	}
	if truncated && fullOutputPath != "" {
		text += fmt.Sprintf("\n\n[Output truncated. Full output: %s]", fullOutputPath)
	}
	return text
}
