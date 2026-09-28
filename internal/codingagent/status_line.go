package codingagent

import (
	"cmp"
	"fmt"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// StatusLine holds the session state the prompt panel and sidebar show
// (Snapshot) and renders it as a footer under the transcript printed on exit:
//
//	Line 1: ~/<pwd> (<branch>) • <session-name>
//	Line 2: ↑<in> ↓<out> $<cost> <context%>/<window> (auto)     <model> • <thinking>
//
// Color coding on the context-% column:
//
//	<=70%  default (no color)
//	>70%   yellow (warning)
//	>90%   red (error)
//
// A third line shows the keyed statuses (routing, savings) when set.
type StatusLine struct {
	tui.BaseComponent

	mu sync.RWMutex

	model                  *ai.Model
	timings                *agent.Recorder
	contextTokens          int // last turn's total tokens (input+output+cacheRead+cacheWrite) for context%
	contextUnknown         bool
	projectedContextWindow int
	working                bool

	// usageTotals reads the session's all-entry usage totals on each render,
	// summing every session entry's stored usage.
	usageTotals func() footerUsageTotals
	// subscriptionFor decides the "(sub)" marker for a newly bound model.
	subscriptionFor func(*ai.Model) bool

	// Cwd and gitBranch for line 1.
	cwd       string
	gitBranch string // cached; resolved once at init via resolveGitBranch

	// Session name shown in footer as " • <name>".
	name string

	// Thinking level display on line 2 right side.
	thinkingLevel string
	// Auto-compact indicator "(auto)" shown next to context %.
	autoCompactEnabled bool

	// providerCount: number of authenticated+reachable providers.
	// When >1, model line shows "(provider) model" prefix.
	providerCount int
	// usingSubscription: OAuth subscription pricing (e.g. GitHub Copilot).
	// Shows "(sub)" in cost display.
	usingSubscription bool
	statusHook        func(string)

	// branchChangeMu guards the OnBranchChange subscribers.
	branchChangeMu     sync.Mutex
	branchChangeHooks  map[int]func()
	branchChangeNextID int

	// keyedStatuses: keyed status strings set via SetKeyedStatus. Rendered as a third footer line when
	// non-empty.
	keyedStatuses map[string]string
}

// NewStatusLine creates a StatusLine bound to model. timings may be nil;
// cost/elapsed columns are then suppressed.
func NewStatusLine(model *ai.Model, timings *agent.Recorder) *StatusLine {
	s := &StatusLine{
		model:              model,
		timings:            timings,
		autoCompactEnabled: true,
		keyedStatuses:      make(map[string]string),
		branchChangeHooks:  make(map[int]func()),
	}
	return s
}

// update applies fn under the lock, then invalidates the render.
func (s *StatusLine) update(fn func()) {
	s.mu.Lock()
	fn()
	s.mu.Unlock()
	s.Invalidate()
}

// SetCwd sets the working directory shown in the footer and resolves
// the git branch. Call once at init.
func (s *StatusLine) SetCwd(cwd string) {
	s.update(func() { s.cwd = cwd; s.gitBranch = resolveGitBranch(cwd) })
}

// SetWorking flips the spinner column on/off.
func (s *StatusLine) SetWorking(b bool) {
	s.update(func() { s.working = b })
}

func (s *StatusLine) SetStatusHook(fn func(string)) {
	s.mu.Lock()
	s.statusHook = fn
	s.mu.Unlock()
}

// Flash forwards a brief notice to the interactive mode's status sink.
func (s *StatusLine) Flash(msg string) {
	s.mu.Lock()
	hook := s.statusHook
	s.mu.Unlock()
	if hook != nil {
		hook(msg)
	}
}

// SetModel rebinds the model. The subscription marker derives from the
// active model, so a rebind re-evaluates it.
func (s *StatusLine) SetModel(m *ai.Model) {
	s.mu.RLock()
	subscriptionFor := s.subscriptionFor
	s.mu.RUnlock()
	usingSubscription := false
	if subscriptionFor != nil {
		usingSubscription = subscriptionFor(m)
	}
	s.mu.Lock()
	s.model = m
	if subscriptionFor != nil {
		s.usingSubscription = usingSubscription
	}
	s.mu.Unlock()
	s.Invalidate()
}

// SetSubscriptionResolver installs the "(sub)" decision used by SetModel.
func (s *StatusLine) SetSubscriptionResolver(resolve func(*ai.Model) bool) {
	s.mu.Lock()
	s.subscriptionFor = resolve
	s.mu.Unlock()
}

// SetUsageTotalsSource installs the session usage totals read on each render.
func (s *StatusLine) SetUsageTotalsSource(source func() footerUsageTotals) {
	s.update(func() { s.usageTotals = source })
}

// SetName updates the session name shown in the footer.
func (s *StatusLine) SetName(name string) {
	s.update(func() { s.name = name })
}

// SetThinkingLevel updates the thinking level display.
func (s *StatusLine) SetThinkingLevel(level string) {
	s.update(func() { s.thinkingLevel = level })
}

// SetAutoCompactEnabled updates the "(auto)" indicator.
func (s *StatusLine) SetAutoCompactEnabled(enabled bool) {
	s.update(func() { s.autoCompactEnabled = enabled })
}

// SetProviderCount updates the number of authenticated+reachable providers.
// When >1, the footer shows "(provider) model" instead of just "model".
func (s *StatusLine) SetProviderCount(n int) {
	s.update(func() { s.providerCount = n })
}

// OnBranchChange subscribes to git-branch updates. Returns an unsubscribe.
func (s *StatusLine) OnBranchChange(fn func()) func() {
	s.branchChangeMu.Lock()
	id := s.branchChangeNextID
	s.branchChangeNextID++
	s.branchChangeHooks[id] = fn
	s.branchChangeMu.Unlock()
	return func() {
		s.branchChangeMu.Lock()
		delete(s.branchChangeHooks, id)
		s.branchChangeMu.Unlock()
	}
}

// notifyBranchChange runs the OnBranchChange subscribers in subscription order.
func (s *StatusLine) notifyBranchChange() {
	s.branchChangeMu.Lock()
	ids := slices.Sorted(maps.Keys(s.branchChangeHooks))
	hooks := make([]func(), len(ids))
	for i, id := range ids {
		hooks[i] = s.branchChangeHooks[id]
	}
	s.branchChangeMu.Unlock()
	for _, fn := range hooks {
		fn()
	}
}

// SetUsingSubscription updates the OAuth subscription indicator.
// When true, cost display shows "(sub)".
func (s *StatusLine) SetUsingSubscription(v bool) {
	s.update(func() { s.usingSubscription = v })
}

// SetKeyedStatus sets (or clears) a keyed status entry in the footer's
// keyed-status line.
// Pass empty text to remove the key.
func (s *StatusLine) SetKeyedStatus(key, text string) {
	s.mu.Lock()
	if text == "" {
		delete(s.keyedStatuses, key)
	} else {
		s.keyedStatuses[key] = text
	}
	s.mu.Unlock()
	s.Invalidate()
}

// SetTurnContextUsage records the latest usage for the context-window column.
// Token and cost totals come from the usage totals source instead.
func (s *StatusLine) SetTurnContextUsage(u *ai.Usage) {
	if u == nil {
		return
	}
	s.mu.Lock()
	// Context tokens = this turn's total (represents actual context window
	// usage): input + output + cacheRead + cacheWrite of the last turn.
	s.contextTokens = u.Input + u.Output + u.CacheRead + u.CacheWrite
	s.mu.Unlock()
	s.Invalidate()
}

// ResetContextUsage clears the context-window column before a transcript
// rebuild re-reads it from the branch.
func (s *StatusLine) ResetContextUsage() {
	s.update(func() { s.contextTokens = 0 })
}

// Render returns the footer lines (normally 2 plus keyed statuses).
func (s *StatusLine) Render(width int) []string {
	return renderFooter(s.Snapshot(), width)
}

// Snapshot returns the footer's current data, including the session usage
// totals, for views that present it differently (the prompt panel and the
// sidebar).
func (s *StatusLine) Snapshot() footerData {
	s.mu.RLock()
	source := s.usageTotals
	s.mu.RUnlock()
	var totals footerUsageTotals
	if source != nil {
		totals = source()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return footerData{
		model:                  s.model,
		sessionName:            s.name,
		cwd:                    s.cwd,
		gitBranch:              s.gitBranch,
		usage:                  totals,
		contextTokens:          s.contextTokens,
		contextUnknown:         s.contextUnknown,
		projectedContextWindow: s.projectedContextWindow,
		timings:                s.timings,
		working:                s.working,
		thinkingLevel:          s.thinkingLevel,
		autoCompactEnabled:     s.autoCompactEnabled,
		providerCount:          s.providerCount,
		usingSubscription:      s.usingSubscription,
		keyedStatuses:          maps.Clone(s.keyedStatuses),
	}
}

// contextWindow is the window of the model serving the session (the
// routed one while routing is on), else the bound model's; 0 when unknown.
func (d footerData) contextWindow() int {
	if d.projectedContextWindow > 0 {
		return d.projectedContextWindow
	}
	if d.model != nil {
		return d.model.Capabilities.ContextWindow
	}
	return 0
}

// footerData is the snapshot of all data needed to render the footer.
// Passed to renderFooter as a value so the free function can be tested.
type footerData struct {
	model                  *ai.Model
	sessionName            string
	cwd                    string
	gitBranch              string
	usage                  footerUsageTotals
	contextTokens          int // last turn's total tokens for context% (not cumulative)
	contextUnknown         bool
	projectedContextWindow int
	timings                *agent.Recorder
	working                bool
	thinkingLevel          string
	autoCompactEnabled     bool
	// providerCount is the number of authenticated+reachable providers.
	// When >1, the model line shows "(provider) model" instead of just "model".
	providerCount int
	// usingSubscription is true when the model's provider uses OAuth
	// subscription pricing (e.g. GitHub Copilot). Shows "(sub)" in cost.
	usingSubscription bool
	// keyedStatuses: keyed status text set via SetKeyedStatus.
	// Rendered as a third footer line (sorted by key).
	keyedStatuses map[string]string
}

// footerUsageTotals is the usage summed over every session
// entry plus the cache-hit rate of the latest assistant message.
type footerUsageTotals struct {
	input, output, cacheRead, cacheWrite int
	cost                                 float64
	latestCacheHitRate                   *float64
}

// footerUsageParts renders the footer's token, cache-hit, and cost stats.
// Kimi Coding and OAuth subscription logins show the cost even at zero, with
// "(sub)"; otherwise a zero total shows no cost segment.
func footerUsageParts(u footerUsageTotals, usingSubscription bool) []string {
	var parts []string
	if u.input != 0 {
		parts = append(parts, "↑"+formatTokens(u.input))
	}
	if u.output != 0 {
		parts = append(parts, "↓"+formatTokens(u.output))
	}
	if u.cacheRead != 0 {
		parts = append(parts, "R"+formatTokens(u.cacheRead))
	}
	if u.cacheWrite != 0 {
		parts = append(parts, "W"+formatTokens(u.cacheWrite))
	}
	if (u.cacheRead > 0 || u.cacheWrite > 0) && u.latestCacheHitRate != nil {
		parts = append(parts, "CH"+strconv.FormatFloat(*u.latestCacheHitRate, 'f', 1, 64)+"%")
	}
	if (u.cost != 0 && !math.IsNaN(u.cost)) || usingSubscription {
		costStr := "$" + strconv.FormatFloat(u.cost, 'f', 3, 64)
		if usingSubscription {
			costStr += " (sub)"
		}
		parts = append(parts, costStr)
	}
	return parts
}

// formatCwdForFooter abbreviates home and its descendants as "~" and
// "~<sep><relative path>", and leaves every other cwd, including a sibling
// that only shares home's prefix, as given. The caller passes home as HOME,
// then USERPROFILE.
func formatCwdForFooter(cwd, home string) string {
	if home == "" {
		return cwd
	}
	resolvedCwd, err := filepath.Abs(cwd)
	if err != nil {
		return cwd
	}
	resolvedHome, err := filepath.Abs(home)
	if err != nil {
		return cwd
	}
	relativeToHome, err := filepath.Rel(resolvedHome, resolvedCwd)
	if err != nil {
		return cwd
	}
	if relativeToHome == "." {
		return "~"
	}
	if relativeToHome == ".." || strings.HasPrefix(relativeToHome, ".."+string(filepath.Separator)) || filepath.IsAbs(relativeToHome) {
		return cwd
	}
	return "~" + string(filepath.Separator) + relativeToHome
}

// renderFooter produces the 2-line footer.
// Line 1: pwd (branch) • name
// Line 2: ↑in ↓out [Rcache] [Wcache] $cost context%/window (auto)   model • thinking
func renderFooter(d footerData, width int) []string {
	if width <= 0 {
		width = 80
	}

	// ── Line 1: pwd ──────────────────────────────────────────────────
	// A session cwd is always absolute. Without one, wopr shows ".",
	// which is not a path to abbreviate.
	pwd := "."
	if d.cwd != "" {
		pwd = formatCwdForFooter(d.cwd, cmp.Or(os.Getenv("HOME"), os.Getenv("USERPROFILE")))
	}
	if d.gitBranch != "" {
		pwd += " (" + d.gitBranch + ")"
	}
	if d.sessionName != "" {
		pwd += " \u2022 " + d.sessionName
	}
	line1 := widthx.TruncateToWidth(dim(pwd), width, dim("..."), false)

	// ── Line 2: stats (left) + model (right) ─────────────────────────
	// Left side: ↑in ↓out [Rcache] [Wcache] $cost context%/window (auto)
	leftParts := footerUsageParts(d.usage, d.usingSubscription)

	// Context usage: context%/window (auto)
	// This uses the LAST turn's token count (not cumulative). This reflects actual current
	// context window pressure. d.contextTokens is set per-turn in AddUsage.
	contextWindow := d.contextWindow()
	tokens := d.contextTokens
	pct := 0.0
	if contextWindow > 0 {
		pct = float64(tokens) / float64(contextWindow) * 100
	}
	autoTag := ""
	if d.autoCompactEnabled {
		autoTag = " (auto)"
	}
	display := fmt.Sprintf("%.1f%%/%s%s", pct, formatTokens(contextWindow), autoTag)
	if d.contextUnknown {
		display = fmt.Sprintf("?/%s%s", formatTokens(contextWindow), autoTag)
	}
	leftParts = append(leftParts, applyContextColor(pct, display))

	// The footer does NOT show a spinner or elapsed timer.
	// The working indicator lives in the statusContainer Loader
	// (between chat and editor), not the footer.

	statsLeft := strings.Join(leftParts, " ")

	// Right side: [provider] model • thinking
	// When multiple providers are available, prepend "(provider)" prefix.
	// The Agent supplies its "unknown" default model when no model is
	// selected, so the footer still renders a stable model identity.
	modelName := "unknown"
	modelProvider := ""
	modelReasoning := false
	if d.model != nil {
		modelName = d.model.ID
		if d.model.Provider != nil {
			modelProvider = d.model.Provider.ID()
		}
		modelReasoning = d.model.Capabilities.MaxThinking != ""
	}

	rightSide := modelName
	if modelReasoning {
		level := cmp.Or(d.thinkingLevel, "off")
		// The thinking level renders as plain
		// text: no per-level color. The entire right side is wrapped in
		// dim() with the rest of line 2, so it appears in dim grey.
		if level == "off" {
			rightSide = modelName + " \u2022 thinking " + level
		} else {
			rightSide = modelName + " \u2022 " + level
		}
	}

	// Prepend provider in parentheses when multiple providers are active.
	rightSideWithProvider := rightSide
	if d.providerCount > 1 && modelProvider != "" {
		rightSideWithProvider = "(" + modelProvider + ") " + rightSide
	}

	// Compose line 2 with right-alignment.
	// Try provider-prefixed right side first; fall back to plain if too wide.
	statsLeftWidth := widthx.VisibleWidth(statsLeft)
	// If statsLeft is too wide, truncate it.
	if statsLeftWidth > width {
		statsLeft = widthx.TruncateToWidth(statsLeft, width, "...", false)
		statsLeftWidth = widthx.VisibleWidth(statsLeft)
	}
	minPad := 2

	// Pick the widest right-side variant that fits.
	chosenRight := rightSideWithProvider
	chosenRightWidth := widthx.VisibleWidth(chosenRight)
	if statsLeftWidth+minPad+chosenRightWidth > width {
		// Provider prefix doesn't fit; fall back to plain.
		chosenRight = rightSide
		chosenRightWidth = widthx.VisibleWidth(chosenRight)
	}

	var line2 string
	totalNeeded := statsLeftWidth + minPad + chosenRightWidth
	if totalNeeded <= width {
		padding := strings.Repeat(" ", width-statsLeftWidth-chosenRightWidth)
		line2 = dim(statsLeft) + dim(padding+chosenRight)
	} else {
		// Right side doesn't fit at full width; truncate or omit
		avail := width - statsLeftWidth - minPad
		if avail > 0 {
			truncRight := widthx.TruncateToWidth(chosenRight, avail, "", false)
			truncWidth := widthx.VisibleWidth(truncRight)
			padding := strings.Repeat(" ", max(0, width-statsLeftWidth-truncWidth))
			line2 = dim(statsLeft) + dim(padding+truncRight)
		} else {
			line2 = dim(statsLeft)
		}
	}

	result := []string{line1, line2}

	// ── Line 3: keyed statuses (optional) ──────────────────────────
	// Sorted by key, space-separated,
	// sanitized (control chars → space), truncated to width.
	if status, ok := renderKeyedStatuses(d.keyedStatuses, width); ok {
		result = append(result, status)
	}

	return result
}

func renderKeyedStatuses(statuses map[string]string, width int) (string, bool) {
	if len(statuses) == 0 {
		return "", false
	}
	keys := slices.Sorted(maps.Keys(statuses))
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		text := strings.Join(strings.Fields(statuses[key]), " ")
		if text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return widthx.TruncateToWidth(strings.Join(parts, " "), width, dim("..."), false), true
}

// applyContextColor wraps the full `pct%/window (auto)` display string in
// the color appropriate to the percent: the color covers the entire token,
// not just the number.
func applyContextColor(pct float64, body string) string {
	switch {
	case pct > 90:
		return ansi(31, body) // red
	case pct > 70:
		return ansi(33, body) // yellow
	default:
		return body // no color
	}
}

// formatTokens renders an int token count as "1.2k", "230", "8.4M".
func formatTokens(n int) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1_000)
	case n < 10_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	default:
		return fmt.Sprintf("%dM", n/1_000_000)
	}
}

