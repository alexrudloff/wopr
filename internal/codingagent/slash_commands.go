package codingagent

import (
	"cmp"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	wopr "github.com/alexrudloff/wopr"
	"github.com/alexrudloff/wopr/internal/codingagent/llama"
)

// BuiltinSlashCommand is one built-in slash command.
type BuiltinSlashCommand struct {
	Name        string
	Aliases     []string
	Description string
	// ArgumentHint is shown after the command in autocomplete (e.g.
	// "<provider/model>").
	ArgumentHint string
	// Hidden commands dispatch normally but are omitted from autocomplete
	// and the docs (e.g. /debug).
	Hidden bool

	// run runs the command in the interactive UI.
	run func(m *InteractiveMode, args string) error
	// headless runs the text version for Session.DispatchSlash; nil means
	// the command needs the interactive UI.
	headless func(sc *SlashContext) error
}

// BuiltinSlashCommands returns the built-in slash commands: the single
// source for dispatch, autocomplete, and the docs check.
func BuiltinSlashCommands() []BuiltinSlashCommand {
	return []BuiltinSlashCommand{
		{Name: "settings", Description: "Open settings menu", run: (*InteractiveMode).settingsCommand},
		{Name: "model", Description: "Select model (opens selector UI)", ArgumentHint: "<provider/model>", run: (*InteractiveMode).modelCommand, headless: headlessModel},
		{Name: "tree", Description: "Navigate session tree (switch branches)", run: func(m *InteractiveMode, _ string) error { return m.treeCommand("") }, headless: headlessTree},
		{Name: "thinking", Description: "Set thinking level", ArgumentHint: "<level>", run: (*InteractiveMode).thinkingCommand},
		{Name: "export", Description: "Export session (HTML default, or specify path: .html/.jsonl)", run: (*InteractiveMode).exportCommand, headless: headlessExport},
		{Name: "import", Description: "Import and resume a session from a JSONL file", run: (*InteractiveMode).importCommand},
		{Name: "share", Description: "Share session as a secret GitHub gist", run: (*InteractiveMode).shareSessionCommand},
		// /bug exports a local archive for a WOPR issue; it never uploads.
		{Name: "bug", Description: "Export a bug report to attach to a WOPR issue", ArgumentHint: "<description>", run: (*InteractiveMode).bugCommand},
		{Name: "copy", Description: "Copy last agent message to clipboard", run: (*InteractiveMode).copyCommand, headless: headlessCopy},
		{Name: "name", Description: "Set session display name", run: (*InteractiveMode).nameCommand, headless: headlessName},
		{Name: "session", Description: "Show session info and stats", run: (*InteractiveMode).sessionCommand, headless: headlessSession},
		{Name: "upgrade", Description: "Upgrade " + AppName + " to the latest release", run: (*InteractiveMode).upgradeCommand},
		{Name: "changelog", Description: "Show changelog entries", run: func(m *InteractiveMode, _ string) error { m.appendMarkdown(changelogText()); return nil }, headless: func(sc *SlashContext) error { sc.Append(changelogText()); return nil }},
		{Name: "hotkeys", Description: "Show all keyboard shortcuts", run: (*InteractiveMode).hotkeysCommand, headless: func(sc *SlashContext) error { sc.Append(hotkeysText(defaultHotkeyLines)); return nil }},
		{Name: "fork", Description: "Create a new fork from a previous user message", run: (*InteractiveMode).forkCommand, headless: headlessFork},
		{Name: "clone", Description: "Duplicate the current session at the current position", run: (*InteractiveMode).cloneCommand, headless: headlessClone},
		{Name: "trust", Description: "Save project trust decision for future sessions", run: (*InteractiveMode).trustCommand},
		{Name: "login", Description: "Configure provider authentication", ArgumentHint: "<provider>", run: (*InteractiveMode).loginCommand},
		{Name: "logout", Description: "Remove stored provider authentication", run: (*InteractiveMode).logoutCommand},
		{Name: "setup", Description: "Set up models: subscriptions, API keys, endpoints, and routing", run: (*InteractiveMode).setupCommand},
		{Name: "new", Description: "Start a new session", run: (*InteractiveMode).newCommand, headless: headlessNew},
		{Name: "undo", Description: "Undo the last file change (or: /undo <path>, /undo prompt)", ArgumentHint: "[path|prompt]", run: (*InteractiveMode).undoCommand},
		{Name: "compact", Description: "Manually compact the session context", run: (*InteractiveMode).compactCommand},
		{Name: "resume", Description: "Resume a different session", run: (*InteractiveMode).resumeCommand, headless: headlessResume},
		{Name: "reload", Description: "Reload keybindings, skills, prompts, themes, and context files", run: (*InteractiveMode).reloadCommand},
		{Name: "quit", Description: "Quit " + AppName, run: func(m *InteractiveMode, _ string) error { m.requestQuit(); return nil }, headless: func(sc *SlashContext) error { sc.Quit(); return nil }},
		{Name: "agents", Description: "List, open, or stop background agents", ArgumentHint: "[<id>|stop [id]]", run: func(m *InteractiveMode, args string) error { m.agentsCommand(args); return nil }},
		{Name: "goal", Description: "Work toward a goal across turns until an audit confirms it; status, stop, resume", ArgumentHint: "[<objective>|stop|resume]", run: (*InteractiveMode).goalCommand},
		{Name: llama.CommandName, Description: llama.CommandDescription, run: (*InteractiveMode).llamaCommand},
		{Name: "router", Description: "Model router: status, on, off, pin <tier>, unpin, why, classify <prompt>", ArgumentHint: "[status|on|off|pin <tier>|unpin|why]", run: (*InteractiveMode).routerCommand},
		{Name: "mcp", Description: "MCP servers: status, tools <server>, restart [server]", ArgumentHint: "[status|tools <server>|restart [server]]", run: (*InteractiveMode).mcpCommand},
		// /debug writes a debug log; ctrl+shift+d runs it too.
		{Name: "debug", Description: "Write a debug log", Hidden: true, run: (*InteractiveMode).debugCommand},
	}
}

