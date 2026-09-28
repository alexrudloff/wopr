package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/coding/resources"
	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/tui"
)

func runConfigCommand(args []string) int {
	local := false
	var trustOverride *bool
	for _, arg := range args {
		switch arg {
		case "-h", "--help":
			fmt.Println("Usage:\n  wopr config [-l] [--approve|--no-approve]\n\nOpen the resource configuration TUI. Press Tab to switch global and project-local scope.")
			return 0
		case "-l", "--local":
			local = true
		case "-a", "--approve":
			trusted := true
			trustOverride = &trusted
		case "-na", "--no-approve":
			trusted := false
			trustOverride = &trusted
		default:
			return failf("wopr config: unknown option %s", arg)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return failf("wopr config: %v", err)
	}
	agentDir := codingagent.AgentDir()
	globalSettings := codingagent.NewSettingsManagerWithProjectTrust(cwd, agentDir, false)
	projectTrusted, err := resolveProjectTrusted(context.Background(), projectTrustResolutionOptions{
		CWD: cwd, Store: codingagent.NewProjectTrustStore(agentDir), Override: trustOverride,
		Default: globalSettings.GetDefaultProjectTrust(),
	})
	if err != nil {
		return failf("wopr config: %v", err)
	}
	if local && !projectTrusted {
		return failf("wopr config: project is not trusted; use --approve to modify local resource config")
	}
	settings := codingagent.NewSettingsManagerWithProjectTrust(cwd, agentDir, projectTrusted)
	reportSettingsErrors(settings, "config command")
	selector, err := newScopedConfigSelector(cwd, agentDir, globalSettings, settings, local, projectTrusted)
	if err != nil {
		return failf("wopr config: %v", err)
	}
	if err := runConfigSelectorTUI(selector, settings.Get().Theme, agentDir); err != nil {
		return failf("wopr config: %v", err)
	}
	return 0
}

func newScopedConfigSelector(cwd, agentDir string, global, settings *codingagent.SettingsManager, local, projectModeAvailable bool) (*tui.ConfigSelectorComponent, error) {
	globalItems, err := collectConfigResourceItems(cwd, agentDir, global)
	if err != nil {
		return nil, err
	}
	projectItems := globalItems
	if projectModeAvailable {
		projectItems, err = collectConfigResourceItems(cwd, agentDir, settings)
		if err != nil {
			return nil, err
		}
		globalByKey := make(map[string]bool, len(globalItems))
		for _, item := range globalItems {
			globalByKey[configItemKey(item)] = item.Enabled
		}
		for i := range projectItems {
			inherited, found := globalByKey[configItemKey(projectItems[i])]
			projectItems[i].Inherited = found || projectItems[i].Scope == "user"
			if !found && projectItems[i].Scope == "project" {
				inherited = true
			}
			projectItems[i].InheritedEnabled = inherited
			projectItems[i].Override = projectConfigOverride(settings, &projectItems[i])
		}
	}
	writeScope := "global"
	if local {
		writeScope = "project"
	}
	selector := tui.NewScopedConfigSelector(tui.BuildResourceGroups(globalItems), tui.BuildResourceGroups(projectItems), 0, writeScope, projectModeAvailable)
	selector.OnToggle = func(item *tui.ResourceItem, enabled bool) {
		if err := applyConfigToggle(cwd, agentDir, settings, item, enabled); err != nil {
			item.Enabled = !enabled
			fmt.Fprintf(os.Stderr, "config toggle: %v\n", err)
		}
	}
	selector.OnOverride = func(item *tui.ResourceItem, state string) error {
		if err := applyProjectConfigOverride(cwd, settings, item, state); err != nil {
			fmt.Fprintf(os.Stderr, "config toggle: %v\n", err)
			return err
		}
		return nil
	}
	return selector, nil
}

func runConfigSelectorTUI(selector *tui.ConfigSelectorComponent, themeName, agentDir string) error {
	if themeName != "" {
		tui.SetThemeByName(themeName)
	} else {
		tui.DetectTheme()
	}
	if themesDir := filepath.Join(agentDir, "themes"); true {
		_ = tui.ActiveThemeRegistry().LoadDir(themesDir)
		if themeName != "" {
			tui.SetThemeByName(themeName)
		}
	}
	noMouse := false
	ui := tui.New(tui.Options{Mouse: &noMouse})
	ui.Add(selector)
	selector.SetTerminalRows(ui.Height())
	restore, err := tui.EnterRawMode()
	if err != nil {
		return err
	}
	defer restore()
	ui.Start()
	defer ui.StopWithOptions(tui.StopOptions{PreserveScreen: true})
	return driveConfigSelector(ui, selector, os.Stdin)
}

// normalizeConfigInputSequence applies the terminal's native Shift+Enter
// normalization to one split input sequence.
var normalizeConfigInputSequence = tui.NormalizeProcessInputSequence

// driveConfigSelector runs the config selector's input loop against source.
//
// EnterRawMode pushes the Kitty flags that make a terminal report a release for
// every key, so raw reads must be split and filtered before they reach the
// component; handing a release to the selector moves its cursor a second time.
// Takes a reader so the loop a user drives is the loop under test.
func driveConfigSelector(ui *tui.TUI, selector *tui.ConfigSelectorComponent, source io.Reader) error {
	done := false
	selector.OnCancel = func() { done = true }
	selector.OnExit = func() { done = true }
	stdinBuf := codingagent.NewStdinBuffer(codingagent.StdinBufferOptions{
		EscapeTimeout: time.Duration(tui.ResolveEscapeTimeoutMs(os.Getenv) * float64(time.Millisecond)),
	})
	dispatch := func(chunks []string) {
		for _, chunk := range chunks {
			chunk = normalizeConfigInputSequence(chunk)
			if !tui.ShouldDeliverKey(selector, chunk) {
				continue
			}
			selector.HandleInput(chunk)
			if done {
				return
			}
		}
	}
	type readResult struct {
		data []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	readNext := func() {
		go func() {
			data, err := tui.ReadInput(source)
			readCh <- readResult{data: data, err: err}
		}()
	}
	var flushTimer *time.Timer
	var flushC <-chan time.Time
	stopFlush := func() {
		if flushTimer != nil {
			flushTimer.Stop()
		}
		flushTimer = nil
		flushC = nil
	}
	syncFlush := func() {
		stopFlush()
		if stdinBuf.HasPendingFlush() {
			flushTimer = time.NewTimer(stdinBuf.FlushTimeout())
			flushC = flushTimer.C
		}
	}
	defer stopFlush()

	ui.Render()
	readNext()
	for !done {
		select {
		case result := <-readCh:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					dispatch(stdinBuf.Flush())
					return nil
				}
				return result.err
			}
			dispatch(stdinBuf.ProcessTerminalBytes(result.data))
			syncFlush()
			ui.Render()
			if !done {
				readNext()
			}
		case <-flushC:
			stopFlush()
			dispatch(stdinBuf.Flush())
			ui.Render()
		}
	}
	return nil
}

