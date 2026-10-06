package main

import (
	"path/filepath"

	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/prompts"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

func reloadResourceSnapshotProvider(cwd, agentDir string, sm *codingagent.SettingsManager, flags CLIFlags, projectTrusted bool) func() codingagent.ReloadResourceSnapshot {
	return func() codingagent.ReloadResourceSnapshot {
		promptPaths := collectPromptPaths(cwd, agentDir, sm, flags, sm.IsProjectTrusted())
		themePaths := collectThemePaths(cwd, agentDir, sm, flags, sm.IsProjectTrusted())
		skillPaths := collectSkillInputs(cwd, agentDir, sm, flags, projectTrusted)
		contextFiles := loadContextFiles(cwd, agentDir, flags.NoContextFiles)
		infos := resourceSourceInfoProvider(cwd, agentDir, sm, flags)()
		return codingagent.ReloadResourceSnapshot{
			PromptPaths:        promptPaths,
			ThemePaths:         themePaths,
			SkillPaths:         skillPaths,
			ContextFiles:       contextFiles,
			ResourceSourceInfo: infos,
		}
	}
}

// systemPromptOptions assembles the system prompt inputs every mode and
// /reload share.
func systemPromptOptions(cwd, agentDir string, projectTrusted bool, flags CLIFlags, toolNames []string, skills []*codingagent.SkillDef, contextFiles []codingagent.ContextFile, systemPrompt string) prompts.Options {
	promptSkills := make([]prompts.Skill, 0, len(skills))
	for _, skill := range skills {
		promptSkills = append(promptSkills, prompts.Skill{
			Name: skill.Name, Description: skill.Description, Path: skill.Path,
			DisableModelInvocation: skill.DisableModelInvocation,
		})
	}
	resolvedPrompts := resolvePromptInputs(cwd, agentDir, flags, projectTrusted)
	options := prompts.Options{
		Cwd: cwd, Tools: toolNames, ToolHints: prompts.DefaultToolSnippets(), ToolGuidelines: tools.DefaultToolGuidelines(),
		Skills: promptSkills, DocsPath: filepath.Join(codingagent.ConfigRoot(), "docs"),
		AppendMode: "append", ContextFiles: toPromptContextFiles(contextFiles),
		CustomPrompt: resolvedPrompts.custom, AppendSystemPrompt: resolvedPrompts.append,
		Persona: codingagent.SystemPromptPersona(agentDir, systemPrompt),
	}
	if resolvedPrompts.custom != "" {
		options.AppendMode = "replace"
	}
	return options
}

func systemPromptRebuilder(cwd, agentDir string, projectTrusted bool, flags CLIFlags, startupToolNames []string) func(skills []*codingagent.SkillDef, contextFiles []codingagent.ContextFile, systemPrompt string) string {
	toolNames := append([]string(nil), startupToolNames...)
	return func(skills []*codingagent.SkillDef, contextFiles []codingagent.ContextFile, systemPrompt string) string {
		return prompts.BuildDefaultPrompt(systemPromptOptions(cwd, agentDir, projectTrusted, flags, toolNames, skills, contextFiles, systemPrompt))
	}
}
