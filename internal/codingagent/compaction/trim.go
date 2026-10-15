package compaction

import (
	"slices"

	"github.com/alexrudloff/wopr/agent"
)

// TrimMessages keeps the leading system messages and the newest messages whose
// estimated tokens fit maxTokens; it drops oldest non-system messages. The kept
// suffix always starts at a cut point, so no tool result is separated from its
// tool call. maxTokens <= 0 or an already-fitting list is returned unchanged.
// This bounds one request's context; it does not truncate the retained
// transcript.
func TrimMessages(messages []agent.AgentMessage, maxTokens int) []agent.AgentMessage {
	if maxTokens <= 0 || EstimateMessagesTokens(messages) <= maxTokens {
		return messages
	}

	// The instruction baseline is always leading in the projection.
	sys := 0
	for sys < len(messages) && messages[sys].System != nil {
		sys++
	}
	prefixTokens := EstimateMessagesTokens(messages[:sys])
	if prefixTokens >= maxTokens {
		return messages[:sys]
	}

	// Walk backward accumulating the newest messages until the budget is hit.
	budget := maxTokens - prefixTokens
	start := len(messages)
	tokens := 0
	for i := len(messages) - 1; i >= sys; i-- {
		tokens += EstimateTokens(messages[i])
		if tokens > budget {
			break
		}
		start = i
	}
	// Never start on a tool result: it must follow its tool call.
	for start < len(messages) && !isCutPointMessage(messages[start]) {
		start++
	}
	// Nothing fit: keep the last cut point and its trailing turn even when it
	// alone exceeds the budget. No preceding cut point means never orphan a
	// tool result.
	if start >= len(messages) {
		start = -1
		for j := len(messages) - 1; j > sys; j-- {
			if isCutPointMessage(messages[j]) {
				start = j
				break
			}
		}
		if start < 0 {
			return messages
		}
	}
	if start <= sys {
		return messages
	}
	return append(slices.Clone(messages[:sys]), messages[start:]...)
}
