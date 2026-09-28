// Package askuser is the ask_user tool: the model asks the user one
// multiple-choice question, with free text allowed, when a decision is
// theirs. Only a frontend with a user installs it; subagents never get it.
package askuser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/text"
)

// Name is the tool name.
const Name = "ask_user"

// Limits on a question.
const (
	MinOptions  = 2
	MaxOptions  = 5
	maxQuestion = 1000
	maxLabel    = 100
	maxDetail   = 200
)

// Option is one answer the model offers.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question is what the model asks.
type Question struct {
	Question string   `json:"question"`
	Options  []Option `json:"options"`
}

// Answer is the user's reply: a chosen option's label, their own text, or
// neither when they declined.
type Answer struct {
	Choice   string `json:"choice,omitempty"`
	Other    string `json:"other,omitempty"`
	Declined bool   `json:"declined,omitempty"`
}

// Asker shows a question to the user and waits for the answer. It returns
// an error only when no answer can come (the run ended).
type Asker func(ctx context.Context, q Question) (Answer, error)

// Result texts the model reads.
const (
	declinedText    = "User declined to answer. Continue with your best judgment and state the assumption, or stop if you can't."
	unavailableText = "No user is available to answer. Decide yourself and state the assumption."
)

// Tool is ask_user.
type Tool struct {
	// Ask returns the frontend's asker, or nil when no user is available.
	Ask func() Asker
}

func (t *Tool) Name() string                           { return Name }
func (t *Tool) Label() string                          { return "Ask user" }
func (t *Tool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }

// Schema is kept small: it is sent with every request while the tool is on.
func (t *Tool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name:        Name,
		Description: "Ask the user one multiple-choice question; they may also type their own answer. Only for a decision that is theirs and can't be settled from the code or sensible defaults; never to ask permission to proceed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"question": map[string]any{"type": "string"},
				"options": map[string]any{
					"type":     "array",
					"minItems": MinOptions,
					"maxItems": MaxOptions,
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"label":       map[string]any{"type": "string"},
							"description": map[string]any{"type": "string"},
						},
						"required": []string{"label"},
					},
				},
			},
			"required": []string{"question", "options"},
		},
	}
}

// Parse validates a call's arguments.
func Parse(raw json.RawMessage) (Question, error) {
	var q Question
	if err := json.Unmarshal(raw, &q); err != nil {
		return Question{}, err
	}
	q.Question = strings.TrimSpace(q.Question)
	if q.Question == "" || len(q.Question) > maxQuestion {
		return Question{}, fmt.Errorf("question must be 1-%d bytes", maxQuestion)
	}
	if len(q.Options) < MinOptions || len(q.Options) > MaxOptions {
		return Question{}, fmt.Errorf("give %d-%d options", MinOptions, MaxOptions)
	}
	seen := map[string]bool{}
	for i, o := range q.Options {
		label := strings.Join(strings.Fields(o.Label), " ")
		switch {
		case label == "" || len(label) > maxLabel:
			return Question{}, fmt.Errorf("option labels must be 1-%d bytes", maxLabel)
		case seen[strings.ToLower(label)]:
			return Question{}, fmt.Errorf("option %q appears twice", label)
		}
		seen[strings.ToLower(label)] = true
		q.Options[i].Label = label
		q.Options[i].Description = text.Clip(strings.Join(strings.Fields(o.Description), " "), maxDetail)
	}
	return q, nil
}

func (t *Tool) Execute(ctx context.Context, _ string, raw json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	q, err := Parse(raw)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	var ask Asker
	if t.Ask != nil {
		ask = t.Ask()
	}
	if ask == nil {
		return agent.AgentToolResult{Content: unavailableText, Preview: "no user available"}, nil
	}
	a, err := ask(ctx, q)
	if err != nil {
		if ctx.Err() != nil {
			return agent.AgentToolResult{}, errors.New("question was aborted")
		}
		return agent.AgentToolResult{}, err
	}
	text, preview := a.Text()
	return agent.AgentToolResult{Content: text, Details: a, Preview: preview}, nil
}

// Text is the answer as the model reads it, and a short preview.
func (a Answer) Text() (string, string) {
	switch {
	case a.Other != "":
		return "User answered: " + a.Other, a.Other
	case a.Choice != "":
		return "User chose: " + a.Choice, a.Choice
	}
	return declinedText, "declined"
}
