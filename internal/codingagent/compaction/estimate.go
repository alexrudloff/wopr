package compaction

import (
	"slices"
	"sync"

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
// branch. Without trusted usage, it counts the replayed current system state
// once plus every non-system projected message. When only context edits
// follow the last usage, that count is scaled by how far it undercounted the
// branch as of that usage: the character estimate runs well under a
// provider's real count on code and tool output, and an uncalibrated count
// after pruning let a conversation fill its window without compacting.
func EstimateProjectedContextTokens(projection codingagent.SessionProjection, branchEntries []codingagent.SessionEntry) ai.ContextUsageEstimate {
	estimate := EstimateContextTokens(projection.Messages)
	usageEntryIndex := -1
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
	tokens := countProjected(projection.Messages)
	if usageEntryIndex >= 0 && !slices.ContainsFunc(branchEntries[usageEntryIndex+1:], func(entry codingagent.SessionEntry) bool {
		return entry.Base.Type == "compaction"
	}) {
		if counted := countedAtUsage(branchEntries[:usageEntryIndex+1]); counted > 0 && estimate.UsageTokens > counted {
			tokens = int(int64(tokens) * int64(estimate.UsageTokens) / int64(counted))
		}
	}
	return ai.ContextUsageEstimate{Tokens: tokens, TrailingTokens: tokens, LastUsageIndex: -1}
}

// usageCount caches countedAtUsage for the last usage entry: the branch up to
// it is append-only, so its count never changes, and projecting a long
// branch takes tens of milliseconds.
var usageCount struct {
	sync.Mutex
	id     string
	tokens int
}

// countedAtUsage is countProjected of the branch ending at its usage entry.
func countedAtUsage(path []codingagent.SessionEntry) int {
	id := path[len(path)-1].Base.ID
	usageCount.Lock()
	defer usageCount.Unlock()
	if usageCount.id != id {
		usageCount.id, usageCount.tokens = id, countProjected(codingagent.BuildSessionProjection(path).Messages)
	}
	return usageCount.tokens
}

// countProjected is the character estimate of a projected context: the
// replayed current system state once plus every non-system message.
func countProjected(messages []agent.AgentMessage) int {
	tokens := 0
	if current := codingagent.CurrentSystemMessage(messages); current != nil {
		tokens = ai.EstimateMessageTokens(*current)
	}
	for _, message := range messages {
		if message.Role() != "system" {
			tokens += EstimateTokens(message)
		}
	}
	return tokens
}
