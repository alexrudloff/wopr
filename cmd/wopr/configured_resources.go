package main

import (
	"os"
	"path/filepath"
	"slices"

	"github.com/alexrudloff/wopr/coding/resources"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

func collectStartupThemePaths(cwd, agentDir string, sm *codingagent.SettingsManager) []string {
	global := sm.GetGlobalSettings()
	paths := opencodeThemePaths(cwd, false)
	paths = append(paths, collectTopLevelResourcePaths(filepath.Join(agentDir, "themes"), global.Themes, "themes")...)
	return resources.Deduplicate(paths)
}

// opencodeThemePaths returns opencode theme files so a user's opencode themes
// work unchanged: the global opencode config themes directory, then (for a
// trusted project) .opencode/themes in every ancestor of cwd, nearest last.
// wopr's own theme directories load after these, so a wopr theme wins a name
// collision.
func opencodeThemePaths(cwd string, includeProject bool) []string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		if home, err := os.UserHomeDir(); err == nil {
			configHome = filepath.Join(home, ".config")
		}
	}
	var paths []string
	if configHome != "" {
		paths = append(paths, collectTopLevelResourcePaths(filepath.Join(configHome, "opencode", "themes"), nil, "themes")...)
	}
	if !includeProject || cwd == "" {
		return paths
	}
	var dirs []string
	for dir := filepath.Clean(cwd); ; dir = filepath.Dir(dir) {
		dirs = append(dirs, filepath.Join(dir, ".opencode", "themes"))
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
	}
	for _, dir := range slices.Backward(dirs) {
		paths = append(paths, collectTopLevelResourcePaths(dir, nil, "themes")...)
	}
	return paths
}

func projectResourceRoot(cwd string) (string, bool) {
	root := codingagent.ProjectConfigDir(cwd)
	return root, !samePath(root, codingagent.ConfigRoot())
}

func collectPromptPaths(cwd, agentDir string, sm *codingagent.SettingsManager, flags CLIFlags, projectTrusted bool) []string {
	paths := make([]string, 0)
	// The first same-name prompt wins after ordering CLI, project, then user
	// resources.
	for _, promptPath := range flags.PromptTemplates {
		paths = append(paths, collectResourceFilesFromPaths([]string{resolveSettingsPath(cwd, promptPath)}, "prompts")...)
	}
	if !flags.NoPromptTemplates {
		if projectRoot, ok := projectResourceRoot(cwd); projectTrusted && ok {
			paths = append(paths, collectTopLevelResourcePaths(filepath.Join(projectRoot, "prompts"), sm.GetProjectSettings().Prompts, "prompts")...)
		}
		paths = append(paths, collectTopLevelResourcePaths(filepath.Join(agentDir, "prompts"), sm.GetGlobalSettings().Prompts, "prompts")...)
	}
	return resources.Deduplicate(paths)
}

func collectThemePaths(cwd, agentDir string, sm *codingagent.SettingsManager, flags CLIFlags, projectTrusted bool) []string {
	paths := make([]string, 0)
	if !flags.NoThemes {
		paths = append(paths, opencodeThemePaths(cwd, projectTrusted)...)
		paths = append(paths, collectTopLevelResourcePaths(filepath.Join(agentDir, "themes"), sm.GetGlobalSettings().Themes, "themes")...)
		if projectRoot, ok := projectResourceRoot(cwd); projectTrusted && ok {
			paths = append(paths, collectTopLevelResourcePaths(filepath.Join(projectRoot, "themes"), sm.GetProjectSettings().Themes, "themes")...)
		}
	}
	for _, p := range flags.Themes {
		paths = append(paths, collectResourceFilesFromPaths([]string{resolveSettingsPath(cwd, p)}, "themes")...)
	}
	return resources.Deduplicate(paths)
}

