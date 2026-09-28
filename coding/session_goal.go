package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/goal"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
)

// The session goal: /goal sets an objective, and after every settled run
// the frontend asks GoalNext whether to start an automatic turn. A reply
// ending in a GOAL MET line is audited by a read-only subagent before the
// goal counts as done; caps pause the loop. The goal is a custom session
// entry.

// sessionGoal is the goal and its bookkeeping. Fields are guarded by mu.
type sessionGoal struct {
	mu    sync.Mutex
	state goal.State
	has   bool
	// seenCost and seenTools are the session's cost and tool-call totals at
	// the last accounting; spend and progress are deltas from them.
	seenCost  float64
	seenTools int
	// activeSince starts the running time of an active goal.
	activeSince time.Time
	// automatic reports that the last message the frontend sent was the
	// goal's, so the next settle closes an automatic turn.
	automatic bool
	// busy guards GoalNext against a concurrent call.
	busy    bool
	changed atomic.Pointer[func()]
}

func (s *Session) initGoal() {
	s.goal = &sessionGoal{}
	s.reloadGoal(true)
}

// Goal returns the session goal, if one was set on this branch.
func (s *Session) Goal() (goal.State, bool) {
	g := s.goal
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.state
	st.Tasks = slices.Clone(st.Tasks)
	return st, g.has
}

// OnGoalChange sets the callback run after any goal change.
func (s *Session) OnGoalChange(fn func()) {
	if fn == nil {
		s.goal.changed.Store(nil)
		return
	}
	s.goal.changed.Store(&fn)
}

// SetGoal replaces the goal with an active one and returns the message
// that starts work on it.
func (s *Session) SetGoal(objective string, caps goal.Caps) (agent.AgentMessage, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return agent.AgentMessage{}, errors.New("goal needs an objective")
	}
	now := time.Now()
	g := s.goal
	g.mu.Lock()
	g.state = goal.New(subagent.NewTaskID(fmt.Sprintf("goal\x00%s\x00%d", objective, now.UnixNano())), objective, caps, now)
	g.has = true
	g.activeSince = now
	g.markLocked(s)
	g.automatic = true
	st := g.state
	g.mu.Unlock()
	s.goalChanged(st)
	return goalMessage(st, "kickoff", st.Kickoff()), nil
}

// StopGoal ends the goal; it reports whether an open goal was stopped.
func (s *Session) StopGoal() bool {
	return s.updateGoal(func(st *goal.State) bool {
		if !st.Open() {
			return false
		}
		st.Status, st.Reason = goal.Stopped, "stopped by the user"
		return true
	})
}

// PauseGoal pauses an active goal with a reason, as when the user
// interrupts its turn.
func (s *Session) PauseGoal(reason string) bool {
	return s.updateGoal(func(st *goal.State) bool {
		if st.Status != goal.Active {
			return false
		}
		st.Status, st.Reason = goal.Paused, reason
		return true
	})
}

// ResumeGoal reactivates an open goal with a fresh window of caps and
// returns the message of its next turn.
func (s *Session) ResumeGoal() (agent.AgentMessage, error) {
	g := s.goal
	g.mu.Lock()
	if !g.has || !g.state.Open() {
		g.mu.Unlock()
		return agent.AgentMessage{}, errors.New("no open goal to resume")
	}
	g.accountLocked(s, time.Now())
	st := &g.state
	st.Status, st.Reason = goal.Active, ""
	st.Turns, st.Spent, st.Elapsed, st.Idle = 1, 0, 0, 0
	g.activeSince = time.Now()
	g.automatic = true
	snapshot := g.state
	g.mu.Unlock()
	s.goalChanged(snapshot)
	return goalMessage(snapshot, "continue", snapshot.Continuation()), nil
}

