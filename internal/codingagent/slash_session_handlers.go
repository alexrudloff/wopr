package codingagent

// The interactive slash commands. Each runs on the owner loop and may open
// dialogs or take over the editor slot.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/export"
	"github.com/alexrudloff/wopr/tui"

	"github.com/alexrudloff/wopr/internal/codingagent/sessionblob"
)

// appendMarkdown and appendPlain add slash command output to the
// transcript.
func (m *InteractiveMode) appendMarkdown(s string) { m.appendToChat(tui.NewMarkdown(s)) }
func (m *InteractiveMode) appendPlain(s string) {
	m.appendToChat(tui.NewPaddedText(s, statusIndent, 0, nil))
}

func (m *InteractiveMode) modelCommand(args string) error {
	spec := args
	if spec == "" {
		m.modelMenu()
		return nil
	} else if _, isMode := modeSpec(args); isMode {
		spec = args
	} else if resolved, ok := m.resolveAvailableModel(args); ok {
		spec = resolved
	} else if spec, ok = m.pickModelDialog(args); !ok {
		return nil
	}
	// The picker lists routing modes too, and /model <mode> names one.
	if o, ok := modeSpec(spec); ok {
		m.selectRoutingMode(o, true)
		return nil
	}
	if err := m.switchModel(spec); err != nil {
		return fmt.Errorf("model switch to %q failed: %w", spec, err)
	}
	m.showModelSelection(spec)
	return nil
}

// showModelSelection reports the switch with the model's bare id.
func (m *InteractiveMode) showModelSelection(spec string) {
	_, bareID, found := strings.Cut(spec, "/")
	if !found {
		bareID = spec
	}
	m.showStatus("Model: " + bareID)
}

// thinkingCommand opens the thinking selector, or with an argument selects
// the matching available level, ignoring case.
func (m *InteractiveMode) thinkingCommand(args string) error {
	levels := levelsForModel(m.opts.Model)
	if search := strings.TrimSpace(args); search != "" {
		for _, level := range levels {
			if strings.EqualFold(level, search) {
				m.selectThinkingLevel(level, false)
				return nil
			}
		}
		return fmt.Errorf("Unknown thinking level %q. Available levels: %s.", search, strings.Join(levels, ", "))
	}
	m.showThinkingSelector()
	return nil
}

func (m *InteractiveMode) copyCommand(string) error {
	text := m.lastAssistantText
	if text == "" {
		return errors.New("No agent messages to copy yet.")
	}
	if err := copyToClipboard(text); err != nil {
		return err
	}
	m.showStatus("Copied last agent message to clipboard")
	return nil
}

func (m *InteractiveMode) sessionCommand(string) error {
	var status *CacheWarmingStatus
	if m.opts.SessionHandle != nil {
		status = m.opts.SessionHandle.CacheWarmingStatus()
	}
	mode := defaultCacheWarmingMode
	if m.opts.SettingsManager != nil {
		mode = m.opts.SettingsManager.GetCacheWarmingMode()
	}
	m.appendPlain(sessionInfoText(m.currentSession(), mode, status))
	return nil
}

func (m *InteractiveMode) nameCommand(args string) error {
	if args == "" {
		m.appendMarkdown(sessionNameText(m.currentSession()))
		return nil
	}
	if err := m.setSessionName(args); err != nil {
		return err
	}
	m.appendMarkdown("Session name set: " + args)
	return nil
}

// setSessionName records name in the session and shows it in the terminal
// title and the status line.
func (m *InteractiveMode) setSessionName(name string) error {
	session := m.currentSession()
	if session == nil {
		return errors.New("no active session")
	}
	if _, err := appendOnLeaf(session, "session_info", func(base SessionEntryBase) SessionInfoEntry {
		return SessionInfoEntry{SessionEntryBase: base, Name: name}
	}); err != nil {
		return err
	}
	tui.SetTerminalTitle(tui.BuildTerminalTitle(name, m.opts.CWD))
	m.statusLine.SetName(name)
	return nil
}

// debugCommand backs /debug and ctrl+shift+d: it writes a debug log (the
// rendered frame and the message JSONL) and shows its path.
func (m *InteractiveMode) debugCommand(string) error {
	path, err := m.writeDebugLog()
	if err != nil {
		return err
	}
	m.appendMarkdown("\033[32m✓ Debug log written\033[0m")
	m.appendMarkdown("\033[2m" + path + "\033[0m")
	return nil
}

func (m *InteractiveMode) forkCommand(args string) error {
	id := args
	if id == "" {
		picked, ok := m.pickUserMessage()
		if !ok {
			m.appendMarkdown("Fork cancelled.")
			return nil
		}
		id = picked
	}
	if err := m.forkToNewSession(id); err != nil {
		return err
	}
	m.showStatus("Forked to new session")
	return nil
}

func (m *InteractiveMode) cloneCommand(string) error {
	path, err := m.cloneCurrent()
	if err != nil {
		return err
	}
	m.appendMarkdown(fmt.Sprintf("Cloned current path to:\n  %s\n\nThis session has been switched to the new file.", path))
	return nil
}