func collectConfigResourceItems(cwd, agentDir string, sm *codingagent.SettingsManager) ([]tui.ResourceItem, error) {
	global := sm.GetGlobalSettings()
	project := sm.GetProjectSettings()
	items := make([]tui.ResourceItem, 0)
	seen := make(map[string]struct{})
	addItem := func(item tui.ResourceItem) {
		key := string(item.ResourceType) + ":" + item.Path
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		items = append(items, item)
	}

	projectBase, projectResourcesEnabled := projectResourceRoot(cwd)
	projectResourcesEnabled = projectResourcesEnabled && sm.IsProjectTrusted()
	userBase := agentDir

	appendTopLevel := func(entries []string, scope, source, baseDir, kind string, autoPaths []string) {
		resourceType := tui.ResourceType(kind)
		plain, patterns := splitResourcePatterns(entries)
		resolved := make([]string, 0, len(plain))
		for _, entry := range plain {
			resolved = append(resolved, resolveSettingsPath(baseDir, entry))
		}
		enabledPaths := applyResourcePatterns(collectResourceFilesFromPaths(resolved, kind), patterns, baseDir, kind)
		for _, path := range collectResourceFilesFromPaths(resolved, kind) {
			addItem(tui.ResourceItem{
				Path:         path,
				Enabled:      slices.Contains(enabledPaths, path),
				ResourceType: resourceType,
				Scope:        scope,
				Origin:       "top-level",
				Source:       source,
				BaseDir:      baseDir,
			})
		}
		for _, path := range autoPaths {
			addItem(tui.ResourceItem{
				Path:         path,
				Enabled:      isEnabledByOverrides(path, entries, baseDir, kind),
				ResourceType: resourceType,
				Scope:        scope,
				Origin:       "top-level",
				Source:       source,
				BaseDir:      baseDir,
			})
		}
	}

	if projectResourcesEnabled {
		appendTopLevel(project.Skills, "project", "local", projectBase, "skills", nil)
		appendTopLevel(project.Prompts, "project", "local", projectBase, "prompts", nil)
		appendTopLevel(project.Themes, "project", "local", projectBase, "themes", nil)
	}
	appendTopLevel(global.Skills, "user", "local", userBase, "skills", nil)
	appendTopLevel(global.Prompts, "user", "local", userBase, "prompts", nil)
	appendTopLevel(global.Themes, "user", "local", userBase, "themes", nil)

	home, _ := os.UserHomeDir()
	userAgentsSkills := filepath.Join(home, ".agents", "skills")
	projectAgentSkillDirs := make([]string, 0)
	for _, dir := range discoverAncestorAgentsSkillDirs(cwd) {
		if samePath(dir, userAgentsSkills) {
			continue
		}
		projectAgentSkillDirs = append(projectAgentSkillDirs, resources.DiscoverAgentSkillDirs(dir)...)
	}
	if projectResourcesEnabled {
		appendTopLevel(project.Skills, "project", "auto", projectBase, "skills", collectAutoDiscoveredResourcePaths(filepath.Join(projectBase, "skills"), "skills"))
		for _, path := range projectAgentSkillDirs {
			appendTopLevel(project.Skills, "project", "auto", filepath.Dir(filepath.Dir(path)), "skills", []string{path})
		}
		appendTopLevel(project.Prompts, "project", "auto", projectBase, "prompts", collectAutoDiscoveredResourcePaths(filepath.Join(projectBase, "prompts"), "prompts"))
		appendTopLevel(project.Themes, "project", "auto", projectBase, "themes", collectAutoDiscoveredResourcePaths(filepath.Join(projectBase, "themes"), "themes"))
	}
	appendTopLevel(global.Skills, "user", "auto", userBase, "skills", collectAutoDiscoveredResourcePaths(filepath.Join(userBase, "skills"), "skills"))
	for _, path := range resources.DiscoverAgentSkillDirs(userAgentsSkills) {
		appendTopLevel(global.Skills, "user", "auto", filepath.Dir(filepath.Dir(path)), "skills", []string{path})
	}
	appendTopLevel(global.Prompts, "user", "auto", userBase, "prompts", collectAutoDiscoveredResourcePaths(filepath.Join(userBase, "prompts"), "prompts"))
	appendTopLevel(global.Themes, "user", "auto", userBase, "themes", collectAutoDiscoveredResourcePaths(filepath.Join(userBase, "themes"), "themes"))

	return items, nil
}