// GoalNext decides what follows a settled run: an audit of a completion
// claim, a pause at a cap or a failed turn, a wait for the goal's
// background tasks, or the next automatic turn. It returns a zero step
// when no goal is active. An audit runs on the caller's goroutine.
func (s *Session) GoalNext(ctx context.Context) goal.Step {
	g := s.goal
	g.mu.Lock()
	if !g.has || g.state.Status != goal.Active || g.busy {
		g.mu.Unlock()
		return goal.Step{}
	}
	g.busy = true
	g.accountLocked(s, time.Now())
	st := g.state
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.busy = false
		g.mu.Unlock()
	}()

	pause := func(reason string) goal.Step {
		s.PauseGoal(reason)
		return goal.Step{Event: "paused", Text: reason}
	}
	if last := lastAssistantMessage(s.agent.Messages()); last != nil && (last.Timestamp == 0 || last.Timestamp >= st.Started.UnixMilli()) {
		switch last.StopReason {
		case ai.StopReasonAborted:
			return pause("interrupted")
		case ai.StopReasonError:
			return pause("the last turn failed")
		}
		claim := goal.ParseClaim(assistantReplyText(last.Content))
		switch claim.Kind {
		case goal.BlockedMarker:
			return pause("blocked: " + firstLine(claim.Evidence))
		case goal.MetMarker:
			if key := subagent.NewTaskID(fmt.Sprintf("%d\x00%s", last.Timestamp, claim.Evidence)); key != st.Audited {
				if step, done := s.auditClaim(ctx, st, key, claim.Evidence); done {
					return step
				}
			}
		}
	}
	g.mu.Lock()
	st = g.state
	g.mu.Unlock()
	if st.Status != goal.Active {
		return goal.Step{}
	}
	if hit := st.CapHit(); hit != "" {
		return pause(hit)
	}
	if registry := s.Agents(); registry != nil && slices.ContainsFunc(registry.Running(), func(a subagent.Agent) bool {
		return slices.Contains(st.Tasks, a.ID)
	}) {
		return goal.Step{Wait: true}
	}
	var msg agent.AgentMessage
	s.updateGoal(func(st *goal.State) bool {
		st.Turns++
		msg = goalMessage(*st, "continue", st.Continuation())
		return true
	})
	g.mu.Lock()
	g.automatic = true
	g.mu.Unlock()
	return goal.Step{Message: &msg}
}

// auditClaim runs the completion audit. It returns done when the goal
// ended or paused; a failed audit leaves feedback for the next turn.
func (s *Session) auditClaim(ctx context.Context, st goal.State, key, claim string) (goal.Step, bool) {
	s.updateGoal(func(st *goal.State) bool { st.Audited, st.Audits = key, st.Audits+1; return true })
	if s.tasks.tool == nil {
		s.PauseGoal("no auditor available")
		return goal.Step{Event: "paused", Text: "no auditor available"}, true
	}
	family := ""
	if m := s.requestModel.Load(); m != nil {
		family = router.ModelFamily(m.ID)
	} else if m := s.activeModel(); m != nil {
		family = router.ModelFamily(m.ID)
	}
	spec := subagent.Spec{Type: subagent.TypeExplore, Description: "goal audit", Brief: st.AuditBrief(claim), Effort: subagent.EffortMedium, Level: "routine", AvoidFamily: family}
	details, text, err := s.tasks.tool.Run(ctx, subagent.NewTaskID(fmt.Sprintf("goal-audit\x00%s\x00%d", st.ID, st.Audits+1)), spec, nil)
	if err != nil {
		if ctx.Err() != nil {
			s.PauseGoal("audit interrupted")
			return goal.Step{Event: "paused", Text: "audit interrupted"}, true
		}
		s.PauseGoal("audit failed to run: " + err.Error())
		return goal.Step{Event: "paused", Text: "audit failed to run: " + err.Error()}, true
	}
	pass, feedback := goal.Verdict(details.State == subagent.StatusDone, subagent.ParseResult(text).Answer)
	// The audit judges the orchestrator's work: a signal for its model.
	if m := s.activeModel(); m != nil {
		o := router.OutcomeCounts{GoalFail: 1}
		if pass {
			o = router.OutcomeCounts{GoalPass: 1}
		}
		router.RecordOutcome(s.services.AgentDir(), providerID(m)+"/"+m.ID, o)
	}
	if pass {
		s.updateGoal(func(st *goal.State) bool {
			st.Status, st.Reason, st.Feedback = goal.Done, "audit passed", ""
			return true
		})
		return goal.Step{Event: "done", Text: feedback}, true
	}
	s.updateGoal(func(st *goal.State) bool { st.Feedback = feedback; return true })
	return goal.Step{}, false
}

// goalTaskStarted links a background task started while the goal is
// active, so automatic turns wait for its result.
func (s *Session) goalTaskStarted(a subagent.Agent) {
	if !a.Background {
		return
	}
	s.updateGoal(func(st *goal.State) bool {
		if st.Status != goal.Active {
			return false
		}
		st.Tasks = append(st.Tasks, a.ID)
		if len(st.Tasks) > 64 {
			st.Tasks = st.Tasks[len(st.Tasks)-64:]
		}
		return true
	})
}

