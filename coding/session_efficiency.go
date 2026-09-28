package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
)

// Efficiency mechanisms wired into the Session (see internal/codingagent/efficiency).

type sessionEfficiency struct {
	cfg       efficiency.Config
	loadError string
	root      string
	pack      *efficiency.Pack
	reducer   *efficiency.Reducer
	compact   *efficiency.Manager
	// boundaryReason marks the compaction this session selected at a plan
	// boundary so RecordCompaction can carry its cache debt.
	boundaryReason string
}

// initEfficiency loads efficiency.json and installs the enabled mechanisms. It runs
// after the router so the reducer can use the cheapest tier.
func (s *Session) initEfficiency() {
	cfg, err := efficiency.Load(s.services.AgentDir())
	state := &sessionEfficiency{cfg: cfg, boundaryReason: "boundary"}
	if err != nil {
		state.loadError = err.Error()
		state.cfg = efficiency.DefaultConfig()
	}
	s.efficiency = state
	if !state.cfg.Enabled() {
		return
	}
	if s.inner != nil {
		state.root = efficiency.RuntimeRoot(s.inner.Path(), s.inner.ID())
	}
	notify := s.emitSavings
	var extra []agent.AgentTool

	if state.cfg.ActionFusion {
		if bash := s.toolNamed("bash"); bash != nil {
			for i, tool := range s.tools {
				if name := tool.Name(); name == "edit" || name == "write" {
					fused := efficiency.Fuse(tool, bash, s.services.CWD())
					fused.Notify = notify
					s.tools[i] = fused
				}
			}
		}
	}
	if state.cfg.ObservationPack && state.root != "" {
		state.pack = efficiency.NewPack(state.root)
		state.pack.Notify = notify
		recall := efficiency.NewRecallTool(state.pack)
		recall.Notify = notify
		extra = append(extra, recall)
	}
	if state.cfg.EvidencePreservingReducer && state.root != "" {
		state.reducer = efficiency.NewReducer(state.root, s.reducerCompleter())
		state.reducer.Notify = notify
		s.agent.AddAfterToolCallHook(s.efficiencyAfterToolCall)
	}
	if state.cfg.OnlineContextCompact {
		state.compact = efficiency.NewManager(efficiencyStore{s: s}, efficiencyCompactor{s: s}, state.cfg.CacheWriteReadRatio)
		state.compact.Notify = notify
		s.agent.SetFinishTurn(s.efficiencyFinishTurn)
	}
	if len(extra) > 0 {
		s.tools = append(s.tools, extra...)
	}
	if state.cfg.ActionFusion || len(extra) > 0 {
		s.agent.SetTools(s.tools)
	}
}

func (s *Session) toolNamed(name string) agent.AgentTool {
	for _, tool := range s.tools {
		if tool.Name() == name {
			return tool
		}
	}
	return nil
}

// EfficiencyStatus reports the mechanism configuration for /harness.
func (s *Session) EfficiencyStatus() string {
	if s.efficiency == nil {
		return "Efficiency mechanisms: not initialised"
	}
	st := s.efficiency
	onOff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	var b strings.Builder
	if st.loadError != "" {
		fmt.Fprintf(&b, "**Config error:** %s\n\n", st.loadError)
	}
	fmt.Fprintf(&b, "- Action Fusion: %s\n- Observation Pack: %s\n- Evidence-Preserving Reducer: %s\n- Online Context Compact: %s (cache write/read ratio %.1f)\n",
		onOff(st.cfg.ActionFusion), onOff(st.cfg.ObservationPack), onOff(st.cfg.EvidencePreservingReducer), onOff(st.cfg.OnlineContextCompact), st.cfg.CacheWriteReadRatio)
	fmt.Fprintf(&b, "- Quota balance: %s\n", onOff(st.cfg.QuotaBalance))
	if st.root != "" {
		fmt.Fprintf(&b, "- archives: %s\n", st.root)
	} else if st.cfg.ObservationPack || st.cfg.EvidencePreservingReducer {
		b.WriteString("- archives: unavailable (session is not persisted), so Observation Pack and the reducer are inactive\n")
	}
	if st.compact != nil {
		state := st.compact.State()
		fmt.Fprintf(&b, "- queue: %d open items, epoch %d, %d requests, %d compactions, cache debt %.0f tokens\n", s.efficiencyCompactor().OpenItems(), state.Epoch, state.RequestCount, state.NativeCompactionCount, state.CacheDebtTokens)
	}
	return b.String()
}

// emitSavings reports a mechanism's saving on the event stream.
func (s *Session) emitSavings(mechanism, saving string, tokens int) {
	s.emitOrderedEvent(agent.SavingsEvent{Mechanism: mechanism, Saving: saving, Tokens: tokens})
}

