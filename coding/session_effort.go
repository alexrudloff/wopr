package coding

import (
	"slices"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Thinking effort per route. A router model's "effort" is its default
// thinking level, and a failed turn raises the level for the next request:
// small models waste the least on easy turns and think harder where they
// just failed.

// maxThinkingBoost bounds how far failed turns raise thinking.
const maxThinkingBoost = 2

// initRouteEffort starts a new session on the router's effort for model
// when no thinking level was chosen in settings or on the command line.
func (s *Session) initRouteEffort(model *ai.Model) {
	if model == nil || s.router == nil {
		return
	}
	if effort := s.router.Effort(providerID(model) + "/" + model.ID); effort != "" {
		s.agent.SetThinkingLevel(ai.ClampThinkingLevel(model, ai.ThinkingLevel(effort)))
	}
}

// observeTurnOutcome raises the thinking boost after a turn whose tool
// calls all failed and lowers it after one whose tool calls all succeeded.
func (s *Session) observeTurnOutcome(results []agent.ToolResultMessage) {
	if s.router == nil || !s.router.EscalateThinking() || len(results) == 0 {
		return
	}
	failed := 0
	for _, r := range results {
		if r.IsError {
			failed++
		}
	}
	switch boost := s.thinkingBoost.Load(); {
	case failed == len(results) && boost < maxThinkingBoost:
		s.thinkingBoost.Store(boost + 1)
	case failed == 0 && boost > 0:
		s.thinkingBoost.Store(boost - 1)
	}
}

// boostedThinking raises level by the current boost within the levels
// model supports.
func (s *Session) boostedThinking(model *ai.Model, level ai.ThinkingLevel) ai.ThinkingLevel {
	boost := int(s.thinkingBoost.Load())
	if boost == 0 || model == nil {
		return level
	}
	levels := slices.DeleteFunc(ai.GetSupportedThinkingLevels(model), func(l ai.ThinkingLevel) bool { return l == ai.ThinkingMinimal })
	i := slices.Index(levels, level)
	if i < 0 {
		return level
	}
	return levels[min(i+boost, len(levels)-1)]
}
