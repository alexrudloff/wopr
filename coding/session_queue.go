package coding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/queue"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
)

// The work queue: update_plan items persisted as custom session entries,
// updated by the model and by the lifecycle of the tasks linked to them.

// initQueue loads the queue and installs update_plan. It runs after
// initEfficiency, so Online Context Compact sees every completed item, and before
// initSubagents, whose task lifecycle updates linked items.
func (s *Session) initQueue(opts SessionOptions) {
	s.queue = queue.New(queueStore{s: s})
	compact := s.efficiency != nil && s.efficiency.compact != nil
	if !compact && !toolAllowed(opts, "update_plan") {
		return
	}
	tool := &queue.Tool{Queue: s.queue}
	if compact {
		tool.OnComplete = s.efficiency.compact.RecordBoundary
	}
	s.tools = append(s.tools, tool)
	s.agent.SetTools(append(s.agent.Tools(), tool))
}

// Queue returns the session's work queue.
func (s *Session) Queue() *queue.Queue { return s.queue }

// taskStarted links a task to the queue item it works on.
func (s *Session) taskStarted(a subagent.Agent) {
	if a.Spec.Item == "" {
		return
	}
	dispatch, _ := json.Marshal(a.Spec)
	s.queue.Link(a.Spec.Item, a.Label(), a.ID, dispatch)
}

// queueTaskFinished records how a task ended in its queue item and returns
// a note for the orchestrator. An interrupted background task without an
// item gets one, so it can be re-dispatched.
func (s *Session) queueTaskFinished(a subagent.Agent) string {
	status, result := queue.Completed, a.ID
	switch a.State {
	case subagent.StateFailed:
		status = queue.Failed
	case subagent.StateCancelled:
		status, result = queue.Blocked, "stopped by the user"
	case subagent.StateInterrupted:
		status = queue.Interrupted
	}
	if a.Spec.Item != "" {
		if got := s.queue.Finish(a.Spec.Item, a.ID, status, result); got != "" {
			return fmt.Sprintf("Queue item %s is now %s.", a.Spec.Item, got)
		}
		return ""
	}
	if a.Background && a.State == subagent.StateInterrupted {
		dispatch, _ := json.Marshal(a.Spec)
		s.queue.Add(queue.Item{ID: a.ID, Goal: a.Label(), Status: queue.Interrupted, Task: a.ID, Dispatch: dispatch})
	}
	return ""
}

// errNoBackground reports a re-dispatch with no frontend to deliver to.
var errNoBackground = errors.New("background tasks need the interactive UI")

// RedispatchInterrupted runs every interrupted queue item's brief again as
// a background task linked to the item, and returns the started tasks.
func (s *Session) RedispatchInterrupted(ctx context.Context) ([]subagent.Agent, error) {
	if s.tasks.tool == nil {
		return nil, errors.New("the task tool is not installed")
	}
	if s.tasks.deliver.Load() == nil {
		return nil, errNoBackground
	}
	var started []subagent.Agent
	var errs []error
	for _, it := range s.queue.Interrupted() {
		var spec subagent.Spec
		if err := json.Unmarshal(it.Dispatch, &spec); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", it.ID, err))
			continue
		}
		spec.Item = it.ID
		id := subagent.NewTaskID(fmt.Sprintf("%s\x00%d", it.ID, time.Now().UnixNano()))
		a, err := s.tasks.tool.Start(ctx, id, spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", it.ID, err))
			continue
		}
		started = append(started, a)
	}
	return started, errors.Join(errs...)
}

// queueStore persists the queue as custom session entries on the branch.
type queueStore struct{ s *Session }

func (st queueStore) Save(state queue.State) error {
	return st.s.inner.AppendCustomEntry(queue.EntryType, state)
}

func (st queueStore) Load() (queue.State, bool) {
	for _, b := range slices.Backward(st.s.currentBranch()) {
		if b.Base.Type != "custom" {
			continue
		}
		var entry struct {
			CustomType string      `json:"customType"`
			Data       queue.State `json:"data"`
		}
		if json.Unmarshal(b.Raw(), &entry) == nil && entry.CustomType == queue.EntryType {
			// Sessions before the goal stopped being mirrored into the queue
			// carry it as the item "goal"; the goal entry is authoritative.
			entry.Data.Items = slices.DeleteFunc(entry.Data.Items, func(it queue.Item) bool { return it.ID == "goal" })
			return entry.Data, true
		}
	}
	return queue.State{}, false
}
