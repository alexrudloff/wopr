package efficiency

import (
	"cmp"
	"strings"
	"sync"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Online Context Compact: completing a work queue item is a candidate point
// for native compaction, subject to the economics in economics.go and window
// pressure. After a successful boundary compaction the session continues the
// task in a new turn with a reminder to bring the queue up to date; the run
// ends gracefully at the boundary instead of being aborted.

const (
	// OnlineStateEntryType is the custom session entry that persists state.
	OnlineStateEntryType = "harness-online-context-state-v1"
	// ContinuationMessageType marks the hidden reminder that restarts work.
	ContinuationMessageType = "harness-online-context-compact"

	DefaultKeepRecentTokens           = 20_000
	DefaultNativeSummaryTokenEstimate = 1_000
	BoundaryCompactionInstructions    = "Preserve completed work, verification results, important decisions, and remaining work."
	PostCompactionPlanReminder        = "Online context compaction finished. The parent task is still active. Before continuing work, call update_plan to bring the work queue up to date."
	correctionPrefix                  = "CORRECTION:"
)

// StateStore persists OnlineState across a session.
type StateStore interface {
	// AppendState records the state as a custom session entry.
	AppendState(state OnlineState) error
	// LoadState returns the newest persisted state, or ok=false.
	LoadState() (OnlineState, bool)
}

// Compactor answers the manager's questions about the live session.
type Compactor interface {
	// ContextTokens estimates the tokens the next request will carry,
	// including the system prompt.
	ContextTokens() int
	// SystemPromptTokens estimates the fixed prompt cost.
	SystemPromptTokens() int
	// ContextWindow is the active model's window, or 0.
	ContextWindow() int
	// NativeCompactionFeasible reports whether native compaction would find
	// anything to summarize right now.
	NativeCompactionFeasible() bool
	// KeepRecentTokens is the native compaction's retained tail.
	KeepRecentTokens() int
	// OpenItems counts the work queue items still open.
	OpenItems() int
}

// CacheDebt is what a boundary compaction owes.
type CacheDebt struct {
	DebtTokens      float64
	RepaymentTokens float64
}

// Manager runs the mechanism for one session.
type Manager struct {
	mu                  sync.Mutex
	state               OnlineState
	store               StateStore
	host                Compactor
	cacheWriteReadRatio float64
	pendingBoundary     string // tool call id of the update_plan that completed an item
	selected            bool
	activeDebt          *CacheDebt
	// Notify reports removed tokens after a boundary compaction.
	Notify func(mechanism, saving string, tokens int)
}

// NewManager restores state from the store.
func NewManager(store StateStore, host Compactor, cacheWriteReadRatio float64) *Manager {
	m := &Manager{store: store, host: host, cacheWriteReadRatio: cacheWriteReadRatio, state: InitialOnlineState()}
	if restored, ok := store.LoadState(); ok && restored.Valid() {
		m.state = restored
	}
	return m
}

// State returns a copy of the current state.
func (m *Manager) State() OnlineState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *Manager) saveLocked() {
	_ = m.store.AppendState(m.state)
}

// RecordProviderRequest counts a request and its context size.
func (m *Manager) RecordProviderRequest(contextTokens int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = m.state.RecordProviderRequest(contextTokens)
	m.saveLocked()
}

// RecordCorrection resets after a steer or an explicit correction.
func (m *Manager) RecordCorrection() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pendingBoundary = ""
	m.selected = false
	m.activeDebt = nil
	m.state = m.state.RecordCorrection()
	m.saveLocked()
}

// IsCorrection reports whether user input counts as a correction.
func IsCorrection(text string, steering bool) bool {
	return steering || strings.HasPrefix(text, correctionPrefix)
}