func (m *InteractiveMode) resumeCommand(string) error {
	path, ok := m.pickSessionDialog(m.newSessionManager())
	if !ok {
		m.showStatus("Resume cancelled.")
		return nil
	}
	if err := m.loadSessionPath(path); err != nil {
		return m.handleFatalRuntimeError("Failed to resume session", err)
	}
	m.showStatus("↻ Resumed session from " + sessionStartLabel(path, time.Now()))
	return nil
}

func (m *InteractiveMode) newCommand(string) error {
	if err := m.newSession(); err != nil {
		return m.handleFatalRuntimeError("Failed to create session", err)
	}
	return nil
}

func (m *InteractiveMode) routerCommand(args string) error {
	rc, ok := m.opts.SessionHandle.(RouterController)
	if !ok {
		return errors.New("model routing is not available")
	}
	out, err := rc.RouterCommand(args)
	if err != nil {
		return err
	}
	if verb, _, _ := strings.Cut(strings.TrimSpace(args), " "); verb == "on" || verb == "off" {
		m.saveRouting()
	}
	m.appendMarkdown(out)
	return nil
}

func (m *InteractiveMode) mcpCommand(args string) error {
	mc, ok := m.opts.SessionHandle.(MCPController)
	if !ok {
		return errors.New("MCP is not available")
	}
	out, err := mc.MCPCommand(args)
	if err != nil {
		return err
	}
	m.appendMarkdown(out)
	return nil
}

func (m *InteractiveMode) llamaCommand(string) error {
	if m.opts.Llama == nil {
		m.appendMarkdown("llama.cpp is not available in this context.")
		return nil
	}
	return m.runLlamaCommand(m.runCtx)
}

func (m *InteractiveMode) hotkeysCommand(string) error {
	m.appendMarkdown(hotkeysText(m.keybindings.HotkeyLines()))
	return nil
}

// trustCommand saves a project-trust decision to <agentDir>/trust.json; it
// takes effect on the next start.
func (m *InteractiveMode) trustCommand(string) error {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	options := GetProjectTrustOptions(cwd, false)
	labels := make([]string, len(options))
	for i, o := range options {
		labels[i] = o.Label
	}
	idx, ok := m.selectInSlot("Project trust ("+cwd+")", labels, "")
	if !ok {
		return nil
	}
	selected := options[idx]
	if len(selected.Updates) > 0 {
		if err := NewProjectTrustStore(m.opts.AgentDir).SetMany(selected.Updates); err != nil {
			m.appendMarkdown("Failed to save trust decision: " + err.Error())
			return nil
		}
	}
	state := "untrusted"
	if selected.Trusted {
		state = "trusted"
	}
	m.showStatus("Saved trust decision: " + state + ". Restart " + AppName + " for this to take effect.")
	return nil
}

// treeCommand opens the session tree at initialSelectedID (the current leaf
// when empty) and navigates to the chosen entry, offering a branch summary.
func (m *InteractiveMode) treeCommand(initialSelectedID string) error {
	session := m.currentSession()
	if session != nil && len(session.Entries()) == 0 {
		m.showStatus("No entries in session")
		return nil
	}
	id, ok := m.pickTreeEntry(initialSelectedID)
	if !ok {
		return nil
	}
	if session != nil {
		if leaf := session.LeafID(); leaf != nil && *leaf == id {
			m.showStatus("Already at this point")
			return nil
		}
	}

	if m.settings().GetBranchSummarySettings().SkipPrompt {
		result, err := m.navigateTree(context.Background(), id, false, "")
		if err != nil {
			m.showStatus(fmt.Sprintf("Navigation error: %v", err))
			return nil
		}
		m.prefillEditor(result.EditorText)
		if !result.Cancelled && !result.Aborted {
			m.navigated()
		}
		return nil
	}

	const (
		optNoSummary       = "No summary"
		optSummarize       = "Summarize"
		optCustomSummarize = "Summarize with custom prompt"
	)
	options := []string{optNoSummary, optSummarize, optCustomSummarize}
	wantsSummary, customInstructions := false, ""
	for {
		choice, ok := m.selectInSlot("Summarize branch?", options, "")
		if !ok {
			// Esc reopens the tree at the same row.
			return m.treeCommand(id)
		}
		wantsSummary = options[choice] != optNoSummary
		if options[choice] == optCustomSummarize {
			instructions, ok := m.editInSlot("Custom summarization instructions", "", "")
			if !ok {
				continue
			}
			customInstructions = instructions
		}
		break
	}

	result, err := m.navigateTree(context.Background(), id, wantsSummary, customInstructions)
	if err != nil {
		return err
	}
	switch {
	case result.Aborted:
		m.showStatus("Branch summarization cancelled")
		return m.treeCommand(id)
	case result.Cancelled:
		m.showStatus("Navigation cancelled")
		return nil
	}
	m.prefillEditor(result.EditorText)
	m.navigated()
	return nil
}

// prefillEditor puts text in an empty editor.
func (m *InteractiveMode) prefillEditor(text string) {
	if text != "" && strings.TrimSpace(m.editor.Text()) == "" {
		m.editor.SetText(text)
	}
}

