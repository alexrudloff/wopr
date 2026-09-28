package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Lazy tools: rarely used tools stay out of every request until the model
// asks for them with load_tools, which keeps the fixed prompt prefix small.
// Loading one changes the declared tools, so the provider re-reads the
// prompt once; the tools stay loaded for the rest of the session.

// loadToolsName is the loader tool's name.
const loadToolsName = "load_tools"

// lazyToolNames are the tools held back until requested.
var lazyToolNames = []string{"task", "web_fetch", "web_search"}

// initLazyTools holds back the lazy tools that are active and installs the
// loader. It runs after every tool is installed.
func (s *Session) initLazyTools() {
	if s.efficiency == nil || !s.efficiency.cfg.LazyTools {
		return
	}
	var held []agent.AgentTool
	var active []string
	for _, tool := range s.agent.Tools() {
		if slices.Contains(lazyToolNames, tool.Name()) {
			held = append(held, tool)
		} else {
			active = append(active, tool.Name())
		}
	}
	if len(held) == 0 {
		return
	}
	loader := &loadToolsTool{s: s, held: held}
	s.tools = append(s.tools, loader)
	s.SetActiveToolsByName(append(active, loadToolsName))
}

// loadToolsTool activates held-back tools by name.
type loadToolsTool struct {
	s    *Session
	held []agent.AgentTool
}

func (t *loadToolsTool) Name() string  { return loadToolsName }
func (t *loadToolsTool) Label() string { return "Load Tools" }

func (t *loadToolsTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }

func (t *loadToolsTool) Schema() ai.ToolSchema {
	var lines []string
	for _, tool := range t.held {
		description := tool.Schema().Description
		if i := strings.Index(description, ". "); i > 0 {
			description = description[:i+1]
		}
		lines = append(lines, tool.Name()+": "+description)
	}
	return ai.ToolSchema{
		Name:        loadToolsName,
		Description: "Load more tools; they can be called from your next step. Available:\n" + strings.Join(lines, "\n"),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"names": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Tool names to load"},
			},
			"required": []string{"names"},
		},
	}
}

func (t *loadToolsTool) Execute(_ context.Context, _ string, params json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return agent.AgentToolResult{}, err
	}
	active := t.s.ActiveToolNames()
	var loaded, unknown []string
	for _, name := range in.Names {
		switch {
		case slices.Contains(active, name):
			loaded = append(loaded, name)
		case slices.ContainsFunc(t.held, func(tool agent.AgentTool) bool { return tool.Name() == name }):
			active = append(active, name)
			loaded = append(loaded, name)
		default:
			unknown = append(unknown, name)
		}
	}
	t.s.SetActiveToolsByName(active)
	text := "Loaded: " + strings.Join(loaded, ", ") + "."
	if len(unknown) > 0 {
		return agent.AgentToolResult{Content: text + fmt.Sprintf(" Unknown: %s.", strings.Join(unknown, ", ")), IsError: len(loaded) == 0}, nil
	}
	return agent.AgentToolResult{Content: text}, nil
}
