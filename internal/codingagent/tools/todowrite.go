package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Name is the tool name.
const Name = "todowrite"

// The statuses and priorities the client renders. A value outside either set is
// one its todo panel cannot draw, so Parse refuses it rather than writing a todo
// the client will silently drop. Priority's value set is the working assumption,
// not a verified contract: if the client disagrees, this is the one switch to
// change.
var (
	todoStatuses   = []string{"pending", "in_progress", "completed", "cancelled"}
	todoPriorities = []string{"high", "medium", "low"}
)

// Todo is one item in the list. The four fields are the client's TodoItem
// verbatim, because this is the only producer of the two surfaces the client
// renders them from: the session todo list and a tool part's own metadata.
type Todo struct {
	ID       string `json:"id"`
	Content  string `json:"content"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

// TodoTool writes the model's todo list.
type TodoTool struct{}

func (t *TodoTool) Name() string  { return Name }
func (t *TodoTool) Label() string { return "Todo list" }

func (t *TodoTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeParallel }

// Schema is kept small: it is sent with every request while the tool is on.
func (t *TodoTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name:        Name,
		Description: "Write the task list for the current request. Call it with the whole list every time — it replaces what is there — and keep exactly one item in_progress while work remains. Pass a stable id per item so an update reuses the entry.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"todos": map[string]any{
					"type":        "array",
					"minItems":    1,
					"description": "The complete list, in the order it should be worked through.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":       map[string]any{"type": "string", "description": "Stable identifier for this item."},
							"content":  map[string]any{"type": "string", "description": "What the item is."},
							"status":   map[string]any{"type": "string", "enum": todoStatuses},
							"priority": map[string]any{"type": "string", "enum": todoPriorities},
						},
						"required": []string{"id", "content", "status", "priority"},
					},
				},
			},
			"required": []string{"todos"},
		},
	}
}

// Parse validates a call's arguments and returns the list to write.
func Parse(raw json.RawMessage) ([]Todo, error) {
	var body struct {
		Todos []Todo `json:"todos"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("%s: invalid params: %w", Name, err)
	}
	if len(body.Todos) == 0 {
		return nil, errors.New(Name + ": the list is empty; a cleared list leaves the caller with nothing to show")
	}
	seen := make(map[string]bool, len(body.Todos))
	for i, todo := range body.Todos {
		todo.ID = strings.TrimSpace(todo.ID)
		todo.Content = strings.TrimSpace(todo.Content)
		switch {
		case todo.ID == "":
			return nil, fmt.Errorf("%s: item %d has no id", Name, i+1)
		case seen[todo.ID]:
			return nil, fmt.Errorf("%s: id %q appears twice", Name, todo.ID)
		case todo.Content == "":
			return nil, fmt.Errorf("%s: item %q has no content", Name, todo.ID)
		case !slices.Contains(todoStatuses, todo.Status):
			return nil, fmt.Errorf("%s: item %q has status %q, want one of %s", Name, todo.ID, todo.Status, strings.Join(todoStatuses, ", "))
		case !slices.Contains(todoPriorities, todo.Priority):
			return nil, fmt.Errorf("%s: item %q has priority %q, want one of %s", Name, todo.ID, todo.Priority, strings.Join(todoPriorities, ", "))
		}
		seen[todo.ID] = true
		body.Todos[i] = todo
	}
	return body.Todos, nil
}

// Execute writes the list and reports the per-status counts, which is what the
// model reads back. A rejected list is a tool error rather than a silent
// write: a list the client cannot render is worse than one the model is told
// to fix.
func (t *TodoTool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	if ctx.Err() != nil {
		return abortedResult, nil
	}
	todos, err := Parse(raw)
	if err != nil {
		return agent.ErrorResult(err.Error()), nil
	}
	counts := make(map[string]int, len(todoStatuses))
	for _, todo := range todos {
		counts[todo.Status]++
	}
	parts := make([]string, 0, len(todoStatuses))
	for _, status := range todoStatuses {
		if counts[status] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[status], status))
		}
	}
	return agent.AgentToolResult{
		Content: fmt.Sprintf("Todo list written: %d items (%s).", len(todos), strings.Join(parts, ", ")),
		Details: todos,
	}, nil
}