// formatDuration renders a duration as "1.2s" / "3m12s" / "0.0s".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", mins, secs)
}

// dim wraps text in the theme's dim foreground color and fg-only reset.
// It emits the theme's resolved dim hex color (e.g. \x1b[38;2;102;102;102m
// for dark) followed by \x1b[39m (fg-only reset) rather than SGR dim
// (\x1b[2m).
func dim(s string) string {
	th := tui.ActiveTheme()
	return th.Dim + s + "\x1b[39m"
}

func ansi(code int, body string) string {
	return fmt.Sprintf("\033[%dm%s\033[0m", code, body)
}

// resolveGitBranch returns the current git branch name, "detached" for a
// detached HEAD, or "" outside a repository. It reads HEAD directly and runs
// git only for a reftable HEAD, whose placeholder ref names no branch.
func resolveGitBranch(cwd string) string {
	if cwd == "" {
		return ""
	}
	paths, ok := findGitPaths(cwd)
	if !ok {
		return ""
	}
	content, err := os.ReadFile(paths.headPath)
	if err != nil {
		return ""
	}
	branch, ok := strings.CutPrefix(strings.TrimSpace(string(content)), "ref: refs/heads/")
	if !ok {
		return "detached"
	}
	if branch == ".invalid" {
		if resolved := resolveBranchWithGit(paths.repoDir); resolved != "" {
			return resolved
		}
		return "detached"
	}
	return branch
}

// resolveBranchWithGit asks git for the current branch. It returns "" on a
// detached HEAD or when git is unavailable. Tests replace it to observe
// process spawns.
var resolveBranchWithGit = func(repoDir string) string {
	cmd := exec.Command("git", "--no-optional-locks", "symbolic-ref", "--quiet", "--short", "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// SetContextUsage stores the Session projection estimate outside the render path.
// Nil tokens indicate unknown usage after compaction until a valid response.
func (s *StatusLine) SetContextUsage(tokens *int, contextWindow int) {
	s.mu.Lock()
	s.contextUnknown = tokens == nil && contextWindow > 0
	s.projectedContextWindow = contextWindow
	s.contextTokens = 0
	if tokens != nil {
		s.contextTokens = *tokens
	}
	s.mu.Unlock()
	s.Invalidate()
}
