package codingagent

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// ReloadResourceSnapshot is the recomputed settings/resource view used by
// /reload: prompt/theme/skill/context inputs are re-resolved from current
// settings.
type ReloadResourceSnapshot struct {
	PromptPaths        []string
	ThemePaths         []string
	SkillPaths         []string
	ContextFiles       []ContextFile
	ResourceSourceInfo map[string]ResourceSourceInfo
}

func (m *InteractiveMode) applyReloadResourceSnapshot(snapshot ReloadResourceSnapshot) {
	m.opts.PromptPaths = slices.Clone(snapshot.PromptPaths)
	m.opts.ThemePaths = slices.Clone(snapshot.ThemePaths)
	m.opts.SkillPaths = slices.Clone(snapshot.SkillPaths)
	m.opts.ContextFiles = slices.Clone(snapshot.ContextFiles)
	if snapshot.ResourceSourceInfo != nil {
		m.resourceSourceInfo = maps.Clone(snapshot.ResourceSourceInfo)
	}
}

// refreshAgentTools rebuilds the built-in tools from the reloaded settings.
func (m *InteractiveMode) refreshAgentTools() {
	if m.agent == nil {
		return
	}
	builtin := tools.CreateAllTools(m.opts.CWD, m.settings(), filepath.Join(m.opts.AgentDir, "bin"))
	selected := tools.SelectBuiltinTools(builtin, m.opts.ActiveBuiltinTools, m.opts.AllowedTools)
	m.agent.SetTools(slices.DeleteFunc(selected, func(tool agent.AgentTool) bool {
		_, excluded := m.opts.ExcludedTools[tool.Name()]
		return excluded
	}))
}

func (m *InteractiveMode) reloadSkillsFromPaths() {
	if m.opts.NoSkills {
		m.opts.Skills = nil
		return
	}
	if len(m.opts.SkillPaths) == 0 {
		m.opts.Skills = nil
		return
	}
	var allSkills []*SkillDef
	for _, skillPath := range m.opts.SkillPaths {
		loaded, err := LoadSkillsFromPath(skillPath)
		if err != nil {
			m.reloadIssues = append(m.reloadIssues, fmt.Sprintf("[skill] %s: %v", skillPath, err))
		}
		for _, skill := range loaded {
			for _, diagnostic := range SkillDiagnostics(skill) {
				m.reloadIssues = append(m.reloadIssues, fmt.Sprintf("[skill] %s: %s", skill.Path, diagnostic))
			}
			if strings.TrimSpace(skill.Description) != "" {
				allSkills = append(allSkills, skill)
			}
		}
	}
	m.opts.Skills = DeduplicateSkills(allSkills)
}

func (m *InteractiveMode) rebuildSystemPromptFromResources() {
	if m.opts.RebuildSystemPrompt == nil {
		return
	}
	name := m.systemPromptName()
	systemPrompt := m.opts.RebuildSystemPrompt(m.opts.Skills, m.opts.ContextFiles, name)
	m.builtSystemPrompt = name
	if p, ok := m.opts.SessionHandle.(interface{ SetPersona(string) }); ok {
		p.SetPersona(SystemPromptPersona(m.opts.AgentDir, name))
	}
	m.opts.SystemPrompt = systemPrompt
	if m.agent != nil {
		m.agent.SetSystemPrompt(systemPrompt)
	}
}
