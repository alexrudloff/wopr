package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Tool is update_plan: the model's way to add and update queue items. A call
// updates the items it names and keeps the rest.
type Tool struct {
	Queue *Queue
	// OnComplete, when set, runs after an update that completed an item.
	OnComplete func(toolCallID string)
}

func (t *Tool) Name() string                           { return "update_plan" }
func (t *Tool) Label() string                          { return "Update queue" }
func (t *Tool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }

func (t *Tool) Schema() ai.ToolSchema {
	statuses := make([]string, len(ModelStatuses))
	for i, s := range ModelStatuses {
		statuses[i] = string(s)
	}
	properties := map[string]any{
		"steps": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     map[string]any{"type": "string"},
					"goal":   map[string]any{"type": "string"},
					"status": map[string]any{"type": "string", "enum": statuses},
				},
				"required": []string{"id", "status"},
			},
		},
	}
	guidelines := []string{
		"Keep the work queue with update_plan: queue what you can't do right now, update items as you and background agents finish (their results say what changed), and answer status questions from it.",
	}
	return ai.ToolSchema{
		Name:        "update_plan",
		Description: "Add or update items of the session's work queue; items you leave out are kept.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   []string{"steps"},
		},
		PromptGuidelines: guidelines,
	}
}

func (t *Tool) Execute(ctx context.Context, toolCallID string, params json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	if ctx.Err() != nil {
		return agent.AgentToolResult{}, errors.New("queue update was aborted")
	}
	var raw struct {
		Steps []Update `json:"steps"`
	}
	if err := json.Unmarshal(params, &raw); err != nil {
		return agent.AgentToolResult{}, err
	}
	completed, err := t.Queue.Upsert(raw.Steps)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	items := t.Queue.Items()
	for i := range items {
		items[i].Dispatch = nil
	}
	if len(completed) > 0 && t.OnComplete != nil {
		t.OnComplete(toolCallID)
	}
	open := 0
	for _, it := range items {
		if it.Status.Open() {
			open++
		}
	}
	return agent.AgentToolResult{
		Content: Format(items),
		Details: map[string]any{"items": items},
		Preview: fmt.Sprintf("Queue · %d open, %d done", open, len(items)-open),
	}, nil
}