// navigated reports a tree navigation and sends the messages queued during
// a compaction now that the new leaf is in place. Navigation runs on the
// input loop, so the flush and any turn it starts stay single-threaded with
// editor state.
func (m *InteractiveMode) navigated() {
	m.showStatus("Navigated to selected point")
	m.flushCompactionQueue(m.runCtx, true)
}

// renderTreeASCII walks a SessionTreeNode and produces an indented
// textual representation. Each node is one line; columns: id (8 chars),
// timestamp HH:MM:SS, role, first 60 chars of text. Branch glyphs
// (`├─`, `└─`, `│`) follow the standard tree style.
func renderTreeASCII(root *SessionTreeNode) string {
	if root == nil || len(root.Children) == 0 {
		return ""
	}
	var b strings.Builder
	for i, c := range root.Children {
		writeTreeNode(&b, c, "", i == len(root.Children)-1)
	}
	return b.String()
}

func writeTreeNode(b *strings.Builder, n *SessionTreeNode, prefix string, last bool) {
	connector := "├─ "
	childPrefix := prefix + "│  "
	if last {
		connector = "└─ "
		childPrefix = prefix + "   "
	}
	id := n.Entry.Base.ID
	if len(id) > 8 {
		id = id[:8]
	}
	role := "?"
	text := ""
	if me, ok := n.Entry.AsMessage(); ok {
		role = me.Message.Role()
		text = extractMessageText(me)
	} else if n.Entry.Base.Type != "message" {
		role = n.Entry.Base.Type
	}
	if len(text) > 60 {
		text = text[:60] + "…"
	}
	text = strings.ReplaceAll(text, "\n", " ")
	ts := n.Entry.Base.Timestamp
	if len(ts) >= 19 {
		ts = ts[11:19]
	}
	label := ""
	if n.Label != "" {
		label = " [" + n.Label + "]"
	}
	fmt.Fprintf(b, "%s%s%s · %s · %s%s · %s\n", prefix, connector, id, ts, role, label, text)
	for i, c := range n.Children {
		writeTreeNode(b, c, childPrefix, i == len(n.Children)-1)
	}
}

// compactCommand starts a manual compaction with optional custom
// instructions. It needs at least two messages in the session file: the live
// context of a long, already-compacted session can be small. Compaction
// events report progress and errors.
func (m *InteractiveMode) compactCommand(args string) error {
	messages := 0
	if s := m.currentSession(); s != nil {
		for _, e := range s.Entries() {
			if e.Base.Type == "message" {
				messages++
			}
		}
	}
	if messages < 2 {
		m.showWarning("Nothing to compact (no messages yet)")
		return nil
	}
	type compactor interface {
		Compact(ctx context.Context, customInstructions string) error
	}
	if c, ok := m.opts.SessionHandle.(compactor); ok {
		ctx := m.runCtx
		go func() { _ = c.Compact(ctx, args) }()
	}
	return nil
}

func (m *InteractiveMode) reloadCommand(args string) error {
	m.reloadResources()
	explain := reloadExplainRequested(args)
	var summary strings.Builder
	summary.WriteString("Reloaded keybindings, skills, prompts, themes, and context files")
	if issues := append(m.resourceCollisionDiagnostics(), m.reloadIssues...); len(issues) > 0 {
		summary.WriteString("\nReload issues:")
		for _, d := range issues {
			summary.WriteString("\n  • " + d)
		}
	}
	if explain {
		summary.WriteString("\nReload explanation:")
		fmt.Fprintf(&summary, "\n  • context files: %d", len(m.opts.ContextFiles))
		fmt.Fprintf(&summary, "\n  • skills: %d", len(m.opts.Skills))
		fmt.Fprintf(&summary, "\n  • prompts: %d", len(m.promptTemplates))
		fmt.Fprintf(&summary, "\n  • themes: %d", len(tui.ActiveThemeRegistry().Names()))
	}
	m.showStatus(summary.String())
	return nil
}

func reloadExplainRequested(args string) bool {
	for field := range strings.FieldsSeq(args) {
		if field == "--explain" || field == "explain" {
			return true
		}
	}
	return false
}

func (m *InteractiveMode) exportCommand(args string) error {
	message, err := exportSession(m.currentSession(), args)
	if err != nil {
		return err
	}
	m.showStatus(message)
	return nil
}