// SlashRegistry resolves slash command names and aliases.
type SlashRegistry struct {
	commands map[string]*BuiltinSlashCommand // by name and alias
}

// NewSlashRegistry returns the registry of built-in commands.
func NewSlashRegistry() *SlashRegistry {
	r := &SlashRegistry{commands: map[string]*BuiltinSlashCommand{}}
	for _, cmd := range BuiltinSlashCommands() {
		r.commands[cmd.Name] = &cmd
		for _, alias := range cmd.Aliases {
			r.commands[alias] = &cmd
		}
	}
	return r
}

// Resolve returns the command's canonical name, following aliases.
func (r *SlashRegistry) Resolve(name string) (string, bool) {
	cmd, ok := r.commands[name]
	if !ok {
		return "", false
	}
	return cmd.Name, true
}

// lookup parses line and finds its command.
func (r *SlashRegistry) lookup(line string) (*BuiltinSlashCommand, string, error) {
	name, args := parseSlashLine(line)
	if name == "" {
		return nil, "", errors.New("empty slash command")
	}
	cmd, ok := r.commands[name]
	if !ok {
		return nil, "", ErrUnknownSlashCommand
	}
	return cmd, args, nil
}

// ErrUnknownSlashCommand signals that a slash command was not found. The
// caller sends the original text to the model as a regular message.
var ErrUnknownSlashCommand = errors.New("unknown slash command")

// Dispatch runs line without the interactive UI: commands with a text
// version run it, the rest say they need the UI. It returns
// ErrUnknownSlashCommand for an unknown command and the command's error
// otherwise; it does not report errors through sc.
func (r *SlashRegistry) Dispatch(sc *SlashContext, line string) error {
	cmd, args, err := r.lookup(line)
	if err != nil {
		return err
	}
	if cmd.headless == nil {
		sc.Append("/" + cmd.Name + " needs the interactive UI.")
		return nil
	}
	sc.Args = args
	return cmd.headless(sc)
}

