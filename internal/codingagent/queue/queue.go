// Package queue is the session's persistent work queue: what the user asked
// for, what is running where, and what finished. The model keeps it with the
// update_plan tool, and background task lifecycles update linked items. It is
// stored as a custom session entry, so it follows the branch through resume
// and fork.
package queue

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// EntryType is the custom session entry type holding the queue.
const EntryType = "wopr-queue-v1"

// Status is an item's state.
type Status string

// Item statuses. The model sets the first four; wopr sets failed and
// interrupted when a linked task ends that way.
const (
	Pending     Status = "pending"
	InProgress  Status = "in_progress"
	Completed   Status = "completed"
	Blocked     Status = "blocked"
	Failed      Status = "failed"
	Interrupted Status = "interrupted"
)

// ModelStatuses are the statuses the tool schema offers the model.
var ModelStatuses = []Status{Pending, InProgress, Completed, Blocked}

func (s Status) valid() bool {
	switch s {
	case Pending, InProgress, Completed, Blocked, Failed, Interrupted:
		return true
	}
	return false
}

// Open reports whether an item in this status still needs work.
func (s Status) Open() bool { return s != Completed }

// Limits on the queue and its strings.
const (
	maxItems    = 128
	maxIDBytes  = 200
	maxGoalSize = 4000
)

// Item is one unit of work.
type Item struct {
	ID     string `json:"id"`
	Goal   string `json:"goal"`
	Status Status `json:"status"`
	// Task is the subagent task working on the item, or "".
	Task string `json:"task,omitempty"`
	// Result is a short reference to what finished the item: a task id or
	// a commit hash.
	Result string `json:"result,omitempty"`
	// Dispatch is the brief of the item's task, kept to re-run an
	// interrupted one.
	Dispatch json.RawMessage `json:"dispatch,omitempty"`
}

// State is the persisted queue.
type State struct {
	Version int    `json:"version"`
	Items   []Item `json:"items"`
}

// Store persists the queue.
type Store interface {
	// Save records the queue.
	Save(State) error
	// Load returns the newest saved queue, or false.
	Load() (State, bool)
}

// Queue is the session's work queue. It is safe for concurrent use.
type Queue struct {
	mu    sync.Mutex
	items []Item
	store Store
}

// New loads the queue from store (nil keeps it in memory).
func New(store Store) *Queue {
	q := &Queue{store: store}
	q.Reload()
	return q
}

// Reload replaces the queue with the store's newest state, as after a
// session switch.
func (q *Queue) Reload() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.items = nil
	if q.store != nil {
		if state, ok := q.store.Load(); ok && state.Version == 1 {
			q.items = slices.DeleteFunc(state.Items, func(it Item) bool { return it.ID == "" || !it.Status.valid() })
		}
	}
}

// Items returns a copy of the items in order.
func (q *Queue) Items() []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	return slices.Clone(q.items)
}

// Get returns one item.
func (q *Queue) Get(id string) (Item, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if i := q.index(id); i >= 0 {
		return q.items[i], true
	}
	return Item{}, false
}

func (q *Queue) index(id string) int {
	return slices.IndexFunc(q.items, func(it Item) bool { return it.ID == id })
}

// saveLocked persists the queue; q.mu is held.
func (q *Queue) saveLocked() {
	if len(q.items) > maxItems {
		// Drop the oldest finished items first.
		for i := 0; i < len(q.items) && len(q.items) > maxItems; {
			if q.items[i].Status == Completed {
				q.items = slices.Delete(q.items, i, i+1)
				continue
			}
			i++
		}
		if len(q.items) > maxItems {
			q.items = q.items[len(q.items)-maxItems:]
		}
	}
	if q.store != nil {
		_ = q.store.Save(State{Version: 1, Items: slices.Clone(q.items)})
	}
}

// Update is one item in an update_plan call: an id, and the goal and
// status to set (empty keeps the current value).
type Update struct {
	ID     string `json:"id"`
	Goal   string `json:"goal"`
	Status Status `json:"status"`
}

