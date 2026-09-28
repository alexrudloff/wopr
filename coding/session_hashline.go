package coding

import (
	"slices"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// Hashline anchors (internal/codingagent/tools/hashline.go) are decided per
// model: a router model's own "hashline" flag, else the "hashline" setting,
// whose "auto" default turns them on for free-local and free-remote tiers.
// Weak local models gain the most from anchored edits; frontier models edit
// exact text reliably and would only pay for the anchors. Subagents are
// read-only and never get anchors.

// hashlineMode reports whether provider/model uses hashline anchors.
func (s *Session) hashlineMode(provider, model string) bool {
	var flag *bool
	free := false
	if s.router != nil {
		flag, free = s.router.Hashline(provider + "/" + model)
	}
	if flag != nil {
		return *flag
	}
	switch s.services.SettingsManager().GetHashline() {
	case "on":
		return true
	case "off":
		return false
	}
	return free
}

// initHashline hands the per-model decision to the built-in read and edit
// tools. It runs before initEfficiency wraps edit.
func (s *Session) initHashline() {
	s.hashlineTools = map[string]bool{}
	for _, tool := range s.tools {
		switch t := tool.(type) {
		case *tools.ReadTool:
			t.Hashline = s.hashlineMode
			s.hashlineTools[t.Name()] = true
		case *tools.EditTool:
			t.Hashline = s.hashlineMode
			s.hashlineTools[t.Name()] = true
		}
	}
}

// hashlineDeclarations swaps the read and edit declarations for their
// hashline forms when the model serving this request uses anchors. Only the
// request changes: the transcript keeps one declaration per tool, so a model
// switch never records a tool change.
func (s *Session) hashlineDeclarations(messages []ai.Message) []ai.Message {
	model := s.requestModel.Load()
	if model == nil || !s.hashlineMode(providerID(model), model.ID) {
		return messages
	}
	var out []ai.Message
	for i, message := range messages {
		system, ok := message.(ai.SystemMessage)
		if !ok || !slices.ContainsFunc(system.ToolsAdded, s.isHashlineTool) {
			continue
		}
		if out == nil {
			out = slices.Clone(messages)
		}
		system.ToolsAdded = slices.Clone(system.ToolsAdded)
		for j, tool := range system.ToolsAdded {
			if s.isHashlineTool(tool) {
				system.ToolsAdded[j] = tools.HashlineSchema(tool)
			}
		}
		out[i] = system
	}
	if out == nil {
		return messages
	}
	return out
}

// isHashlineTool reports a declaration of a built-in read or edit tool.
func (s *Session) isHashlineTool(tool ai.ToolSchema) bool { return s.hashlineTools[tool.Name] }
