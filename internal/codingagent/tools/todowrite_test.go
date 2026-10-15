package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestTodoWriteCarriesItemsAndCounts covers the three ways this tool can be
// wrong in a way only a model and a client would notice: a write that is not
// reported as an error, an error that does not name the offending value, and an
// item the client cannot render.
func TestTodoWriteCarriesItemsAndCounts(t *testing.T) {
	tool := &TodoTool{}

	items := []Todo{
		{ID: "a", Content: "read the spec", Status: "completed", Priority: "high"},
		{ID: "b", Content: "write the code", Status: "in_progress", Priority: "medium"},
		{ID: "c", Content: "cut the changelog", Status: "pending", Priority: "low"},
		{ID: "d", Content: "drop the idea", Status: "cancelled", Priority: "low"},
	}
	raw, err := json.Marshal(map[string]any{"todos": items})
	if err != nil {
		t.Fatal(err)
	}

	got, err := tool.Execute(context.Background(), "call-1", raw, nil)
	if err != nil {
		t.Fatalf("Execute: unexpected error: %v", err)
	}
	if got.IsError {
		t.Fatalf("Execute: expected a successful write, got an error result: %q", got.Content)
	}

	// Details is what the client renders; it must survive as the items
	// themselves, not as a wrapped or stringified value.
	encoded, err := json.Marshal(got.Details)
	if err != nil {
		t.Fatalf("marshal Details: %v", err)
	}
	var roundTripped []Todo
	if err := json.Unmarshal(encoded, &roundTripped); err != nil {
		t.Fatalf("Details is not a todo list: %v (%s)", err, encoded)
	}
	if len(roundTripped) != len(items) {
		t.Fatalf("Details: got %d items, want %d (%s)", len(roundTripped), len(items), encoded)
	}
	for i, want := range items {
		if roundTripped[i] != want {
			t.Errorf("Details[%d]: got %+v, want %+v", i, roundTripped[i], want)
		}
	}

	// Content is what the model reads back: every status present, counted.
	for _, want := range []string{"4 items", "1 completed", "1 in_progress", "1 pending", "1 cancelled"} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("Content %q does not report %q", got.Content, want)
		}
	}

	// A list the caller cannot render is refused, and the error names the value
	// so the model can correct it in one turn.
	if _, err := Parse([]byte(`{"todos":[{"id":"a","content":"x","status":"done","priority":"high"}]}`)); err == nil {
		t.Error(`Parse: status "done" was accepted, want a rejection`)
	}
	if _, err := Parse([]byte(`{"todos":[{"id":"a","content":"x","status":"pending","priority":"urgent"}]}`)); err == nil {
		t.Error(`Parse: priority "urgent" was accepted, want a rejection`)
	}
	if _, err := Parse([]byte(`{"todos":[]}`)); err == nil {
		t.Error("Parse: an empty list was accepted, want a rejection")
	}

	for name, bad := range map[string]string{
		"empty list":   `{"todos":[]}`,
		"bad status":   `{"todos":[{"id":"a","content":"x","status":"done","priority":"high"}]}`,
		"no id":        `{"todos":[{"content":"x","status":"pending","priority":"high"}]}`,
		"malformed":    `{"todos":`,
		"not a struct": `["a"]`,
	} {
		res, err := tool.Execute(context.Background(), "call-2", []byte(bad), nil)
		if err != nil {
			t.Errorf("Execute(%s): unexpected error: %v", name, err)
			continue
		}
		if !res.IsError {
			t.Errorf("Execute(%s): wrote a list the caller rejected: %q", name, res.Content)
		}
		if res.Details != nil {
			t.Errorf("Execute(%s): a refused write carried Details %+v", name, res.Details)
		}
	}
}