// RecordCompaction starts a new epoch after any compaction. fromBoundary
// carries the debt of the compaction this manager selected.
func (m *Manager) RecordCompaction(fromBoundary bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	debt := CacheDebt{}
	if fromBoundary && m.activeDebt != nil {
		debt = *m.activeDebt
	}
	m.state = m.state.RecordCompaction(debt.DebtTokens, debt.RepaymentTokens)
	m.saveLocked()
	m.pendingBoundary = ""
	m.selected = false
	m.activeDebt = nil
}

// RecordBoundary records that update_plan call toolCallID completed a work
// item; OnTurnEnd prices the boundary once the turn ends.
func (m *Manager) RecordBoundary(toolCallID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = m.state.RecordBoundary()
	m.pendingBoundary = cmp.Or(m.pendingBoundary, toolCallID)
	m.saveLocked()
}

// OnTurnEnd prices a boundary that completed during the turn. It reports
// whether the run should end so the session can compact.
func (m *Manager) OnTurnEnd(turn agent.AgentTurnContext) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	boundary := m.pendingBoundary
	m.pendingBoundary = ""
	if boundary == "" || m.selected || turn.Message == nil {
		return false
	}
	if turn.Message.StopReason == ai.StopReasonError || turn.Message.StopReason == ai.StopReasonAborted {
		return false
	}
	found := false
	for _, result := range turn.ToolResults {
		if result.ToolCallID == boundary {
			if result.IsError {
				return false
			}
			found = true
		}
	}
	if !found {
		return false
	}
	writeTokens := m.host.ContextTokens()
	fixed := m.host.SystemPromptTokens()
	keep := m.host.KeepRecentTokens()
	if keep <= 0 {
		keep = DefaultKeepRecentTokens
	}
	archive := max(0, writeTokens-fixed-keep)
	remaining := m.host.OpenItems()
	increment := 0.0
	if m.state.PositiveContextDeltaCount > 0 {
		increment = m.state.PositiveContextDeltaTotal / float64(m.state.PositiveContextDeltaCount)
	}
	var counts []int
	if m.state.CompletedBoundaryRequestCounts != nil {
		counts = append([]int{}, m.state.CompletedBoundaryRequestCounts...)
	}
	ratio := m.cacheWriteReadRatio
	if ratio < 0 {
		ratio = -1
	}
	decision := DecideCompaction(CompactionInput{
		WriteTokens:                    writeTokens,
		ArchiveTokens:                  archive,
		MemoTokens:                     DefaultNativeSummaryTokenEstimate,
		ContextTokens:                  writeTokens,
		CompletedBoundaryRequestCounts: counts,
		RemainingBoundaries:            remaining,
		AverageContextTokenIncrement:   increment,
		ContextWindowTokens:            m.host.ContextWindow(),
		PriorCompactionCount:           m.state.NativeCompactionCount,
		CarriedDebtTokens:              m.state.CacheDebtTokens,
		CacheDebtRepaymentTokens:       m.state.CacheDebtRepaymentTokens,
		CacheWriteReadRatio:            ratio,
	})
	if !decision.Compact || !m.host.NativeCompactionFeasible() {
		return false
	}
	m.selected = true
	m.activeDebt = &CacheDebt{
		DebtTokens:      float64(writeTokens) * max(0, decision.IncrementalCacheCostRatio),
		RepaymentTokens: float64(max(0, archive-DefaultNativeSummaryTokenEstimate)),
	}
	return true
}

// TakeSelected reports and clears whether a compaction was chosen at turn end.
func (m *Manager) TakeSelected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	selected := m.selected
	m.selected = false
	return selected
}

// ContinuationMessage is the hidden reminder sent after a boundary
// compaction so the model rebuilds its plan and keeps working.
func ContinuationMessage(timestamp int64) agent.AgentMessage {
	return agent.AgentMessage{Custom: map[string]any{
		"role":       agent.RoleCustom,
		"customType": ContinuationMessageType,
		"content":    PostCompactionPlanReminder,
		"display":    false,
		"timestamp":  timestamp,
	}}
}