// parseSlashLine splits "/cmd rest of line" into ("cmd", "rest of line").
func parseSlashLine(line string) (name, args string) {
	s, ok := strings.CutPrefix(strings.TrimSpace(line), "/")
	if !ok {
		return "", ""
	}
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	return strings.TrimSpace(s), ""
}

// NavigateTreeResult is the outcome of a tree navigation. It mirrors
// coding.NavigateTreeResult, which this package cannot import.
type NavigateTreeResult struct {
	EditorText string
	Cancelled  bool
	Aborted    bool
}

// ─── Text versions for Session.DispatchSlash ─────────────────────────────────

// SlashContext is what a slash command sees without the interactive UI:
// the arguments, where output goes, and the session operations the text
// versions use. Session.DispatchSlash fills it.
type SlashContext struct {
	// Args is the text after the command name, trimmed.
	Args string

	Append     func(string) // markdown output
	AppendText func(string) // plain output
	Clear      func()
	Quit       func()
	// Reset clears the agent's messages for /new.
	Reset func()

	ModelName        func() string
	LastAssistant    func() string
	CopyClipboard    func(text string) error
	CurrentSession   func() *Session
	SetSessionName   func(name string) error
	ForkToNewSession func(userMsgEntryID string) error
	CloneCurrent     func() (newPath string, err error)
	ListSessions     func() ([]SessionInfo, error)
	RenderTree       func() string
}

func headlessModel(sc *SlashContext) error {
	if sc.Args != "" {
		sc.Append("Model switching needs the interactive UI.")
		return nil
	}
	sc.Append("Current model: " + sc.ModelName())
	return nil
}

func headlessTree(sc *SlashContext) error {
	txt := sc.RenderTree()
	if txt == "" {
		sc.Append("(empty tree)")
		return nil
	}
	sc.Append("**Session tree**\n\n```\n" + txt + "\n```")
	return nil
}

func headlessExport(sc *SlashContext) error {
	message, err := exportSession(sc.CurrentSession(), sc.Args)
	if err != nil {
		return err
	}
	sc.Append(message)
	return nil
}

func headlessCopy(sc *SlashContext) error {
	text := sc.LastAssistant()
	if text == "" {
		return errors.New("No agent messages to copy yet.")
	}
	if err := sc.CopyClipboard(text); err != nil {
		return err
	}
	sc.Append("Copied last agent message to clipboard")
	return nil
}

func headlessName(sc *SlashContext) error {
	if sc.Args == "" {
		sc.Append(sessionNameText(sc.CurrentSession()))
		return nil
	}
	if err := sc.SetSessionName(sc.Args); err != nil {
		return err
	}
	sc.Append("Session name set: " + sc.Args)
	return nil
}

func headlessSession(sc *SlashContext) error {
	sc.AppendText(sessionInfoText(sc.CurrentSession(), defaultCacheWarmingMode, nil))
	return nil
}

func headlessFork(sc *SlashContext) error {
	if sc.Args == "" {
		sc.Append("Usage: /fork <entry-id>\nRun /tree to see entry ids.")
		return nil
	}
	if err := sc.ForkToNewSession(sc.Args); err != nil {
		return err
	}
	sc.Append("Forked to new session")
	return nil
}

func headlessClone(sc *SlashContext) error {
	path, err := sc.CloneCurrent()
	if err != nil {
		return err
	}
	sc.Append(fmt.Sprintf("Cloned current path to:\n  %s\n\nThis session has been switched to the new file.", path))
	return nil
}

func headlessNew(sc *SlashContext) error {
	sc.Reset()
	sc.Clear()
	sc.Append("Started a fresh conversation. Message history cleared.")
	return nil
}