// exportSession writes the session's current branch to arg: a .jsonl path
// writes a standalone session, anything else HTML (default
// session-<timestamp>.html in the working directory). It returns the
// message to show.
func exportSession(s *Session, arg string) (string, error) {
	if s == nil {
		return "No active session.", nil
	}
	if strings.HasSuffix(arg, ".jsonl") {
		filePath, err := ExportSessionToJsonl(s, arg, nil)
		if err != nil {
			return "", fmt.Errorf("Failed to export session: %w", err)
		}
		return "Session exported to: " + filePath, nil
	}
	if s.Path() == "" {
		return "Session has no file path (in-memory session).", nil
	}
	dst := arg
	switch {
	case arg == "":
		dst = filepath.Join(".", fmt.Sprintf("session-%d.html", time.Now().UnixMilli()))
	case !strings.HasSuffix(arg, ".html"):
		dst = arg + ".html"
	}
	data, err := os.ReadFile(s.Path())
	if err != nil {
		return fmt.Sprintf("Export failed: %v", err), nil
	}
	sd, err := export.FromJSONL(sessionblob.ResolveFile(s.Path(), data))
	if err != nil {
		return fmt.Sprintf("Export parse failed: %v", err), nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Sprintf("Export failed: %v", err), nil
	}
	if err := os.WriteFile(dst, []byte(export.ToHTML(sd)), 0o644); err != nil {
		return fmt.Sprintf("Export failed: %v", err), nil
	}
	return "Session exported to: " + dst, nil
}

// importCommand copies a .jsonl session into the session directory and
// resumes it.
func (m *InteractiveMode) importCommand(args string) error {
	if args == "" {
		m.appendMarkdown("Usage: /import <path.jsonl>")
		return nil
	}
	if !strings.HasSuffix(args, ".jsonl") {
		m.appendMarkdown("Only .jsonl files can be imported.")
		return nil
	}
	session := m.currentSession()
	srcPath := args
	if !filepath.IsAbs(srcPath) && session != nil && session.CWD() != "" {
		srcPath = filepath.Join(session.CWD(), srcPath)
	}
	if _, err := os.Stat(srcPath); err != nil {
		m.appendMarkdown("File not found: " + srcPath)
		return nil
	}
	dstPath := srcPath
	if session != nil && session.Path() != "" {
		dstPath = filepath.Join(filepath.Dir(session.Path()), filepath.Base(srcPath))
	}
	if dstPath != srcPath {
		// Relocate carries the session's images into this directory's store.
		if err := sessionblob.Relocate(srcPath, dstPath); err != nil {
			m.appendMarkdown(fmt.Sprintf("Import failed (copy): %v", err))
			return nil
		}
	}
	if err := m.loadSessionPath(dstPath); err != nil {
		return m.handleFatalRuntimeError("Failed to import session", err)
	}
	m.chatContainer.Clear()
	m.showStatus("Session imported from: " + args)
	return nil
}

// shareSessionCommand publishes the active branch as a secret GitHub gist. The
// command is the explicit opt-in; the privacy notice shows before the
// upload starts.
func (m *InteractiveMode) shareSessionCommand(string) error {
	session := m.currentSession()
	if session == nil {
		m.appendMarkdown("No active session.")
		return nil
	}
	status, err := m.shareSessionWithLoader(m.runCtx, session, m.showStatus)
	if errors.Is(err, errShareCancelled) {
		m.showStatus("Share cancelled")
		return nil
	}
	if err != nil {
		return err
	}
	// Consecutive status lines coalesce, so the result is its own row to
	// keep the privacy notice readable.
	m.appendPlain(status)
	return nil
}

// loginCommand configures a provider: an account (OAuth) or an API key.
func (m *InteractiveMode) loginCommand(string) error {
	methods := []string{"Sign in with an account", "Sign in with an API key"}
	idx, ok := m.runSlotSelector(tui.NewSlotSelector("Select authentication method:", methods))
	if !ok {
		return nil
	}
	if idx == 1 {
		provider, ok := m.pickOAuthProvider("login-api-key")
		if !ok || m.opts.Llama != nil && m.loginAPIKeyProvider(provider) {
			return nil
		}
		input := tui.NewSlotInputComponent("Enter API key", "sk-...")
		if !m.runInSlot(modalOf(input)) || input.Cancelled() {
			return nil
		}
		if err := m.setAPIKey(provider, strings.TrimSpace(input.Text())); err != nil {
			m.appendMarkdown(fmt.Sprintf("Login failed: %v", err))
		}
		return nil
	}
	provider, ok := m.pickOAuthProvider("login-oauth")
	if !ok {
		return nil
	}
	var err error
	if provider == "github-copilot" {
		err = m.runLoginGitHubCopilotDialog(context.Background())
	} else {
		err = m.runOAuthLogin(context.Background(), provider)
	}
	if err != nil {
		m.appendMarkdown(fmt.Sprintf("Login failed: %v", err))
	}
	return nil
}

// setAPIKey stores provider's API key in auth.json.
func (m *InteractiveMode) setAPIKey(provider, value string) error {
	auth, err := ai.NewAuthStorage(filepath.Join(m.opts.AgentDir, "auth.json"))
	if err != nil {
		return fmt.Errorf("auth storage: %w", err)
	}
	if err := auth.Set(provider, ai.Credential{Type: ai.CredentialAPIKey, Key: value}); err != nil {
		return err
	}
	if m.opts.ModelRegistry != nil {
		m.opts.ModelRegistry.Refresh()
	}
	m.updateProviderInfo()
	m.showStatus(fmt.Sprintf("Configured API key for %s", buildAuthProviderName(provider)))
	return nil
}

