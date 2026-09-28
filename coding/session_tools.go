package coding

import (
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/askuser"
	"github.com/alexrudloff/wopr/internal/codingagent/prompts"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// ActiveToolNames returns the names of the tools active for the next agent
// turn.
func (s *Session) ActiveToolNames() []string {
	active := s.agent.Tools()
	names := make([]string, len(active))
	for i, tool := range active {
		names[i] = tool.Name()
	}
	return names
}

// SetActiveToolsByName activates the Session's tools named in names, in that
// order. Names the Session has no tool for are ignored. The change applies
// from the next provider request, which declares the new loadout, and the
// Session's default system prompt lists the active tools from the next prompt.
func (s *Session) SetActiveToolsByName(names []string) {
	var active []agent.AgentTool
	var valid []string
	for _, name := range names {
		for _, tool := range s.tools {
			if tool.Name() == name {
				active = append(active, tool)
				valid = append(valid, name)
				break
			}
		}
	}
	// ask_user stays on while the frontend's asker is installed.
	if s.asker.Load() != nil && s.askAllowed && !slices.Contains(valid, askuser.Name) {
		active = append(active, s.askTool)
		valid = append(valid, askuser.Name)
	}
	s.agent.SetTools(active)
	s.rebuildSystemPrompt(slices.DeleteFunc(slices.Clone(valid), func(name string) bool { return name == tools.ApplyPatchName }))
}

// toolGuidelines merges the built-in guidelines with each session tool's own
// schema guidelines, so wrapped and harness tools (then_run, obs_recall,
// update_plan) tell the model how to use them.
func (s *Session) toolGuidelines() map[string][]string {
	out := tools.DefaultToolGuidelines()
	for _, tool := range s.tools {
		if guidelines := tool.Schema().PromptGuidelines; len(guidelines) > 0 {
			out[tool.Name()] = guidelines
		}
	}
	return out
}

// toolHints lists a one-line snippet for every session tool, using the
// built-in snippet when there is one and the schema description otherwise.
func (s *Session) toolHints() map[string]string {
	out := prompts.DefaultToolSnippets()
	for _, tool := range s.tools {
		if _, ok := out[tool.Name()]; ok {
			continue
		}
		description := tool.Schema().Description
		if i := strings.Index(description, ". "); i > 0 {
			description = description[:i+1]
		}
		out[tool.Name()] = description
	}
	return out
}

// rebuildSystemPrompt lists toolNames in the default system prompt the
// Session built itself. A prompt the caller
// supplied is left unchanged.
func (s *Session) rebuildSystemPrompt(toolNames []string) {
	if !s.defaultSystemPrompt {
		return
	}
	s.baseSystemSections = prompts.BuildSystemPromptSections(prompts.Options{Cwd: s.services.CWD(), Tools: toolNames, ToolHints: s.toolHints(), ToolGuidelines: s.toolGuidelines()})
	s.baseSystemPrompt = ai.GetCurrentSystemPrompt([]ai.Message{ai.SystemMessage{Sections: s.baseSystemSections}})
}

// restoreToolsFromTranscript activates the tools the current branch's
// transcript declares, keeping only tools the Session has. A branch without
// a system message keeps the current tools.
func (s *Session) restoreToolsFromTranscript() {
	var systems []ai.Message
	for _, message := range s.inner.BuildSessionProjection().Messages {
		if message.System != nil {
			systems = append(systems, *message.System)
		}
	}
	current := ai.GetCurrentSystemMessage(systems)
	if current == nil {
		return
	}
	names := make([]string, len(current.ToolsAdded))
	for i, tool := range current.ToolsAdded {
		names[i] = tool.Name
	}
	s.SetActiveToolsByName(names)
}