// headlessResume lists this folder's sessions to relaunch with.
func headlessResume(sc *SlashContext) error {
	infos, err := sc.ListSessions()
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		sc.Append("No sessions found in this project's session directory.")
		return nil
	}
	var b strings.Builder
	b.WriteString("**Recent sessions in this directory** (relaunch with `--session <id>` or `--continue` to pick the latest):\n\n")
	for i, info := range infos {
		if i >= 20 {
			fmt.Fprintf(&b, "\n  …and %d more.\n", len(infos)-20)
			break
		}
		name := cmp.Or(info.Name, info.FirstMessage, "(no name)")
		fmt.Fprintf(&b, "  - `%s`  · %d msg · %s · %s\n", info.ID, info.MessageCount, info.Modified.Format("2006-01-02 15:04"), name)
	}
	sc.Append(b.String())
	return nil
}

// ─── Shared output ───────────────────────────────────────────────────────────

// sessionNameText shows the session's name dimmed, or the usage.
func sessionNameText(session *Session) string {
	if session != nil {
		if name := session.GetSessionName(); name != "" {
			return "\033[2mSession name: " + name + "\033[0m"
		}
	}
	return "Usage: /name <name>"
}

// sessionInfoText is /session's output: plain ANSI text, bold headings
// and dim labels.
func sessionInfoText(s *Session, cacheMode CacheWarmingMode, cacheStatus *CacheWarmingStatus) string {
	bold := func(s string) string { return "\033[1m" + s + "\033[22m" }
	dim := func(s string) string { return "\033[2m" + s + "\033[22m" }

	var b strings.Builder
	b.WriteString(bold("Session Info") + "\n\n")
	var stats SessionAccounting
	if s != nil {
		if name := s.GetSessionName(); name != "" {
			fmt.Fprintf(&b, "%s %s\n", dim("Name:"), name)
		}
		fmt.Fprintf(&b, "%s %s\n", dim("File:"), cmp.Or(s.Path(), "In-memory"))
		fmt.Fprintf(&b, "%s %s\n", dim("ID:"), s.ID())
		stats = s.Accounting()
	}
	b.WriteString("\n" + bold("Messages") + "\n")
	fmt.Fprintf(&b, "%s %d\n", dim("Total:"), stats.TotalMessages)
	fmt.Fprintf(&b, "%s %d\n", dim("User:"), stats.UserMessages)
	fmt.Fprintf(&b, "%s %d\n", dim("Assistant:"), stats.AssistantMessages)
	fmt.Fprintf(&b, "%s %d calls, %d results\n", dim("Tools:"), stats.ToolCalls, stats.ToolResults)

	ts := stats.Tokens
	promptTokens := ts.Input + ts.CacheRead + ts.CacheWrite
	b.WriteString("\n" + bold("Tokens") + "\n")
	fmt.Fprintf(&b, "%s %s\n", dim("Input:"), formatNumber(promptTokens))
	if promptTokens > 0 && (ts.CacheRead > 0 || ts.CacheWrite > 0) {
		fmt.Fprintf(&b, "  %s %s %s\n", dim("Cached:"), formatNumber(ts.CacheRead), dim("("+strconv.FormatFloat(float64(ts.CacheRead)/float64(promptTokens)*100, 'f', 1, 64)+"%)"))
		written := ""
		if ts.CacheWrite > 0 {
			written = " " + dim(fmt.Sprintf("(%s written to cache)", formatNumber(ts.CacheWrite)))
		}
		fmt.Fprintf(&b, "  %s %s%s\n", dim("Uncached:"), formatNumber(ts.Input+ts.CacheWrite), written)
	}
	fmt.Fprintf(&b, "%s %s\n", dim("Output:"), formatNumber(ts.Output))
	fmt.Fprintf(&b, "%s %s\n", dim("Total:"), formatNumber(ts.Total))

	statusText := "Inactive (cache warming unavailable)"
	if cacheStatus != nil {
		statusText = FormatCacheWarmingStatus(*cacheStatus, time.Now().UnixMilli())
	}
	b.WriteString("\n" + bold("Cache Warming") + "\n")
	fmt.Fprintf(&b, "%s %s\n", dim("Mode:"), cacheMode)
	fmt.Fprintf(&b, "%s %s\n", dim("Status:"), statusText)
	if cacheStatus != nil && cacheStatus.Decision != nil && cacheStatus.Decision.EconomicsAvailable {
		fmt.Fprintf(&b, "%s $%.3f\n", dim("Cache miss penalty:"), cacheStatus.Decision.MissCost)
		fmt.Fprintf(&b, "%s $%.3f\n", dim("Refresh cost:"), cacheStatus.Decision.WarmCost)
	}

	if ts.Cost > 0 || stats.CacheWaste.MissedTokens > 0 {
		b.WriteString("\n" + bold("Cost") + "\n")
		fmt.Fprintf(&b, "%s $%.3f", dim("Total:"), ts.Cost)
		if len(stats.UsageBreakdown) > 1 {
			for _, entry := range stats.UsageBreakdown {
				fmt.Fprintf(&b, "\n  %s $%.3f %s", dim(entry.Key+":"), entry.Cost, dim(fmt.Sprintf("(%s tokens)", formatTokens(entry.Tokens))))
			}
		}
		if waste := stats.CacheWaste; waste.MissedTokens > 0 {
			missLabel := fmt.Sprintf("%d misses", waste.MissCount)
			if waste.MissCount == 1 {
				missLabel = "1 miss"
			}
			detail := fmt.Sprintf("%s tokens, %s", formatNumber(waste.MissedTokens), missLabel)
			if waste.MissedCost >= 0.0001 {
				fmt.Fprintf(&b, "\n%s $%.3f %s", dim("Cache Re-billed:"), waste.MissedCost, dim("("+detail+")"))
			} else {
				fmt.Fprintf(&b, "\n%s %s", dim("Cache Re-billed:"), detail)
			}
		}
	}
	return b.String()
}