func (m *InteractiveMode) logoutCommand(string) error {
	provider, ok := m.pickOAuthProvider("logout")
	if !ok {
		return nil
	}
	if err := m.runOAuthLogout(provider); err != nil {
		m.appendMarkdown(fmt.Sprintf("Logout failed: %v", err))
		return nil
	}
	m.showStatus("Logged out of " + buildAuthProviderName(provider))
	return nil
}

// pickOAuthProvider runs the provider picker for mode ("login-oauth",
// "login-api-key", or "logout").
func (m *InteractiveMode) pickOAuthProvider(mode string) (string, bool) {
	sel := tui.NewOAuthSelector(mode, m.oauthProviderList(mode))
	if !m.runInSlot(modalOf(sel)) || sel.Cancelled() {
		return "", false
	}
	return sel.SelectedID(), true
}

// ─── /settings handler ───────────────────────────────────────────

// settingItem describes one toggle-able setting for the /settings selector.
type settingItem struct {
	id    string
	label string
	desc  string
	// get returns the current display value.
	get func(s Settings) string
	// values is the ordered list of valid values to cycle through.
	values []string
	// apply writes the chosen value into the settings struct.
	apply func(s *Settings, val string)
	// gated, when non-nil, returns false to hide this item from /settings
	// when the current terminal lacks the relevant capability.
	gated func() bool
}

type httpIdleTimeoutChoice struct {
	label     string
	timeoutMs int
}

var httpIdleTimeoutChoices = []httpIdleTimeoutChoice{
	{label: "30 sec", timeoutMs: 30_000},
	{label: "1 min", timeoutMs: 60_000},
	{label: "2 min", timeoutMs: 120_000},
	{label: "5 min", timeoutMs: 300_000},
	{label: "disabled", timeoutMs: 0},
}

func formatHTTPIdleTimeoutMs(timeoutMs int) string {
	for _, choice := range httpIdleTimeoutChoices {
		if choice.timeoutMs == timeoutMs {
			return choice.label
		}
	}
	return strconv.FormatFloat(float64(timeoutMs)/1000, 'f', -1, 64) + " sec"
}

func parseHTTPIdleTimeoutLabel(value string) (int, bool) {
	for _, choice := range httpIdleTimeoutChoices {
		if choice.label == value {
			return choice.timeoutMs, true
		}
	}
	return 0, false
}

// settingsItemsVisible returns settingsItems filtered by capability gates.
// show-images and image-width-cells appear only when the terminal supports
// images.
func settingsItemsVisible() []settingItem {
	return slices.DeleteFunc(settingsItems(), func(item settingItem) bool { return item.gated != nil && !item.gated() })
}

// boolSetting is a true/false item: get reads the effective value and field
// returns the setting to write, creating any section it lives in.
func boolSetting(id, label, desc string, get func(Settings) bool, field func(*Settings) **bool) settingItem {
	return settingItem{
		id: id, label: label, desc: desc,
		values: []string{"true", "false"},
		get:    func(s Settings) string { return strconv.FormatBool(get(s)) },
		apply:  func(s *Settings, v string) { *field(s) = new(v == "true") },
	}
}

// imagesSupported gates the image items on terminal image support.
func imagesSupported() bool { return tui.Capabilities().Images != "" }

// gatedOnImages shows item only where the terminal supports images.
func gatedOnImages(item settingItem) settingItem {
	item.gated = imagesSupported
	return item
}

