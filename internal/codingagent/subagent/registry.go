package subagent

import (
	"context"
	"slices"
	"sync"
	"time"
)

// Agent states in the registry.
const (
	StateRunning     = "running"
	StateDone        = "done"
	StateFailed      = "failed"
	StateCancelled   = "cancelled"
	StateInterrupted = "interrupted"
)

// maxAgentLog bounds the progress lines kept per agent for the live view.
const maxAgentLog = 200

// Spec is a brief as the orchestrator gave it. Running a Spec again
// re-dispatches the same task.
type Spec struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Brief       string   `json:"brief"`
	Effort      string   `json:"effort,omitempty"`
	Paths       []string `json:"paths,omitempty"`
	// Item is the work queue item the task works on, or "".
	Item string `json:"item,omitempty"`
	// Level and AvoidFamily are routing hints for wopr's own briefs: the
	// difficulty level, which skips classification, and a model family
	// the child should not share.
	Level       string `json:"level,omitempty"`
	AvoidFamily string `json:"avoidFamily,omitempty"`
}

// Agent is a snapshot of one task in the registry.
type Agent struct {
	ID         string
	Spec       Spec
	Background bool
	State      string
	Model      string
	ModelSpec  string
	Current    string
	Started    time.Time
	Finished   time.Time
	ToolCalls  int
	Tokens     int
	Cost       float64
	// Transcript is the archive id of the child's transcript, or "".
	Transcript string
	// Result is the rendered, verified result the orchestrator reads.
	Result string
	// Log is the child's tool calls, oldest first.
	Log []string
}

// Running reports whether the agent has not finished.
func (a Agent) Running() bool { return a.State == StateRunning }

type registryEntry struct {
	Agent
	cancel context.CancelFunc
	// stop is the state a cancellation ends in: cancelled when the user
	// stops the task, interrupted when the session closes.
	stop string
}

// Registry tracks the session's subagent tasks, foreground and background.
// Background tasks run on goroutines it owns, detached from the turn that
// started them; Interrupt cancels them and waits for their results.
type Registry struct {
	mu      sync.Mutex
	entries []*registryEntry
	wg      sync.WaitGroup
	// OnChange runs after any change, outside the lock.
	OnChange func()
	// OnStart runs once when a task registers, outside the lock.
	OnStart func(Agent)
	// OnFinish runs once when a task ends, with its final snapshot, outside
	// the lock and before the task leaves the running set.
	OnFinish func(Agent)
	// Now is the clock (tests inject one).
	Now func() time.Time
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Registry) changed() {
	if r.OnChange != nil {
		r.OnChange()
	}
}

// add registers a running task. cancel stops it.
func (r *Registry) add(id string, spec Spec, background bool, route Route, cancel context.CancelFunc) {
	e := &registryEntry{
		ID: id, Spec: spec, Background: background, State: StateRunning,
		Model: modelName(route), ModelSpec: route.Spec, Started: r.now(), cancel: cancel}
	r.mu.Lock()
	r.entries = slices.DeleteFunc(r.entries, func(old *registryEntry) bool { return old.ID == id && old.State != StateRunning })
	r.entries = append(r.entries, e)
	snapshot := e.snapshot()
	r.mu.Unlock()
	if r.OnStart != nil {
		r.OnStart(snapshot)
	}
	r.changed()
}

// progress records a task's live counters.
func (r *Registry) progress(id string, d Details) {
	r.mu.Lock()
	e := r.find(id)
	if e == nil || e.State != StateRunning {
		r.mu.Unlock()
		return
	}
	if d.Current != "" && d.Current != e.Current {
		e.Log = append(e.Log, d.Current)
		if len(e.Log) > maxAgentLog {
			e.Log = e.Log[len(e.Log)-maxAgentLog:]
		}
	}
	e.Current, e.ToolCalls = d.Current, d.ToolCalls
	if d.Model != "" {
		e.Model, e.ModelSpec = d.Model, d.Spec
	}
	if d.Tokens > 0 {
		e.Tokens, e.Cost = d.Tokens, d.Cost
	}
	r.mu.Unlock()
	r.changed()
}

