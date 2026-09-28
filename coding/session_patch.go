package coding

import (
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// Edit format per model family: OpenAI's GPT and Codex models are trained
// on apply_patch, so with applyPatch on a request they serve
// declares apply_patch in place of edit; every other model sees edit (with
// hashline anchors where those are on). Both tools stay installed, so a
// call to either always runs.

// usesApplyPatch reports whether model is an OpenAI GPT or Codex model.
func usesApplyPatch(model *ai.Model) bool {
	if model == nil {
		return false
	}
	id := strings.ToLower(model.ID[strings.LastIndex(model.ID, "/")+1:])
	return strings.HasPrefix(id, "gpt-") || strings.Contains(id, "codex")
}

// initApplyPatch installs apply_patch beside the built-in edit tool when
// applyPatch is on. It runs after initEfficiency loads the configuration.
func (s *Session) initApplyPatch() {
	if s.efficiency == nil || !s.efficiency.cfg.ApplyPatch {
		return
	}
	for _, tool := range s.tools {
		if fused, ok := tool.(*efficiency.FusedTool); ok {
			tool = fused.Base()
		}
		if edit, ok := tool.(*tools.EditTool); ok {
			patch := &tools.ApplyPatchTool{CWD: edit.CWD, Queue: edit.Queue, Diagnostics: edit.Diagnostics}
			s.tools = append(s.tools, patch)
			s.agent.SetTools(append(s.agent.Tools(), patch))
			return
		}
	}
}

// patchDeclarations declares apply_patch or edit, not both, for the model
// serving this request.
func (s *Session) patchDeclarations(messages []ai.Message) []ai.Message {
	drop := tools.ApplyPatchName
	if usesApplyPatch(s.requestModel.Load()) {
		drop = "edit"
	}
	var out []ai.Message
	for i, message := range messages {
		system, ok := message.(ai.SystemMessage)
		if !ok || !slices.ContainsFunc(system.ToolsAdded, func(t ai.ToolSchema) bool { return t.Name == tools.ApplyPatchName }) {
			continue
		}
		if out == nil {
			out = slices.Clone(messages)
		}
		system.ToolsAdded = slices.DeleteFunc(slices.Clone(system.ToolsAdded), func(t ai.ToolSchema) bool { return t.Name == drop })
		out[i] = system
	}
	if out == nil {
		return messages
	}
	return out
}