// settingsItems returns the list of toggle-able settings.
func settingsItems() []settingItem {
	return []settingItem{
		boolSetting("autocompact", "Auto-compact", "Automatically compact context when it gets too large",
			func(s Settings) bool { return s.Compaction == nil || boolOr(s.Compaction.Enabled, true) },
			func(s *Settings) **bool {
				if s.Compaction == nil {
					s.Compaction = &CompactionSettingsJSON{}
				}
				return &s.Compaction.Enabled
			}),
		{
			id: "compact-at", label: "Compact at",
			desc:   "Compact once the context passes this many tokens, even on a larger window; 'window' waits until the model's window is nearly full",
			values: []string{"100k", "200k", "400k", "window"},
			get: func(s Settings) string {
				n := s.GetModelCompactionSettings("", "").MaxContextTokens
				if n <= 0 {
					return "window"
				}
				return strconv.Itoa(n/1000) + "k"
			},
			apply: func(s *Settings, v string) {
				n, _ := strconv.Atoi(strings.TrimSuffix(v, "k"))
				if s.Compaction == nil {
					s.Compaction = &CompactionSettingsJSON{}
				}
				s.Compaction.MaxContextTokens = new(n * 1000)
			},
		},
		gatedOnImages(boolSetting("show-images", "Show images", "Render images inline in terminal",
			Settings.GetShowImages, func(s *Settings) **bool { return &s.terminal().ShowImages })),
		{
			id: "image-width-cells", label: "Image width",
			desc:   "Preferred inline image width in terminal cells",
			values: []string{"60", "80", "120"},
			get: func(s Settings) string {
				return fmt.Sprintf("%d", s.GetImageWidthCells())
			},
			apply: func(s *Settings, v string) {
				n, _ := strconv.Atoi(v)
				if n > 0 {
					s.terminal().ImageWidthCells = n
				}
			},
			gated: imagesSupported,
		},
		boolSetting("auto-resize-images", "Auto-resize images", "Resize large images to 2000x2000 max for better model compatibility", Settings.GetImageAutoResize, func(s *Settings) **bool { return &s.images().AutoResize }),
		boolSetting("block-images", "Block images", "Prevent images from being sent to LLM providers", Settings.GetBlockImages, func(s *Settings) **bool { return &s.images().BlockImages }),
		boolSetting("cleanup-temp-files", "Clean up temp files", "Delete the temp files a session's tools create once nothing needs them",
			Settings.GetCleanupTempFiles, func(s *Settings) **bool { return &s.CleanupTempFiles }),
		boolSetting("skill-commands", "Skill commands", "Register skills as /skill:name commands",
			Settings.GetEnableSkillCommands, func(s *Settings) **bool { return &s.EnableSkillCommands }),
		boolSetting("show-hardware-cursor", "Show hardware cursor", "Show the terminal cursor while still positioning it for IME support", Settings.GetShowHardwareCursor, func(s *Settings) **bool { return &s.ShowHardwareCursor }),
		{
			id: "editor-padding", label: "Editor padding",
			desc:   "Horizontal padding for input editor (0-3)",
			values: []string{"0", "1", "2", "3"},
			get: func(s Settings) string {
				return fmt.Sprintf("%d", s.GetEditorPaddingX())
			},
			apply: func(s *Settings, v string) {
				n, _ := strconv.Atoi(v)
				p := max(0, min(3, n))
				s.EditorPaddingX = &p
			},
		},
		{
			id: "output-padding", label: "Output padding",
			desc:   "Horizontal padding for user messages, assistant messages, and thinking",
			values: []string{"0", "1"},
			get: func(s Settings) string {
				return fmt.Sprintf("%d", s.GetOutputPad())
			},
			apply: func(s *Settings, v string) {
				n, _ := strconv.Atoi(v)
				p := max(0, min(1, n))
				s.OutputPad = &p
			},
		},
		{
			id: "autocomplete-max-visible", label: "Autocomplete max items",
			desc:   "Max visible items in autocomplete dropdown (3-20)",
			values: []string{"3", "5", "7", "10", "15", "20"},
			get: func(s Settings) string {
				return fmt.Sprintf("%d", s.GetAutocompleteMaxVisible())
			},
			apply: func(s *Settings, v string) {
				n, _ := strconv.Atoi(v)
				m := max(3, min(20, n))
				s.AutocompleteMaxVisible = &m
			},
		},
		// Listed unconditionally (no capability gate). The capability check
		// still applies at write time inside the TUI: the menu just always
		// lets the user toggle the persisted setting.
		boolSetting("terminal-progress", "Terminal progress", "Show OSC 9;4 progress indicators in the terminal tab bar",
			Settings.GetShowTerminalProgress, func(s *Settings) **bool { return &s.terminal().ShowTerminalProgress }),
		{
			id: "steering-mode", label: "Steering mode",
			desc:   "Enter while streaming queues steering messages. 'one-at-a-time': deliver one, wait for response. 'all': deliver all at once.",
			values: []string{"one-at-a-time", "all"},
			get:    func(s Settings) string { return s.GetSteeringMode() },
			apply:  func(s *Settings, v string) { s.SteeringMode = v },
		},
		{
			id: "follow-up-mode", label: "Follow-up mode",
			desc:   fmt.Sprintf("%s queues follow-up messages until agent stops. 'one-at-a-time': deliver one, wait for response. 'all': deliver all at once.", tui.KeyDisplayText("alt+enter")),
			values: []string{"one-at-a-time", "all"},
			get:    func(s Settings) string { return s.GetFollowUpMode() },
			apply:  func(s *Settings, v string) { s.FollowUpMode = v },
		},
		{
			id: "transport", label: "Transport",
			desc:   "Preferred transport for providers that support multiple transports",
			values: []string{"sse", "websocket", "websocket-cached", "auto"},
			get:    func(s Settings) string { return cmp.Or(s.Transport, "auto") },
			apply:  func(s *Settings, v string) { s.Transport = v },
		},
		{
			id: "http-idle-timeout", label: "HTTP idle timeout",
			desc:   "Maximum idle gap while waiting for HTTP headers or body chunks. Disable for local models that pause longer than five minutes.",
			values: []string{"30 sec", "1 min", "2 min", "5 min", "disabled"},
			get: func(s Settings) string {
				timeoutMs, err := s.GetHttpIdleTimeoutMs()
				if err != nil {
					return err.Error()
				}
				return formatHTTPIdleTimeoutMs(timeoutMs)
			},
			apply: func(s *Settings, v string) {
				timeoutMs, ok := parseHTTPIdleTimeoutLabel(v)
				if !ok {
					return
				}
				s.HTTPIdleTimeoutMs = json.RawMessage(strconv.Itoa(timeoutMs))
			},
		},
		{
			id: "cache-warming-mode", label: "Cache warming",
			desc:   "off; streaming while the agent runs; idle also between runs while continuation stays profitable",
			values: []string{"off", "streaming", "idle"},
			get: func(s Settings) string {
				return string(s.GetCacheWarmingMode())
			},
			apply: func(s *Settings, v string) { s.CacheWarming = CacheWarmingMode(v) },
		},
		boolSetting("hide-thinking", "Hide thinking", "Hide thinking blocks in assistant responses", Settings.GetHideThinkingBlock, func(s *Settings) **bool { return &s.HideThinkingBlock }),
		{
			id: "mermaid-rendering", label: "Mermaid diagrams",
			desc:   "Render Mermaid code blocks as Unicode diagrams",
			values: []string{"off", "final", "streaming"},
			get: func(s Settings) string {
				return s.GetMermaidRenderingMode()
			},
			apply: func(s *Settings, v string) {
				if s.Markdown == nil {
					s.Markdown = &MarkdownSettings{}
				}
				s.Markdown.Mermaid = v
			},
		},
		boolSetting("cache-miss-notices", "Cache miss notices", "Show transcript notices for cache costs and provider recovery diagnostics", Settings.GetShowCacheMissNotices, func(s *Settings) **bool { return &s.ShowCacheMissNotices }),
		boolSetting("collapse-changelog", "Collapse changelog", "Show condensed changelog after updates", Settings.GetCollapseChangelog, func(s *Settings) **bool { return &s.CollapseChangelog }),
		boolSetting("quiet-startup", "Quiet startup", "Disable verbose printing at startup", Settings.GetQuietStartup, func(s *Settings) **bool { return &s.QuietStartup }),
		boolSetting("attribution-headers", "Attribution headers", "Identify wopr to OpenRouter, NVIDIA, and Cloudflare with app-attribution headers",
			func(s Settings) bool { return boolOr(s.EnableAttributionHeaders, true) },
			func(s *Settings) **bool { return &s.EnableAttributionHeaders }),
		{
			id: "default-project-trust", label: "Default project trust",
			desc:   "Fallback behavior when no saved trust decision decides project trust",
			values: []string{"Ask", "Always trust", "Never trust"},
			get: func(s Settings) string {
				switch s.DefaultProjectTrust {
				case "always":
					return "Always trust"
				case "never":
					return "Never trust"
				default:
					return "Ask"
				}
			},
			apply: func(s *Settings, v string) {
				switch v {
				case "Always trust":
					s.DefaultProjectTrust = "always"
				case "Never trust":
					s.DefaultProjectTrust = "never"
				default:
					s.DefaultProjectTrust = "ask"
				}
			},
		},
		{
			id: "double-escape-action", label: "Double-escape action",
			desc:   "Action when pressing Escape twice with empty editor",
			values: []string{"tree", "fork", "none"},
			get: func(s Settings) string {
				if s.DoubleEscapeAction == "" {
					return "tree"
				}
				return s.DoubleEscapeAction
			},
			apply: func(s *Settings, v string) { s.DoubleEscapeAction = v },
		},
		{
			id: "tree-filter-mode", label: "Tree filter mode",
			desc:   "Default filter when opening /tree",
			values: []string{"default", "no-tools", "user-only", "labeled-only", "all"},
			get: func(s Settings) string {
				if s.TreeFilterMode == "" {
					return "default"
				}
				return s.TreeFilterMode
			},
			apply: func(s *Settings, v string) { s.TreeFilterMode = v },
		},
		{
			// There is no separate global "Thinking level"
			// settings-list entry: the global default is set via /thinking
			// (app.thinking.cycle / app.thinking.save) in
			// thinking_selector.go. This item instead opens a per-model
			// override submenu, handled specially in settingsHandlerTUI.
			id: "model-thinking", label: "Default thinking level per model",
			desc: fmt.Sprintf("Override the default thinking level for specific models. %s cycles in-session.",
				tui.ActionKeyDisplayText("app.thinking.cycle")),
			values: nil,
			get: func(s Settings) string {
				if len(s.ModelThinkingLevels) == 0 {
					return "none"
				}
				return fmt.Sprintf("%d configured", len(s.ModelThinkingLevels))
			},
			apply: func(*Settings, string) {}, // no-op: applied via the submenu directly
		},
		{
			id: "fullscreen-exit-output", label: "Fullscreen exit output",
			desc:   "On exit: clear the screen, print the transcript, or restore the previous screen",
			values: []string{"clear", "transcript", "resume-hint"},
			get: func(s Settings) string {
				return s.GetFullscreenExitOutput()
			},
			apply: func(s *Settings, v string) { s.FullscreenExitOutput = v },
		},
		{
			id: "fullscreen-scrollbar", label: "Fullscreen scrollbar",
			desc:   "Transcript scrollbar behavior",
			values: []string{"auto", "always", "hidden"},
			get: func(s Settings) string {
				return s.GetFullscreenScrollbar()
			},
			apply: func(s *Settings, v string) { s.FullscreenScrollbar = v },
		},
		{
			id: "fullscreen-scroll-speed", label: "Scroll speed",
			desc:   "Lines one mouse wheel step scrolls (alt+wheel scrolls 5 times as far)",
			values: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"},
			get:    func(s Settings) string { return strconv.Itoa(s.GetFullscreenScrollSpeed()) },
			apply: func(s *Settings, v string) {
				n, _ := strconv.Atoi(v)
				n = max(1, min(10, n))
				s.FullscreenScrollSpeed = &n
			},
		},
		boolSetting("fullscreen-copy-on-select", "Fullscreen copy on select", "Automatically copy selected text; disable to copy selections with Ctrl+X",
			Settings.GetFullscreenCopyOnSelect, func(s *Settings) **bool { return &s.FullscreenCopyOnSelect }),
		{
			id: "theme", label: "Theme",
			desc:   "Color theme for the interface",
			values: []string{"auto", "dark", "light"},
			get: func(s Settings) string {
				if s.Theme == "" {
					return tui.ActiveTheme().Name
				}
				return s.Theme
			},
			apply: func(s *Settings, v string) {
				if v == "auto" {
					s.Theme = ""
				} else {
					s.Theme = v
				}
			},
		},
	}
}

