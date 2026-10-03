package ai

// ThinkingBudgets maps thinking level names to token budgets.
type ThinkingBudgets struct {
	Minimal int
	Low     int
	Medium  int
	High    int
}

// DefaultThinkingBudgets returns the default budget map.
func DefaultThinkingBudgets() ThinkingBudgets {
	return ThinkingBudgets{
		Minimal: 1024,
		Low:     2048,
		Medium:  8192,
		High:    16384,
	}
}

// AdjustMaxTokensForThinking computes maxTokens and thinkingBudget given an
// optional caller max, model limit, thinking level, and optional custom budgets.
// A nil base max means "unset": use the model cap and fit the thinking budget
// inside it.
func AdjustMaxTokensForThinking(baseMaxTokens *int, modelMaxTokens int, reasoningLevel string, custom *ThinkingBudgets) (maxTokens, thinkingBudget int) {
	budgets := DefaultThinkingBudgets()
	if custom != nil {
		if custom.Minimal > 0 {
			budgets.Minimal = custom.Minimal
		}
		if custom.Low > 0 {
			budgets.Low = custom.Low
		}
		if custom.Medium > 0 {
			budgets.Medium = custom.Medium
		}
		if custom.High > 0 {
			budgets.High = custom.High
		}
	}

	const minOutputTokens = 1024
	level := reasoningLevel
	if level == string(ThinkingXHigh) || level == string(ThinkingMax) {
		level = string(ThinkingHigh)
	}

	switch level {
	case "minimal":
		thinkingBudget = budgets.Minimal
	case "low":
		thinkingBudget = budgets.Low
	case "medium":
		thinkingBudget = budgets.Medium
	case "high":
		thinkingBudget = budgets.High
	default:
		thinkingBudget = 0
	}

	if baseMaxTokens == nil {
		maxTokens = modelMaxTokens
	} else {
		maxTokens = min(*baseMaxTokens+thinkingBudget, modelMaxTokens)
	}
	if maxTokens <= thinkingBudget {
		thinkingBudget = max(0, maxTokens-minOutputTokens)
	}
	return maxTokens, thinkingBudget
}

// Request budget constants.
const (
	contextSafetyTokens = 4096
	minMaxTokens        = 1
)

// ClampMaxTokensToContext reduces a requested output budget to what fits in
// the model's context window after the estimated request context and a safety
// margin, never below one token.
func ClampMaxTokensToContext(model *Model, context TranscriptContext, maxTokens int) int {
	contextWindow := model.Capabilities.ContextWindow
	if contextWindow <= 0 {
		return max(minMaxTokens, maxTokens)
	}
	estimate := EstimateContextTokens(context.messages).Tokens
	// An estimate past the window itself can't be the context the provider
	// just accepted, so it's wrong: clamping to it would send max_tokens=1
	// and every reply would stop after one token. Send the model's limit and
	// let a real overflow come back as the provider's error, which
	// compaction handles.
	if estimate > contextWindow {
		return maxTokens
	}
	available := contextWindow - estimate - contextSafetyTokens
	return min(maxTokens, max(minMaxTokens, available))
}