// goalTaskFinished charges a task that ran while the goal was active to
// its spend: a subagent's usage is not in the session's own accounting.
func (s *Session) goalTaskFinished(a subagent.Agent) {
	if a.Cost <= 0 {
		return
	}
	s.updateGoal(func(st *goal.State) bool {
		if st.Status != goal.Active && !slices.Contains(st.Tasks, a.ID) {
			return false
		}
		st.Spent += a.Cost
		st.TotalSpent += a.Cost
		return true
	})
}

// updateGoal applies fn to the goal and, when it reports a change,
// persists it and notifies.
func (s *Session) updateGoal(fn func(*goal.State) bool) bool {
	g := s.goal
	g.mu.Lock()
	if !g.has || !fn(&g.state) {
		g.mu.Unlock()
		return false
	}
	if g.state.Status == goal.Active {
		if g.activeSince.IsZero() {
			g.activeSince = time.Now()
		}
	} else {
		g.accountLocked(s, time.Now())
		g.activeSince = time.Time{}
	}
	st := g.state
	g.mu.Unlock()
	s.goalChanged(st)
	return true
}

// accountLocked adds the spend and time since the last accounting and
// closes an automatic turn: one without a tool call counts as idle.
func (g *sessionGoal) accountLocked(s *Session, now time.Time) {
	var cost float64
	var toolCalls int
	if s.inner != nil {
		acc := s.inner.Accounting()
		cost, toolCalls = acc.Tokens.Cost, acc.ToolCalls
	}
	if d := cost - g.seenCost; d > 0 {
		g.state.Spent += d
		g.state.TotalSpent += d
	}
	if g.automatic {
		if toolCalls > g.seenTools {
			g.state.Idle = 0
		} else {
			g.state.Idle++
		}
		g.automatic = false
	}
	g.seenCost, g.seenTools = cost, toolCalls
	if !g.activeSince.IsZero() {
		g.state.Elapsed += now.Sub(g.activeSince)
		g.activeSince = now
	}
}

// markLocked starts accounting from the session's current totals.
func (g *sessionGoal) markLocked(s *Session) {
	g.seenCost, g.seenTools = 0, 0
	if s.inner != nil {
		acc := s.inner.Accounting()
		g.seenCost, g.seenTools = acc.Tokens.Cost, acc.ToolCalls
	}
}

// goalChanged persists st and notifies.
func (s *Session) goalChanged(st goal.State) {
	if s.inner != nil {
		_ = s.inner.AppendCustomEntry(goal.EntryType, st)
	}
	if fn := s.goal.changed.Load(); fn != nil {
		(*fn)()
	}
}

// reloadGoal loads the branch's goal. A goal other than the one in memory
// (a new process, another session or branch) comes back paused when it
// was active, so reopening a session never spends on its own.
func (s *Session) reloadGoal(fresh bool) {
	st, ok := loadGoal(s)
	g := s.goal
	g.mu.Lock()
	if ok && !fresh && g.has && g.state.ID == st.ID {
		g.mu.Unlock()
		return
	}
	g.state, g.has = st, ok
	g.automatic, g.activeSince = false, time.Time{}
	g.markLocked(s)
	pause := ok && st.Status == goal.Active
	g.mu.Unlock()
	if pause {
		s.PauseGoal("session reopened")
	}
}

func loadGoal(s *Session) (goal.State, bool) {
	for _, b := range slices.Backward(s.currentBranch()) {
		if b.Base.Type != "custom" {
			continue
		}
		var entry struct {
			CustomType string     `json:"customType"`
			Data       goal.State `json:"data"`
		}
		if json.Unmarshal(b.Raw(), &entry) == nil && entry.CustomType == goal.EntryType {
			return entry.Data, entry.Data.Version == 1 && entry.Data.Objective != ""
		}
	}
	return goal.State{}, false
}

// goalMessage is a goal message as the orchestrator reads it.
func goalMessage(st goal.State, kind, content string) agent.AgentMessage {
	return agent.AgentMessage{Custom: map[string]any{
		"role":       agent.RoleCustom,
		"customType": goal.MessageType,
		"content":    content,
		"display":    true,
		"details":    map[string]any{"kind": kind, "turn": st.Turns, "turns": st.Caps.Turns, "objective": st.Objective},
		"timestamp":  time.Now().UnixMilli(),
	}}
}

func assistantReplyText(content []ai.AssistantContentBlock) string {
	var b strings.Builder
	for _, block := range content {
		if text, ok := block.(ai.TextContent); ok {
			b.WriteString(text.Text)
		}
	}
	return b.String()
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