// settingsCommand shows the settings list; choosing a row cycles or picks
// its value, which is saved to global settings and applied at once.
func (m *InteractiveMode) settingsCommand(string) error {
	sm := m.opts.SettingsManager
	if sm == nil {
		m.appendMarkdown("Settings manager unavailable.")
		return nil
	}
	items := settingsItemsVisible()
	s := sm.Get()
	tuiItems := make([]tui.SettingItem, len(items))
	for i, item := range items {
		tuiItems[i] = tui.SettingItem{
			ID:           item.id,
			Label:        item.label,
			Description:  item.desc,
			CurrentValue: item.get(s),
			Values:       item.values,
		}
		if item.id == "model-thinking" {
			tuiItems[i].Submenu = m.modelThinkingSettingsSubmenu
		}
	}
	// One list for the whole loop, so a change keeps the cursor and the
	// search where they were.
	sl := tui.NewSettingsList(tuiItems)
	for {
		s := sm.Get()
		for _, item := range items {
			sl.UpdateValue(item.id, item.get(s))
		}
		sl.Reset()
		if !m.runDialog(modalOf(sl), dialogLarge) || sl.Cancelled() {
			return nil
		}
		changedID, changedValue := sl.ChangedID, sl.ChangedValue
		var selected *settingItem
		for i := range items {
			if items[i].id == changedID {
				selected = &items[i]
				break
			}
		}
		switch {
		case selected == nil:
			continue
		case changedID == "theme":
			chosen, ok := m.pickThemeDialog()
			if !ok || chosen == "" {
				continue
			}
			changedValue = chosen
		}

		appliedValue := changedValue
		if changedID == "http-idle-timeout" {
			timeoutMs, ok := parseHTTPIdleTimeoutLabel(changedValue)
			if !ok {
				m.showStatus(fmt.Sprintf("Failed to save settings: invalid HTTP idle timeout %q", changedValue))
				continue
			}
			appliedValue = strconv.Itoa(timeoutMs)
		}
		if err := sm.UpdateGlobal(func(gs *Settings) { selected.apply(gs, changedValue) }); err != nil {
			m.showStatus(fmt.Sprintf("Failed to save settings: %v", err))
			continue
		}
		m.showStatus(fmt.Sprintf("%s: %s", selected.label, changedValue))
		m.applySetting(selected.id, appliedValue)
	}
}