func samePath(a, b string) bool {
	aa, err1 := filepath.Abs(a)
	bb, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && aa == bb
}

func applyConfigToggle(cwd, agentDir string, sm *codingagent.SettingsManager, item *tui.ResourceItem, enabled bool) error {
	baseDir := agentDir
	if item.Scope == "project" {
		baseDir = codingagent.ProjectConfigDir(cwd)
	}
	pattern, err := filepath.Rel(baseDir, item.Path)
	if err != nil {
		pattern = item.Path
	}
	pattern = filepath.ToSlash(pattern)
	var current []string
	var setter func([]string) error
	switch item.ResourceType {
	case tui.ResourceSkills:
		if item.Scope == "project" {
			current = append([]string{}, sm.GetProjectSettings().Skills...)
			setter = sm.SetProjectSkillPaths
		} else {
			current = append([]string{}, sm.GetGlobalSettings().Skills...)
			setter = sm.SetSkillPaths
		}
	case tui.ResourcePrompts:
		if item.Scope == "project" {
			current = append([]string{}, sm.GetProjectSettings().Prompts...)
			setter = sm.SetProjectPromptTemplatePaths
		} else {
			current = append([]string{}, sm.GetGlobalSettings().Prompts...)
			setter = sm.SetPromptTemplatePaths
		}
	case tui.ResourceThemes:
		if item.Scope == "project" {
			current = append([]string{}, sm.GetProjectSettings().Themes...)
			setter = sm.SetProjectThemePaths
		} else {
			current = append([]string{}, sm.GetGlobalSettings().Themes...)
			setter = sm.SetThemePaths
		}
	default:
		return nil
	}
	current = slices.DeleteFunc(current, func(s string) bool {
		return filepath.ToSlash(s) == pattern || filepath.ToSlash(resolveSettingsPath(baseDir, s)) == filepath.ToSlash(item.Path)
	})
	if enabled {
		current = append(current, pattern)
	}
	return setter(current)
}

