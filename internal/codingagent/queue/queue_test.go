package queue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type memStore struct {
	saved []State
}

func (m *memStore) Save(s State) error { m.saved = append(m.saved, s); return nil }
func (m *memStore) Load() (State, bool) {
	if len(m.saved) == 0 {
		return State{}, false
	}
	return m.saved[len(m.saved)-1], true
}

// An update names only the items it changes: the others are kept, a task
// link survives, and a new item needs a goal.
func TestUpsertKeepsItemsLeftOut(t *testing.T) {
	q := New(nil)
	if _, err := q.Upsert([]Update{{ID: "a", Goal: "fix login", Status: Pending}, {ID: "b", Goal: "write docs", Status: Pending}}); err != nil {
		t.Fatal(err)
	}
	q.Link("a", "", "task_1", json.RawMessage(`{"brief":"x"}`))
	completed, err := q.Upsert([]Update{{ID: "a", Status: Completed}})
	if err != nil || len(completed) != 1 || completed[0].ID != "a" {
		t.Fatalf("completed %+v err %v", completed, err)
	}
	items := q.Items()
	if len(items) != 2 || items[0].Task != "task_1" || items[0].Goal != "fix login" || items[1].Status != Pending {
		t.Fatalf("items %+v", items)
	}
	if again, _ := q.Upsert([]Update{{ID: "a", Status: Completed}}); len(again) != 0 {
		t.Fatalf("an already completed item completed again: %+v", again)
	}
	if _, err := q.Upsert([]Update{{ID: "c", Status: Pending}}); err == nil {
		t.Fatal("a new item without a goal was accepted")
	}
	if _, err := q.Upsert([]Update{{ID: "a", Status: "done"}}); err == nil {
		t.Fatal("an unknown status was accepted")
	}
}

// A task's end updates only the item still linked to it.
func TestFinishUpdatesTheLinkedItemOnly(t *testing.T) {
	q := New(nil)
	q.Link("a", "investigate", "task_1", nil)
	if items := q.Items(); len(items) != 1 || items[0].Status != InProgress || items[0].Goal != "investigate" {
		t.Fatalf("link added %+v", items)
	}
	q.Link("a", "", "task_2", nil)
	if got := q.Finish("a", "task_1", Completed, "task_1"); got != "" {
		t.Fatalf("a superseded task changed the item to %q", got)
	}
	if got := q.Finish("a", "task_2", Interrupted, "task_2"); got != Interrupted {
		t.Fatalf("Finish = %q", got)
	}
	if it, _ := q.Get("a"); it.Status != Interrupted {
		t.Fatalf("item %+v", it)
	}
}

func TestQueueReloadsFromTheStore(t *testing.T) {
	store := &memStore{}
	q := New(store)
	if _, err := q.Upsert([]Update{{ID: "a", Goal: "g", Status: Blocked}}); err != nil {
		t.Fatal(err)
	}
	restored := New(store)
	if items := restored.Items(); len(items) != 1 || items[0].Status != Blocked {
		t.Fatalf("restored %+v", items)
	}
}

func TestToolShowsOpenItemsAndFoldsCompleted(t *testing.T) {
	tool := &Tool{Queue: New(nil)}
	params, _ := json.Marshal(map[string]any{"steps": []map[string]string{
		{"id": "a", "goal": "fix login", "status": "completed"},
		{"id": "b", "goal": "write docs", "status": "in_progress"},
	}})
	res, err := tool.Execute(context.Background(), "c1", params, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "b · in_progress · write docs") || !strings.Contains(res.Content, "(1 completed: a)") || strings.Contains(res.Content, "fix login") {
		t.Fatalf("snapshot:\n%s", res.Content)
	}
}