// efficiencyProject applies ObservationPack to a request context.
func (s *Session) efficiencyProject(messages []agent.AgentMessage) []agent.AgentMessage {
	if s.efficiency == nil || s.efficiency.pack == nil {
		return messages
	}
	messages = s.efficiency.pack.Project(messages)
	if s.efficiency.cfg.ToolOutputHalfLife {
		if model := s.activeModel(); model != nil {
			messages = s.efficiency.pack.ProjectHalfLife(messages, efficiency.HalfLifeKeep(model.Capabilities.ContextWindow))
		}
	}
	return messages
}

// efficiencyRecordRequest counts a provider request for Online Context Compact.
func (s *Session) efficiencyRecordRequest(messages []agent.AgentMessage) {
	if s.efficiency == nil || s.efficiency.compact == nil {
		return
	}
	s.efficiency.compact.RecordProviderRequest(compaction.EstimateMessagesTokens(messages) + ai.EstimateTextTokens(s.systemPrompt()))
}

// efficiencyCorrection resets the plan state after the user steers.
func (s *Session) efficiencyCorrection() {
	if s.efficiency != nil && s.efficiency.compact != nil {
		s.efficiency.compact.RecordCorrection()
	}
}

// recordCompaction tells the router that the orchestrator's cache is cold
// and Online Context Compact that a compaction happened; reason "boundary"
// carries cache debt.
func (s *Session) recordCompaction(reason string) {
	if s.router != nil {
		s.router.MarkCold()
	}
	if s.efficiency != nil && s.efficiency.compact != nil {
		s.efficiency.compact.RecordCompaction(reason == s.efficiency.boundaryReason)
	}
	s.pruneCold()
}

// efficiencyAfterToolCall runs the Evidence-Preserving Reducer on tool results.
func (s *Session) efficiencyAfterToolCall(ctx context.Context, toolCallID, toolName string, args json.RawMessage, result agent.AgentToolResult) agent.AfterToolCallResult {
	if s.efficiency == nil || s.efficiency.reducer == nil {
		return agent.AfterToolCallResult{}
	}
	reduced := s.efficiency.reducer.Reduce(ctx, toolCallID, toolName, args, result)
	if reduced == nil {
		return agent.AfterToolCallResult{}
	}
	content := reduced.Content
	return agent.AfterToolCallResult{Content: &content, Details: reduced.Details}
}