// finish ends a task. A task cancelled by Stop or Interrupt ends in that
// state whatever its child returned.
func (r *Registry) finish(id string, d Details, result string) Agent {
	r.mu.Lock()
	e := r.find(id)
	if e == nil {
		r.mu.Unlock()
		return Agent{}
	}
	switch {
	case e.stop != "":
		e.State = e.stop
	case d.State == StatusFailed || d.State == StatusBlocked:
		e.State = StateFailed
	default:
		e.State = StateDone
	}
	e.Finished = r.now()
	e.Current = ""
	e.ToolCalls, e.Tokens, e.Cost = d.ToolCalls, d.Tokens, d.Cost
	if d.Model != "" {
		e.Model, e.ModelSpec = d.Model, d.Spec
	}
	e.Transcript, e.Result = d.Transcript, result
	e.cancel = nil
	snapshot := e.snapshot()
	r.mu.Unlock()
	if r.OnFinish != nil {
		r.OnFinish(snapshot)
	}
	r.changed()
	return snapshot
}

func (r *Registry) find(id string) *registryEntry {
	for _, e := range r.entries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

func (e *registryEntry) snapshot() Agent {
	a := e.Agent
	a.Log = slices.Clone(e.Log)
	a.Spec.Paths = slices.Clone(e.Spec.Paths)
	return a
}

// List returns every task this session, oldest first.
func (r *Registry) List() []Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Agent, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.snapshot()
	}
	return out
}

// Get returns one task.
func (r *Registry) Get(id string) (Agent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.find(id); e != nil {
		return e.snapshot(), true
	}
	return Agent{}, false
}

// Running returns the tasks still running.
func (r *Registry) Running() []Agent {
	return slices.DeleteFunc(r.List(), func(a Agent) bool { return !a.Running() })
}

// Stop cancels a running task; it ends as cancelled. It reports whether the
// task was running.
func (r *Registry) Stop(id string) bool {
	r.mu.Lock()
	e := r.find(id)
	if e == nil || e.State != StateRunning || e.cancel == nil {
		r.mu.Unlock()
		return false
	}
	e.stop = StateCancelled
	cancel := e.cancel
	r.mu.Unlock()
	cancel()
	return true
}

// Interrupt cancels every running background task, marking it
// interrupted, and waits up to timeout for their goroutines to record their
// partial results. It returns the tasks it interrupted.
func (r *Registry) Interrupt(timeout time.Duration) []Agent {
	var cancels []context.CancelFunc
	var interrupted []string
	r.mu.Lock()
	for _, e := range r.entries {
		if e.State == StateRunning && e.Background && e.cancel != nil {
			e.stop = StateInterrupted
			cancels = append(cancels, e.cancel)
			interrupted = append(interrupted, e.ID)
		}
	}
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
	var out []Agent
	for _, id := range interrupted {
		if a, ok := r.Get(id); ok {
			out = append(out, a)
		}
	}
	return out
}

// Wait blocks until every background task has ended (tests).
func (r *Registry) Wait() { r.wg.Wait() }

// ResultMessageType is the custom message type of a finished background
// task's result delivered to the orchestrator.
const ResultMessageType = "task-result"

// CouncilMessageType is the custom message carrying a war council's
// proposals to the orchestrator.
const CouncilMessageType = "war_council"

// ResultDetails describe a delivered result for the transcript's compact
// block.
type ResultDetails struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	State      string  `json:"state"`
	Model      string  `json:"model"`
	Spec       string  `json:"spec,omitempty"`
	ToolCalls  int     `json:"toolCalls"`
	Tokens     int     `json:"tokens"`
	Cost       float64 `json:"cost"`
	DurationMs int64   `json:"durationMs"`
	Item       string  `json:"item,omitempty"`
}

// Label is the task's short label: its description, else its type.
func (a Agent) Label() string {
	if a.Spec.Description != "" {
		return a.Spec.Description
	}
	return a.Spec.Type
}

// Elapsed is how long the task ran, or has run so far at now.
func (a Agent) Elapsed(now time.Time) time.Duration {
	if !a.Finished.IsZero() {
		return a.Finished.Sub(a.Started)
	}
	return now.Sub(a.Started)
}

// ResultDetails summarizes the task for its delivered result.
func (a Agent) ResultDetails() ResultDetails {
	return ResultDetails{
		ID: a.ID, Label: a.Label(), State: a.State, Model: a.Model, Spec: a.ModelSpec, ToolCalls: a.ToolCalls,
		Tokens: a.Tokens, Cost: a.Cost, DurationMs: a.Elapsed(a.Finished).Milliseconds(), Item: a.Spec.Item,
	}
}