func configItemKey(item tui.ResourceItem) string {
	return string(item.ResourceType) + "\x00" + canonicalStatusPath(item.Path)
}

func projectConfigOverride(sm *codingagent.SettingsManager, item *tui.ResourceItem) string {
	var entries []string
	switch item.ResourceType {
	case tui.ResourceSkills:
		entries = sm.GetProjectSettings().Skills
	case tui.ResourcePrompts:
		entries = sm.GetProjectSettings().Prompts
	case tui.ResourceThemes:
		entries = sm.GetProjectSettings().Themes
	}
	pattern, _ := filepath.Rel(codingagent.ProjectConfigDir(sm.CWD()), item.Path)
	pattern = filepath.ToSlash(pattern)
	state := "inherit"
	for _, entry := range entries {
		target := strings.TrimLeft(entry, "+-!")
		if filepath.ToSlash(target) != pattern && canonicalStatusPath(resolveSettingsPath(codingagent.ProjectConfigDir(sm.CWD()), target)) != canonicalStatusPath(item.Path) {
			continue
		}
		if strings.HasPrefix(entry, "-") || strings.HasPrefix(entry, "!") {
			state = "unload"
		} else {
			state = "load"
		}
	}
	return state
}

func applyProjectConfigOverride(cwd string, sm *codingagent.SettingsManager, item *tui.ResourceItem, state string) error {
	project := sm.GetProjectSettings()
	var entries []string
	var setter func([]string) error
	switch item.ResourceType {
	case tui.ResourceSkills:
		entries, setter = project.Skills, sm.SetProjectSkillPaths
	case tui.ResourcePrompts:
		entries, setter = project.Prompts, sm.SetProjectPromptTemplatePaths
	case tui.ResourceThemes:
		entries, setter = project.Themes, sm.SetProjectThemePaths
	}
	// Overrides are persisted as the path or base-relative path with the
	// platform separator; override entries match by exact string.
	projectBase := codingagent.ProjectConfigDir(cwd)
	pattern := item.Path
	if !item.Inherited && item.Scope == "project" {
		if relative, err := filepath.Rel(projectBase, item.Path); err == nil {
			pattern = relative
		}
	}
	entries = slices.DeleteFunc(slices.Clone(entries), func(entry string) bool {
		target := strings.TrimLeft(entry, "+-!")
		return canonicalStatusPath(resolveSettingsPath(projectBase, target)) == canonicalStatusPath(item.Path)
	})
	if state != "inherit" {
		if item.Inherited && !slices.Contains(entries, pattern) {
			entries = append(entries, pattern)
		}
		entries = append(entries, map[bool]string{true: "+", false: "-"}[state == "load"]+pattern)
	}
	return setter(entries)
}

func reportSettingsErrors(sm *codingagent.SettingsManager, context string) {
	for _, serr := range sm.DrainErrors() {
		fmt.Fprintf(os.Stderr, "Warning (%s, %s settings): %v\n", context, serr.Scope, serr.Error)
	}
}