// modelThinkingClearOverrideValue is the sentinel select-item value that
// clears a per-model thinking override. It is never a
// real thinking level, so it cannot collide with one.
const modelThinkingClearOverrideValue = "__clear__"

// thinkingDescriptions labels each level in the /settings submenu and /thinking.
var thinkingDescriptions = map[string]string{
	"off":     "No reasoning",
	"minimal": "Very brief reasoning (~1k tokens)",
	"low":     "Light reasoning (~2k tokens)",
	"medium":  "Moderate reasoning (~8k tokens)",
	"high":    "Deep reasoning (~16k tokens)",
	"xhigh":   "Extra-high reasoning (~32k tokens)",
	"max":     "Maximum reasoning",
}

// sessionStampLayout is the UTC start time at the front of a session file name.
const sessionStampLayout = "2006-01-02T15-04-05"

// sessionStartLabel names a session file by the local time it started, read
// from its "2006-01-02T15-04-05-000Z_<id>.jsonl" name ("today at 3:04 PM",
// "Sep 27 at 3:04 PM"), or by its file name when the name has no time.
func sessionStartLabel(path string, now time.Time) string {
	name := filepath.Base(path)
	stamp, _, _ := strings.Cut(name, "_")
	if len(stamp) < len(sessionStampLayout) {
		return name
	}
	started, err := time.Parse(sessionStampLayout, stamp[:len(sessionStampLayout)])
	if err != nil {
		return name
	}
	started = started.In(now.Location())
	day := started.Format("Jan 2")
	switch y, m, d := now.Date(); {
	case started.Year() == y && started.Month() == m && started.Day() == d:
		day = "today"
	case started.Year() != y:
		day = started.Format("Jan 2, 2006")
	}
	return day + " at " + started.Format("3:04 PM")
}