// reducerCompleter routes the reducer request to a configured model or to
// the router's cheapest tier that fits, with thinking off.
func (s *Session) reducerCompleter() efficiency.ReducerCompleter {
	return func(ctx context.Context, system, user string, maxTokens int, timeout time.Duration) (efficiency.ReducerResponse, error) {
		var model *ai.Model
		cfg := s.efficiency.cfg
		if cfg.ReducerProvider != "" {
			built, err := s.routeModel(cfg.ReducerProvider, cfg.ReducerModel)
			if err != nil {
				return efficiency.ReducerResponse{}, fmt.Errorf("%w: %w", efficiency.ErrReducerUnavailable, err)
			}
			model = built
		} else if s.subagentRoutingActive() {
			ref, _, ok := s.router.SideTask(ai.EstimateTextTokens(system+user) + maxTokens)
			if !ok {
				return efficiency.ReducerResponse{}, efficiency.ErrReducerUnavailable
			}
			built, err := s.routeModel(ref.Provider, ref.Model)
			if err != nil {
				return efficiency.ReducerResponse{}, fmt.Errorf("%w: %w", efficiency.ErrReducerUnavailable, err)
			}
			model = built
		}
		if model == nil || s.objectiveGate(model) != nil {
			return efficiency.ReducerResponse{}, efficiency.ErrReducerUnavailable
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		request := ai.Context{
			SystemPrompt: system,
			Messages:     []ai.Message{ai.UserMessage{Content: ai.UserContentBlocks{ai.TextContent{Text: user}}, Timestamp: time.Now().UnixMilli()}},
		}
		limit := maxTokens
		if model.Capabilities.MaxOutputTokens > 0 && model.Capabilities.MaxOutputTokens < limit {
			limit = model.Capabilities.MaxOutputTokens
		}
		// IsReasoning makes the provider send its explicit thinking-off
		// parameter; without it some local servers think by default and the
		// output budget goes to reasoning instead of the receipt.
		options := ai.StreamOptions{MaxTokens: limit, Thinking: ai.ThinkingOff, IsReasoning: model.Capabilities.MaxThinking != "", SessionID: s.ID() + ":reducer", TimeoutMs: int(timeout / time.Millisecond)}
		message := s.modelRuntime.Complete(callCtx, model, request, options)
		if message == nil {
			return efficiency.ReducerResponse{}, errors.New("efficiency: reducer returned no message")
		}
		var text strings.Builder
		for _, block := range message.Content {
			if t, ok := block.(ai.TextContent); ok {
				text.WriteString(t.Text)
			}
		}
		if callCtx.Err() != nil && message.StopReason != ai.StopReasonStop {
			return efficiency.ReducerResponse{}, callCtx.Err()
		}
		return efficiency.ReducerResponse{
			Provider:    message.Provider,
			Model:       message.Model,
			Text:        text.String(),
			OK:          message.StopReason == ai.StopReasonStop || message.StopReason == ai.StopReasonLength,
			StopReason:  string(message.StopReason),
			Error:       message.ErrorMessage,
			TotalTokens: message.Usage.TotalTokens,
		}, nil
	}
}

// efficiencyFinishTurn ends the run at a plan boundary the economics selected,
// so the session can compact and continue on the smaller context.
func (s *Session) efficiencyFinishTurn(_ context.Context, turn agent.AgentTurnContext) *agent.AgentTurnDecision {
	if s.efficiency == nil || s.efficiency.compact == nil {
		return nil
	}
	if s.efficiency.compact.OnTurnEnd(turn) {
		return &agent.AgentTurnDecision{Action: agent.AgentTurnEnd}
	}
	return nil
}

// efficiencyBoundaryCompaction runs the compaction selected at a plan boundary and
// queues the hidden reminder that continues the task. It reports whether the
// run should continue.
func (s *Session) efficiencyBoundaryCompaction(ctx context.Context) bool {
	if s.efficiency == nil || s.efficiency.compact == nil {
		return false
	}
	if !s.efficiency.compact.TakeSelected() {
		return false
	}
	before := s.efficiencyCompactor().ContextTokens()
	if !s.runAutoCompactionWith(ctx, s.efficiency.boundaryReason, false, efficiency.BoundaryCompactionInstructions) {
		return false
	}
	after := s.efficiencyCompactor().ContextTokens()
	if removed := before - after; removed > 0 {
		s.emitSavings("Online Context Compact", fmt.Sprintf("%d context tokens removed", removed), removed)
	}
	s.agent.FollowUp(efficiency.ContinuationMessage(time.Now().UnixMilli()))
	return true
}

func (s *Session) efficiencyCompactor() efficiencyCompactor { return efficiencyCompactor{s: s} }

// efficiencyCompactor adapts the Session to efficiency.Compactor.
type efficiencyCompactor struct{ s *Session }

func (c efficiencyCompactor) ContextTokens() int {
	tokens := 0
	if c.s.inner != nil {
		tokens = compaction.EstimateProjectedContextTokens(c.s.inner.BuildSessionProjection(), c.s.currentBranch()).Tokens
	}
	if reported := c.s.ContextUsage(); reported != nil && reported.Tokens != nil && *reported.Tokens > tokens {
		tokens = *reported.Tokens
	}
	return tokens + c.SystemPromptTokens()
}

func (c efficiencyCompactor) SystemPromptTokens() int {
	return ai.EstimateTextTokens(c.s.systemPrompt())
}

func (c efficiencyCompactor) ContextWindow() int {
	if model := c.s.activeModel(); model != nil {
		return model.Capabilities.ContextWindow
	}
	return 0
}

func (c efficiencyCompactor) OpenItems() int {
	if c.s.queue == nil {
		return 0
	}
	open := 0
	for _, it := range c.s.queue.Items() {
		if it.Status.Open() {
			open++
		}
	}
	return open
}

func (c efficiencyCompactor) NativeCompactionFeasible() bool {
	return compaction.PrepareCompaction(c.s.currentBranch(), c.s.compactionSettings()) != nil
}

func (c efficiencyCompactor) KeepRecentTokens() int { return c.s.compactionSettings().KeepRecentTokens }

// efficiencyStore persists Online Context Compact state as custom entries.
type efficiencyStore struct{ s *Session }

func (st efficiencyStore) AppendState(state efficiency.OnlineState) error {
	return st.s.inner.AppendCustomEntry(efficiency.OnlineStateEntryType, state)
}

func (st efficiencyStore) LoadState() (efficiency.OnlineState, bool) {
	branch := st.s.currentBranch()
	for _, b := range slices.Backward(branch) {
		if b.Base.Type != "custom" {
			continue
		}
		var entry struct {
			CustomType string          `json:"customType"`
			Data       json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(b.Raw(), &entry); err != nil || entry.CustomType != efficiency.OnlineStateEntryType {
			continue
		}
		var state efficiency.OnlineState
		if err := json.Unmarshal(entry.Data, &state); err != nil || !state.Valid() {
			continue
		}
		return state, true
	}
	return efficiency.OnlineState{}, false
}
