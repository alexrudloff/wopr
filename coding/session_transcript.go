package coding

import (
	"context"
	"slices"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/prompts"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// restoreAgentMessages retains the current instruction baseline when a session branch or compaction context contains only conversation entries.
func restoreAgentMessages(a *agent.Agent, messages []agent.AgentMessage) {
	a.SetMessages(withInstructionBaseline(a.Messages(), messages))
}

// withInstructionBaseline prepends the current system message of current to
// messages when messages start without one.
func withInstructionBaseline(current, messages []agent.AgentMessage) []agent.AgentMessage {
	if len(messages) == 0 || messages[0].System != nil {
		return messages
	}
	var systems []ai.Message
	for _, m := range current {
		if m.System != nil {
			systems = append(systems, *m.System)
		}
	}
	if baseline := ai.GetCurrentSystemMessage(systems); baseline != nil {
		messages = append([]agent.AgentMessage{{System: baseline}}, messages...)
	}
	return messages
}

// preparePrompt applies the Session instruction baseline when the first user
// prompt runs. The agent adds the active tool declarations to this same message.
func (s *Session) preparePrompt(_ context.Context, messages []agent.AgentMessage) ([]agent.AgentMessage, error) {
	text := lastUserPrompt(messages)
	if efficiency.IsCorrection(text, false) {
		s.efficiencyCorrection()
	}
	s.pruneOfferAtPrompt()
	s.thinkingBoost.Store(0)
	s.stall = stallState{}
	if !slices.ContainsFunc(messages, func(message agent.AgentMessage) bool { return message.User != nil }) {
		return messages, nil
	}
	var systems []ai.Message
	for _, message := range append(s.agent.Messages(), messages...) {
		if message.System != nil {
			systems = append(systems, *message.System)
		}
	}
	current := ai.GetCurrentSystemMessage(systems)
	if current != nil && !s.structuredSystemPrompt {
		return messages, nil
	}
	var previous ai.OrderedSections
	if current != nil {
		previous = current.Sections
	}
	patch := prompts.DiffSystemPromptSections(previous, s.baseSystemSections)
	if len(patch) == 0 {
		return messages, nil
	}
	initial := &ai.SystemMessage{Content: ai.SystemText(""), Sections: patch, Timestamp: time.Now().UnixMilli()}
	return append([]agent.AgentMessage{{System: initial}}, messages...), nil
}

func (s *Session) systemPrompt() string {
	if prompt := s.agent.SystemPrompt(); prompt != "" || slices.ContainsFunc(s.agent.Messages(), func(message agent.AgentMessage) bool { return message.System != nil }) {
		return prompt
	}
	return s.baseSystemPrompt
}

func cloneSystemSections(sections ai.OrderedSections) ai.OrderedSections {
	out := slices.Clone(sections)
	for i, section := range out {
		if section.Value != nil {
			out[i].Value = new(*section.Value)
		}
	}
	return out
}

func (s *Session) initSystemPrompt(opts SessionOptions) {
	s.structuredSystemPrompt = opts.SystemPromptSections != nil || opts.SystemPrompt == ""
	switch {
	case opts.SystemPromptSections != nil:
		s.baseSystemSections = cloneSystemSections(opts.SystemPromptSections)
	case opts.SystemPrompt != "":
		s.baseSystemSections = ai.OrderedSections{{Name: "preamble", Value: new(opts.SystemPrompt)}}
	default:
		s.defaultSystemPrompt = true
		names := make([]string, 0, len(s.agent.Tools()))
		for _, tool := range s.agent.Tools() {
			// apply_patch stands in for edit on OpenAI models only; its
			// declaration explains it (see session_patch.go).
			if tool.Name() != tools.ApplyPatchName {
				names = append(names, tool.Name())
			}
		}
		s.baseSystemSections = prompts.BuildSystemPromptSections(prompts.Options{Cwd: s.services.CWD(), Tools: names, ToolHints: s.toolHints(), ToolGuidelines: s.toolGuidelines()})
	}
	s.baseSystemPrompt = ai.GetCurrentSystemPrompt([]ai.Message{ai.SystemMessage{Sections: s.baseSystemSections}})
}

// projectedContext returns the canonical Session projection as the agent's
// loop context, keeping the current instruction baseline.
func (s *Session) projectedContext() []agent.AgentMessage {
	return withInstructionBaseline(s.agent.Messages(), s.inner.BuildSessionProjection().Messages)
}

// prepareRequest re-projects the Session before every provider request, so
// context edits and compactions written during a run reach the next request.
func (s *Session) prepareRequest(ctx context.Context, _ agent.PrepareRequestContext) *agent.AgentRequestUpdate {
	projected := s.pruneRequestContext()
	model, thinking := s.agent.Model(), s.agent.ThinkingLevel()
	if s.routingActive() {
		rs, compacted := s.ensureRoute(ctx, projected)
		if compacted {
			projected = s.pruneRequestContext()
		}
		if rs != nil && rs.model != nil {
			model, thinking = rs.model, rs.thinking
		}
		s.reqStart, s.reqTTFT = time.Now(), 0
	}
	s.efficiencyRecordRequest(projected)
	thinking = s.boostedThinking(model, thinking)
	s.requestModel.Store(model)
	return &agent.AgentRequestUpdate{Context: projected, Model: model, ThinkingLevel: &thinking}
}

// prepareNextTurn compacts before the next assistant response of a run when
// the projected context crosses the threshold, then continues from the
// projection. It runs on the agent goroutine inside
// Send, which holds s.mu.
func (s *Session) prepareNextTurn(ctx context.Context, turn agent.PrepareNextTurnContext) *agent.AgentLoopTurnUpdate {
	s.observeTurnOutcome(turn.ToolResults)
	if model := s.activeModel(); model != nil && model.Capabilities.ContextWindow > 0 {
		projection := s.inner.BuildSessionProjection()
		tokens := compaction.EstimateProjectedContextTokens(projection, s.currentBranch()).Tokens
		settings := s.compactionSettings()
		if compaction.ShouldCompact(tokens, model.Capabilities.ContextWindow, settings) && s.pruneBeforeCompaction() {
			// Pruning may bring the context back under the threshold.
			tokens = compaction.EstimateProjectedContextTokens(s.inner.BuildSessionProjection(), s.currentBranch()).Tokens
		}
		if compaction.ShouldCompact(tokens, model.Capabilities.ContextWindow, settings) {
			s.runAutoCompaction(ctx, "threshold", false)
		} else {
			s.pruneMaybeOffer(tokens, model.Capabilities.ContextWindow)
		}
	}
	model, thinking := s.agent.Model(), s.agent.ThinkingLevel()
	if s.routingActive() {
		if rs := s.route.Load(); rs != nil && rs.model != nil {
			model, thinking = rs.model, rs.thinking
		}
	}
	thinking = s.boostedThinking(model, thinking)
	context := s.projectedContext()
	return &agent.AgentLoopTurnUpdate{Context: context, Messages: s.stallNudge(context), Model: model, ThinkingLevel: &thinking}
}
