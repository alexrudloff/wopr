package codingagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// dispatchSlash runs a slash command line. An unknown command does nothing:
// the caller sends it to the model as a message.
func (m *InteractiveMode) dispatchSlash(line string) {
	cmd, args, err := m.slashRegistry.lookup(line)
	if err == nil {
		err = cmd.run(m, args)
	}
	if err != nil && !errors.Is(err, ErrInteractiveCrashed) && !errors.Is(err, ErrUnknownSlashCommand) {
		m.showError(err.Error())
	}
	m.tuiInst.Render()
}

// writeDebugLog dumps the current frame and message history to a debug log
// file under the agent dir and returns its path. The rendered-lines section
// reflects wopr's line renderer.
func (m *InteractiveMode) writeDebugLog() (string, error) {
	width, height := m.tuiInst.Width(), m.tuiInst.Height()
	lines := m.tuiInst.RenderSnapshot(width)

	var b strings.Builder
	fmt.Fprintf(&b, "Debug output at %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Terminal: %dx%d\n", width, height)
	fmt.Fprintf(&b, "Total lines: %d\n\n", len(lines))
	b.WriteString("=== All rendered lines with visible widths ===\n")
	for i, line := range lines {
		esc, _ := json.Marshal(line)
		fmt.Fprintf(&b, "[%d] (w=%d) %s\n", i, widthx.VisibleWidth(line), esc)
	}
	b.WriteString("\n=== Agent messages (JSONL) ===\n")
	for _, msg := range m.agent.Messages() {
		j, err := json.Marshal(msg)
		if err != nil {
			return "", fmt.Errorf("marshal message: %w", err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	path := filepath.Join(m.opts.AgentDir, AppName+"-debug.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// buildAutocompleteProvider constructs the combined slash-command and
// @-file autocomplete provider for the editor.
func (m *InteractiveMode) buildAutocompleteProvider() tui.AutocompleteProvider {
	builtins := BuiltinSlashCommands()
	promptTemplates := withBuiltinPromptCommands(m.promptTemplates)
	cmds := make([]tui.SlashCommand, 0, len(builtins)+len(promptTemplates))
	for _, b := range builtins {
		if b.Hidden {
			continue
		}
		sc := tui.SlashCommand{Name: b.Name, Description: b.Description, ArgumentHint: b.ArgumentHint}
		if b.Name == "model" {
			sc.GetArgumentCompletions = m.modelArgCompletions
		}
		cmds = append(cmds, sc)
	}

	// Add prompt templates as slash commands in autocomplete.
	for _, pt := range promptTemplates {
		desc := pt.Description
		if pt.Scope != "" {
			desc = "[" + pt.Scope + "] " + desc
		}
		sc := tui.SlashCommand{
			Name:         pt.Name,
			Description:  desc,
			ArgumentHint: pt.ArgumentHint,
		}
		cmds = append(cmds, sc)
	}

	// Add skill commands as /skill:name entries (when enabled in settings).
	skillCommandsEnabled := m.opts.SettingsManager == nil || m.settings().GetEnableSkillCommands()
	if skillCommandsEnabled {
		for _, s := range m.opts.Skills {
			cmds = append(cmds, tui.SlashCommand{
				Name:        "skill:" + s.Name,
				Description: s.Description,
			})
		}
	}

	// Detect fd for fuzzy file search. The pre-warm at startup already populated
	// <agentDir>/bin/fd when needed; LookupToolPath prefers that over PATH.
	fdPath := tools.LookupToolPath("fd", filepath.Join(m.opts.AgentDir, "bin"))

	cwd, _ := os.Getwd()
	prov := tui.NewCombinedProvider(cmds, cwd, fdPath)
	// Defer the fd subprocess off the keystroke path; only meaningful when
	// fd is present.
	prov.SetAsyncFileSearch(true)
	return prov
}

// modelArgCompletions fuzzy-filters the available runtime snapshot using getModelSearchText.
func (m *InteractiveMode) modelArgCompletions(prefix string) []tui.AutocompleteItem {
	items := m.availableModelItems()
	filtered := tui.FuzzyFilter(items, prefix, func(item tui.ModelSelectorItem) string {
		return tui.GetModelSearchText(tui.ModelSearchItem{ID: item.ID, Provider: item.Provider, Name: item.Name})
	})
	out := make([]tui.AutocompleteItem, 0, len(filtered))
	for _, item := range filtered {
		out = append(out, tui.AutocompleteItem{Value: item.FQ(), Label: item.ID, Description: item.Provider})
	}
	return out
}

func (m *InteractiveMode) resolveAvailableModel(input string) (string, bool) {
	needle := strings.TrimSpace(strings.ToLower(input))
	if needle == "" {
		return "", false
	}
	bareID := ""
	for _, mm := range m.availableModelItems() {
		fq := mm.FQ()
		if strings.EqualFold(fq, needle) {
			return fq, true
		}
		if strings.EqualFold(mm.ID, needle) {
			if bareID != "" {
				return "", false
			}
			bareID = fq
		}
	}
	if bareID != "" {
		return bareID, true
	}
	return "", false
}

// shareState is the system prompt and active tools the wopr.share entry
// records.
func (m *InteractiveMode) shareState() ShareState {
	var activeTools []agent.AgentTool
	if m.agent != nil {
		activeTools = m.agent.Tools()
	}
	return NewShareState(m.currentSystemPrompt(), activeTools)
}

// newSession starts a fresh session file and clears the transcript.
func (m *InteractiveMode) newSession() error {
	m.clearStatusIndicator("")
	if err := m.settleActiveRun(); err != nil {
		return err
	}
	sessID := generateSessionID()
	newSess, err := m.newSessionManager().Create(sessID, "")
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	// The first entries record the model and thinking level.
	if model := m.opts.Model; model != nil {
		provID := ""
		if model.Provider != nil {
			provID = model.Provider.ID()
		}
		if err := newSess.AppendModelSwitch(provID, model.ID, model.DisplayName); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
	}
	if err := newSess.AppendThinkingLevelChange(DefaultThinkingLevel); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	m.replaceSession(newSess)
	m.restoreSidebar()
	if m.agent != nil {
		m.agent.SetMessages(nil)
	}
	m.chatContainer.Clear()
	m.tuiInst.Render()
	return nil
}

// replaceSession makes s the live session. ReplaceInner redirects both the
// displayed session and the agent's persistence hook, so later turns record
// into s.
func (m *InteractiveMode) replaceSession(s *Session) {
	if m.opts.SessionHandle != nil {
		m.opts.SessionHandle.ReplaceInner(s)
	}
}

// syncSessionModel shows the model, thinking level, and routing the session
// restored on resume, and makes them the choice the next start restores.
func (m *InteractiveMode) syncSessionModel() {
	if handle, ok := m.opts.SessionHandle.(interface{ Model() *ai.Model }); ok {
		if model := handle.Model(); model != nil {
			m.opts.Model = model
			m.statusLine.SetModel(model)
		}
	}
	m.refreshFooterContextUsage()
	m.refreshThinkingLevel()
	m.saveRouting()
	m.updateProviderInfo()
}

// switchModel makes spec the orchestrator model. Picking a model takes the
// choice away from the router; its mode still governs subagents.
func (m *InteractiveMode) switchModel(spec string) error {
	m.pinOrchestrator()
	if m.opts.ModelBuilder == nil {
		return errors.New("model switching is not configured (no ModelBuilder)")
	}
	newModel, err := m.opts.ModelBuilder(spec)
	if err != nil {
		return err
	}
	if m.opts.SessionHandle != nil {
		if err := m.opts.SessionHandle.SetModel(newModel); err != nil {
			return err
		}
	} else if m.agent != nil {
		m.agent.SetModel(newModel)
	}
	m.opts.Model = newModel
	m.statusLine.SetModel(newModel)
	m.refreshFooterContextUsage()
	m.refreshThinkingLevel()
	m.saveRouting()
	m.tuiInst.Render()
	return nil
}

// loadSessionPath resumes the session file at path.
func (m *InteractiveMode) loadSessionPath(path string) error {
	m.clearStatusIndicator("")
	loaded, err := m.newSessionManager().Load(path)
	if err != nil {
		return err
	}
	if err := m.settleActiveRun(); err != nil {
		return err
	}
	if m.agent != nil {
		m.agent.SetMessages(loaded.BuildContext(nil))
	}
	m.replaceSession(loaded)
	m.syncSessionModel()
	m.restoreSidebar()
	m.rebuildChatFromSession()
	m.offerRedispatch()
	if name := loaded.GetSessionName(); name != "" {
		m.statusLine.SetName(name)
	}
	return nil
}

// forkToNewSession branches the selected user message into a new session
// file (the branch up to its parent), switches to it, and puts the message
// in the editor.
func (m *InteractiveMode) forkToNewSession(userMsgEntryID string) error {
	sess := m.currentSession()
	if sess == nil {
		return errors.New("no active session")
	}
	if err := m.settleActiveRun(); err != nil {
		return err
	}
	newSess, selectedText, err := m.newSessionManager().ForkToNewSession(sess, userMsgEntryID)
	if err != nil {
		return err
	}
	m.replaceSession(newSess)
	m.rebuildChatFromSession()
	m.editor.SetText(selectedText)
	return nil
}

// cloneCurrent copies the current branch into a new session file and
// switches to it.
func (m *InteractiveMode) cloneCurrent() (string, error) {
	sess := m.currentSession()
	if sess == nil {
		return "", errors.New("no active session")
	}
	leaf := sess.LeafID()
	if leaf == nil {
		return "", errors.New("session is empty: nothing to clone")
	}
	if err := m.settleActiveRun(); err != nil {
		return "", err
	}
	clone, err := m.newSessionManager().Clone(sess, *leaf)
	if err != nil {
		return "", err
	}
	m.replaceSession(clone)
	return clone.Path(), nil
}

// pickUserMessage picks one of the branch's user messages in the editor
// slot and returns its entry id.
func (m *InteractiveMode) pickUserMessage() (string, bool) {
	sess := m.currentSession()
	if sess == nil {
		return "", false
	}
	ids, labels := userMessageSelectorItems(sess)
	if len(ids) == 0 {
		m.statusLine.Flash("No messages to fork from")
		return "", false
	}
	sel := tui.NewUserMessageSelector(labels)
	if !m.runInSlot(modalOf(sel)) || sel.Cancelled() {
		return "", false
	}
	idx := sel.SelectedIndex()
	if idx < 0 || idx >= len(ids) {
		return "", false
	}
	return ids[idx], true
}

// pickTreeEntry shows the session tree in the editor slot, opened at the
// current leaf or initialSelectedID; shift+L edits a label.
func (m *InteractiveMode) pickTreeEntry(initialSelectedID string) (string, bool) {
	sess := m.currentSession()
	if sess == nil {
		return "", false
	}
	root := sess.Tree()
	if root == nil {
		return "", false
	}
	ts := tui.NewTreeSelect("Session tree", &treeNodeAdapter{n: root, f: newTreeRowFormatter(sess)})
	ts.MaxVisibleLines = tui.TreeVisibleLines(m.tuiInst.Height())
	currentLeafID := ""
	if leaf := sess.LeafID(); leaf != nil {
		currentLeafID = *leaf
	}
	ts.SetInitialCursor(currentLeafID, initialSelectedID)
	ts.OnLabelEdit = func(entryID, label string) {
		var lp *string
		if label != "" {
			lp = &label
		}
		if err := sess.AppendLabelChange(entryID, lp); err != nil {
			m.showError(err.Error())
		}
	}
	if !m.runInSlot(modalOf(ts)) || ts.Cancelled() {
		return "", false
	}
	return ts.SelectedID(), true
}

// selectInSlot shows a bordered choice list (no filter) with an optional
// description in the editor slot and returns the chosen index.
func (m *InteractiveMode) selectInSlot(title string, options []string, description string) (int, bool) {
	sel := tui.NewSlotSelector(title, options)
	sel.SetDescription(description)
	idx, ok := m.runSlotSelector(sel)
	if !ok || idx < 0 || idx >= len(options) {
		return -1, false
	}
	return idx, true
}

// editInSlot shows a bordered text editor with an optional description and
// prefill in the editor slot.
func (m *InteractiveMode) editInSlot(title, description, prefill string) (string, bool) {
	ed := tui.NewSlotEditorComponent(title, prefill)
	ed.SetDescription(description)
	if !m.runInSlot(modalOf(ed)) || ed.Cancelled() {
		return "", false
	}
	return ed.Value(), true
}

// reloadResources re-reads settings, keybindings, prompt templates, context
// files, skills, tools, and themes, then rebuilds the transcript with them.
// Failures go to m.reloadIssues for /reload to show.
func (m *InteractiveMode) reloadResources() {
	m.reloadIssues = nil
	if m.opts.SettingsManager != nil {
		m.opts.SettingsManager.Reload()
	}
	if err := m.keybindings.Reload(); err != nil {
		m.reloadIssues = append(m.reloadIssues, "[keybindings] "+err.Error())
	}
	if m.opts.ReloadResourceProvider != nil {
		m.applyReloadResourceSnapshot(m.opts.ReloadResourceProvider())
	}
	if m.opts.NoPromptTemplates {
		m.promptTemplates = nil
	} else {
		m.loadPromptTemplates()
	}
	m.reloadSkillsFromPaths()
	m.rebuildSystemPromptFromResources()
	m.refreshAgentTools()

	// The rebuilt transcript uses the reloaded display settings.
	m.hideThinking = m.settings().GetHideThinkingBlock()
	m.outputPad = m.settings().GetOutputPad()
	m.rebuildChatFromSession()
	// Prompt conflicts show once, after the rebuild, so the block survives it.
	if !m.opts.NoPromptTemplates {
		m.showPromptDiagnostics()
	}

	// The theme registry reloads without re-applying the active theme.
	if !m.opts.NoThemes {
		registry := tui.ActiveThemeRegistry()
		themesDir := filepath.Join(m.opts.AgentDir, "themes")
		if _, err := os.Stat(themesDir); err == nil {
			if err := registry.LoadDir(themesDir); err != nil {
				m.reloadIssues = append(m.reloadIssues, "[theme] "+err.Error())
			}
		}
		for _, themePath := range m.opts.ThemePaths {
			if err := loadThemePath(registry, themePath); err != nil {
				m.reloadIssues = append(m.reloadIssues, "[theme] "+err.Error())
			}
		}
	}
	tui.SetCapabilityOverrides(m.settings().GetTerminalCapabilityOverrides())
	tui.RefreshActiveThemeColorMode()
	// Skills and prompts may have changed.
	m.editor.SetAutocomplete(m.buildAutocompleteProvider())
}

// applySetting applies a setting /settings just saved to the running
// session.
func (m *InteractiveMode) applySetting(id, value string) {
	switch id {
	case "skill-commands":
		m.editor.SetAutocomplete(m.buildAutocompleteProvider())
	case "transport":
		m.agent.SetTransport(ai.Transport(value))
	case "cache-miss-notices":
		m.rebuildChatFromSession()
	case "http-idle-timeout":
		if timeoutMs, err := strconv.Atoi(value); err == nil {
			_ = ai.ConfigureHTTPIdleTimeout(timeoutMs)
		}
	case "cache-warming-mode":
		if m.opts.SessionHandle != nil {
			_ = m.opts.SessionHandle.SetCacheWarmingMode(CacheWarmingMode(value))
		}
	case "hide-thinking":
		// toggleThinkingVisibility flips, repaints, and saves: use it only
		// when the live state differs.
		if m.hideThinking != (value == "true") {
			m.toggleThinkingVisibility()
		}
	case "autocompact":
		// The session reads compaction settings on demand; only the
		// indicator needs updating.
		m.statusLine.SetAutoCompactEnabled(value == "true")
	case "theme":
		tui.SetThemeSetting(value)
		m.tuiInst.ForceFullRender()
	case "show-hardware-cursor":
		m.tuiInst.SetShowHardwareCursor(value == "true")
	case "fullscreen-scrollbar":
		if m.transcriptScrollView != nil {
			m.transcriptScrollView.SetScrollbar(value)
		}
	case "fullscreen-copy-on-select":
		m.tuiInst.SetCopyOnSelect(value == "true")
	case "fullscreen-scroll-speed":
		if lines, err := strconv.Atoi(value); err == nil {
			m.tuiInst.SetWheelScrollLines(lines)
		}
	case "editor-padding":
		if padding, err := strconv.Atoi(value); err == nil {
			m.editor.SetPaddingX(padding)
		}
	case "output-padding":
		padding, err := strconv.Atoi(value)
		if err != nil {
			return
		}
		m.outputPad = max(0, min(1, padding))
		if !m.agent.IsStreaming() {
			m.rebuildChatFromSession()
			return
		}
		for _, block := range m.assistantBlocks {
			block.SetOutputPad(m.outputPad)
		}
		for _, component := range m.customMessageOrder {
			if padded, ok := component.(interface{ SetOutputPad(int) }); ok {
				padded.SetOutputPad(m.outputPad)
			}
		}
		m.tuiInst.RequestRender()
	case "autocomplete-max-visible":
		if maxVisible, err := strconv.Atoi(value); err == nil {
			m.editor.SetAutocompleteMaxVisible(maxVisible)
		}
	case "steering-mode":
		m.agent.SetSteeringMode(agent.QueueMode(value))
	case "follow-up-mode":
		m.agent.SetFollowUpMode(agent.QueueMode(value))
	case "show-images", "image-width-cells", "auto-resize-images", "block-images":
		s := m.settings()
		show := s.GetShowImages() && !s.GetBlockImages()
		width := s.GetImageWidthCells()
		m.toolMu.Lock()
		for _, comp := range m.toolOrder {
			comp.SetShowImages(show)
			comp.SetImageWidthCells(width)
		}
		m.toolMu.Unlock()
	}
}

// oauthProviderNames labels the subscription providers; API-key providers take
// their names from ai.APIKeyProviders.
var oauthProviderNames = map[string]string{
	"anthropic":      "Anthropic",
	"github-copilot": "GitHub Copilot",
	"openai-codex":   ai.OpenAICodexOAuthDisplayName,
}

// buildAuthProviderName returns the user-facing provider label used by /login and /logout.
func buildAuthProviderName(provider string) string {
	if name, ok := oauthProviderNames[provider]; ok {
		return name
	}
	for _, p := range ai.APIKeyProviders() {
		if p.ID == provider {
			return p.Name
		}
	}
	return provider
}