// Upsert applies the model's updates: new ids are appended, known ones
// change goal and status, and items left out are kept. A task link and a
// result survive the update. It returns the items that were open before and
// are completed now.
func (q *Queue) Upsert(updates []Update) (completed []Item, err error) {
	seen := map[string]bool{}
	for _, u := range updates {
		switch {
		case u.ID == "" || len(u.ID) > maxIDBytes:
			return nil, fmt.Errorf("item id must be 1-%d bytes", maxIDBytes)
		case seen[u.ID]:
			return nil, fmt.Errorf("item %q appears twice", u.ID)
		case u.Status != "" && !u.Status.valid():
			return nil, fmt.Errorf("item %q has unknown status %q", u.ID, u.Status)
		case len(u.Goal) > maxGoalSize:
			return nil, fmt.Errorf("item %q goal is longer than %d bytes", u.ID, maxGoalSize)
		}
		seen[u.ID] = true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, u := range updates {
		if u.Goal == "" && q.index(u.ID) < 0 {
			return nil, fmt.Errorf("new item %q needs a goal", u.ID)
		}
	}
	for _, u := range updates {
		i := q.index(u.ID)
		if i < 0 {
			status := cmp.Or(u.Status, Pending)
			q.items = append(q.items, Item{ID: u.ID, Goal: strings.TrimSpace(u.Goal), Status: status})
			if status == Completed {
				completed = append(completed, q.items[len(q.items)-1])
			}
			continue
		}
		it := &q.items[i]
		if u.Goal != "" {
			it.Goal = strings.TrimSpace(u.Goal)
		}
		if u.Status != "" {
			wasOpen := it.Status.Open()
			it.Status = u.Status
			if wasOpen && u.Status == Completed {
				completed = append(completed, *it)
			}
		}
	}
	q.saveLocked()
	return completed, nil
}

// Link marks item id in progress on task, adding it with goal when it is
// new. dispatch is the task's brief, kept to re-run it if interrupted.
func (q *Queue) Link(id, goal, task string, dispatch json.RawMessage) {
	if id == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.index(id)
	if i < 0 {
		q.items = append(q.items, Item{ID: id, Goal: goal})
		i = len(q.items) - 1
	}
	it := &q.items[i]
	it.Status, it.Task, it.Result, it.Dispatch = InProgress, task, "", dispatch
	q.saveLocked()
}

// Finish records how the task linked to item id ended, unless the item has
// since moved to another task. It returns the item's new status, or "" when
// nothing changed.
func (q *Queue) Finish(id, task string, status Status, result string) Status {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.index(id)
	if i < 0 || q.items[i].Task != task {
		return ""
	}
	it := &q.items[i]
	it.Status, it.Result = status, result
	if status != Interrupted {
		it.Dispatch = nil
	}
	q.saveLocked()
	return status
}

// Add appends an item, replacing one with the same id.
func (q *Queue) Add(item Item) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if i := q.index(item.ID); i >= 0 {
		q.items[i] = item
	} else {
		q.items = append(q.items, item)
	}
	q.saveLocked()
}

// Interrupted returns the items whose task was interrupted and can be
// re-dispatched.
func (q *Queue) Interrupted() []Item {
	return slices.DeleteFunc(q.Items(), func(it Item) bool { return it.Status != Interrupted || len(it.Dispatch) == 0 })
}

// Format is the queue as the model reads it: open items one per line, and
// the completed ones as a count with their ids.
func Format(items []Item) string {
	var b strings.Builder
	b.WriteString("<queue>\n")
	var done []string
	for _, it := range items {
		if !it.Status.Open() {
			done = append(done, it.ID)
			continue
		}
		fmt.Fprintf(&b, "%s · %s · %s", it.ID, it.Status, strings.Join(strings.Fields(it.Goal), " "))
		if it.Task != "" {
			b.WriteString(" · " + it.Task)
		}
		b.WriteString("\n")
	}
	if len(done) > 0 {
		fmt.Fprintf(&b, "(%d completed: %s)\n", len(done), strings.Join(done, ", "))
	}
	if len(items) == 0 {
		b.WriteString("(empty)\n")
	}
	b.WriteString("</queue>")
	return b.String()
}