// formatNumber adds comma separators to an integer for readability.
func formatNumber(n int) string {
	if n < 0 {
		return "-" + formatNumber(-n)
	}
	s := strconv.Itoa(n)
	var buf strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			buf.WriteByte(',')
		}
		buf.WriteRune(r)
	}
	return buf.String()
}

// changelogText is the bundled changelog as a chat block. The
// collapse-changelog setting applies to the startup notice only.
func changelogText() string {
	return FormatChangelogForChat(ParseChangelog(wopr.Changelog))
}

// defaultHotkeyLines lists the default keys where no keybindings are loaded.
var defaultHotkeyLines = []string{
	"Enter       : submit",
	"Ctrl+J      : newline",
	"Shift+Enter : newline (kitty/iTerm)",
	"Alt+Enter   : follow-up (while working) / submit (idle)",
	"Alt+Up      : dequeue follow-up messages",
	"Esc         : abort (while working) / cancel",
	"Esc Esc     : open /tree (500ms window)",
	"Ctrl+C      : abort (working) / clear editor (idle)",
	"Ctrl+D      : exit wopr",
	"Ctrl+O      : toggle all tool details",
	"Ctrl+G      : open external editor ($VISUAL / $EDITOR)",
	"Ctrl+L      : model picker",
	"Ctrl+P      : cycle model forward",
	"Shift+Ctrl+P: cycle model backward",
	"Ctrl+T      : toggle thinking block visibility",
	"Ctrl+V      : paste image from clipboard",
	"Ctrl+Z      : suspend (resume with fg)",
	"Shift+Tab   : cycle thinking level",
	"Ctrl+U      : kill line (cut to start)",
	"Ctrl+K      : kill to end of line",
	"Ctrl+Y      : yank (paste from kill ring)",
	"Alt+Y       : cycle yank ring",
	"Ctrl+/      : undo",
	"Ctrl+A      : move to start of line",
	"Ctrl+E      : move to end of line",
	"Alt+B       : word backward",
	"Alt+F       : word forward",
	"Alt+Bksp    : delete word backward",
	"Alt+D       : delete word forward",
	"Ctrl+Del    : delete forward",
	"Up/Down     : history navigation (empty editor)",
}

func hotkeysText(lines []string) string {
	var b strings.Builder
	b.WriteString("**Hotkeys**\n\n")
	for _, l := range lines {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	return b.String()
}
