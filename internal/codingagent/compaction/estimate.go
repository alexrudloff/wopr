package compaction

import (
	"slices"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

// EstimateTokens estimates one context message as ai.EstimateMessageTokens
// sizes its provider form. A message not sent to the provider counts as zero.
func EstimateTokens(message agent.AgentMessage) int {
	llm, ok := message.LLMMessage()
	if !ok {
		return 0
	}
	return ai.EstimateMessageTokens(llm)
}

// EstimateMessagesTokens sums EstimateTokens over messages.
func EstimateMessagesTokens(messages []agent.AgentMessage) int {
	tokens := 0
	for _, message := range messages {
		tokens += EstimateTokens(message)
	}
	return tokens
}

// EstimateContextTokens is ai.EstimateContextTokens over the provider form of
// messages; LastUsageIndex indexes messages.
func EstimateContextTokens(messages []agent.AgentMessage) ai.ContextUsageEstimate {
	llm := make([]ai.Message, len(messages))
	for i, message := range messages {
		llm[i], _ = message.LLMMessage()
	}
	return ai.EstimateContextTokens(llm)
}

// EstimateProjectedContextTokens estimates a projected context without
// trusting usage captured before a later context edit or compaction on the
// branch. Without trusted
// usage, it counts the replayed current system state once plus every
// non-system projected message.
func EstimateProjectedContextTokens(projection codingagent.SessionProjection, branchEntries []codingagent.SessionEntry) ai.ContextUsageEstimate {
	estimate := EstimateContextTokens(projection.Messages)
	if estimate.LastUsageIndex >= 0 {
		usageEntryID := ""
		projectedIndex := 0
		for _, entry := range projection.Entries {
			next := projectedIndex + len(entry.Messages)
			if estimate.LastUsageIndex < next {
				usageEntryID = entry.SourceEntry.Base.ID
				break
			}
			projectedIndex = next
		}
		usageEntryIndex := -1
		if usageEntryID != "" {
			usageEntryIndex = slices.IndexFunc(branchEntries, func(entry codingagent.SessionEntry) bool {
				return entry.Base.ID == usageEntryID
			})
		}
		latestInvalidating := -1
		for i, entry := range slices.Backward(branchEntries) {
			if entry.Base.Type == "context_edit" || entry.Base.Type == "compaction" {
				latestInvalidating = i
				break
			}
		}
		if usageEntryIndex > latestInvalidating {
			return estimate
		}
	}
	tokens := 0
	if current := codingagent.CurrentSystemMessage(projection.Messages); current != nil {
		tokens = ai.EstimateMessageTokens(*current)
	}
	for _, message := range projection.Messages {
		if message.Role() != "system" {
			tokens += EstimateTokens(message)
		}
	}
	return ai.ContextUsageEstimate{Tokens: tokens, TrailingTokens: tokens, LastUsageIndex: -1}
}