func collectSkillInputs(cwd, agentDir string, sm *codingagent.SettingsManager, flags CLIFlags, projectTrusted bool) []string {
	inputs := make([]string, 0)
	for _, p := range flags.Skills {
		inputs = append(inputs, collectResourceFilesFromPaths([]string{resolveSettingsPath(cwd, p)}, "skills")...)
	}
	if flags.NoSkills {
		return resources.Deduplicate(inputs)
	}

	// Same-name Skill collisions resolve first-wins after ordering paths by
	// precedence: CLI, project explicit, project auto, user explicit, then user
	// auto.
	projectRoot, projectResourcesEnabled := projectResourceRoot(cwd)
	if projectTrusted && projectResourcesEnabled {
		projectSettings := sm.GetProjectSettings().Skills
		inputs = append(inputs, resolveConfiguredResourceEntries(projectSettings, projectRoot, "skills")...)
		projectAuto := collectAutoDiscoveredResourcePaths(filepath.Join(projectRoot, "skills"), "skills")
		inputs = append(inputs, filterAutoDiscoveredPaths(projectAuto, projectSettings, projectRoot, "skills")...)

		home, _ := os.UserHomeDir()
		userAgentsSkills := filepath.Join(home, ".agents", "skills")
		for _, dir := range discoverAncestorAgentsSkillDirs(cwd) {
			abs, _ := filepath.Abs(dir)
			uabs, _ := filepath.Abs(userAgentsSkills)
			if abs == uabs {
				continue
			}
			inputs = append(inputs, filterAutoDiscoveredPaths(resources.DiscoverAgentSkillDirs(dir), projectSettings, filepath.Dir(dir), "skills")...)
		}
	}

	userSettings := sm.GetGlobalSettings().Skills
	inputs = append(inputs, resolveConfiguredResourceEntries(userSettings, agentDir, "skills")...)
	userAuto := collectAutoDiscoveredResourcePaths(filepath.Join(agentDir, "skills"), "skills")
	inputs = append(inputs, filterAutoDiscoveredPaths(userAuto, userSettings, agentDir, "skills")...)
	home, _ := os.UserHomeDir()
	userAgentsSkills := filepath.Join(home, ".agents", "skills")
	inputs = append(inputs, filterAutoDiscoveredPaths(resources.DiscoverAgentSkillDirs(userAgentsSkills), userSettings, filepath.Dir(userAgentsSkills), "skills")...)
	return resources.Deduplicate(inputs)
}

func collectTopLevelResourcePaths(autoDir string, entries []string, kind string) []string {
	auto := collectAutoDiscoveredResourcePaths(autoDir, kind)
	baseDir := filepath.Dir(autoDir)
	explicit := resolveConfiguredResourceEntries(entries, baseDir, kind)
	auto = filterAutoDiscoveredPaths(auto, entries, baseDir, kind)
	return append(auto, explicit...)
}

func resolveConfiguredResourceEntries(entries []string, baseDir string, kind string) []string {
	return resources.ResolveConfigured(entries, baseDir, resources.Kind(kind))
}

func filterAutoDiscoveredPaths(paths, overrides []string, baseDir string, kind string) []string {
	return resources.FilterAutomatic(paths, overrides, baseDir, resources.Kind(kind))
}

func collectAutoDiscoveredResourcePaths(dir string, kind string) []string {
	return resources.DiscoverAutomatic(dir, resources.Kind(kind))
}

func collectResourceFilesFromPaths(paths []string, kind string) []string {
	return resources.Collect(paths, resources.Kind(kind))
}

func splitResourcePatterns(entries []string) (plain, patterns []string) {
	return resources.SplitPatterns(entries)
}

func applyResourcePatterns(allPaths, patterns []string, baseDir, kind string) []string {
	return resources.ApplyPatterns(allPaths, patterns, baseDir, resources.Kind(kind))
}

func isEnabledByOverrides(filePath string, patterns []string, baseDir, kind string) bool {
	return resources.EnabledByOverrides(filePath, patterns, baseDir, resources.Kind(kind))
}

func resolveSettingsPath(baseDir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Clean(filepath.Join(baseDir, p))
}

// discoverAncestorAgentsSkillDirs walks from startDir up to the git root
// (or filesystem root), collecting .agents/skills/ directories at each level.
func discoverAncestorAgentsSkillDirs(startDir string) []string {
	resolved, err := filepath.Abs(startDir)
	if err != nil {
		return nil
	}
	gitRoot := findGitRepoRoot(resolved)

	var dirs []string
	dir := resolved
	for {
		candidate := filepath.Join(dir, ".agents", "skills")
		dirs = append(dirs, candidate)
		if gitRoot != "" && dir == gitRoot {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return dirs
}

// findGitRepoRoot walks up from startDir looking for a .git directory.
// Returns the directory containing .git, or "" if none found.
func findGitRepoRoot(startDir string) string {
	dir := startDir
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
