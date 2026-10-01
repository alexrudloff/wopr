package codingagent

import (
	"cmp"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// titlecase uppercases the first letter of every word.
func titlecase(s string) string {
	runes := []rune(s)
	for i, r := range runes {
		if i == 0 || !unicode.IsLetter(runes[i-1]) && !unicode.IsDigit(runes[i-1]) {
			runes[i] = unicode.ToUpper(r)
		}
	}
	return string(runes)
}

// providerDisplayNames names the providers wopr ships or configures by
// default; any other provider id is titlecased.
var providerDisplayNames = map[string]string{
	"anthropic":      "Anthropic",
	"openai":         "OpenAI",
	"openai-codex":   "ChatGPT",
	"openrouter":     "OpenRouter",
	"google":         "Google",
	"google-vertex":  "Vertex AI",
	"amazon-bedrock": "Bedrock",
	"github-copilot": "Copilot",
	"xai":            "xAI",
	"groq":           "Groq",
	"mistral":        "Mistral",
	"deepseek":       "DeepSeek",
	"huggingface":    "Hugging Face",
}

// providerName is a provider's display name: the registry's, else wopr's
// short name, else the titlecased id.
func (m *InteractiveMode) providerName(id string) string {
	if id == "" {
		return ""
	}
	if m.opts.ModelRegistry != nil {
		if name := m.opts.ModelRegistry.GetProviderDisplayName(id); name != "" && name != id {
			return name
		}
	}
	if name, ok := providerDisplayNames[id]; ok {
		return name
	}
	return titlecase(strings.ReplaceAll(id, "-", " "))
}

// modelName is a model's display name from the registry or the model
// catalog, else its id.
func (m *InteractiveMode) modelName(provider, id string) string {
	if m.opts.ModelRegistry != nil {
		for _, entry := range m.opts.ModelRegistry.GetAll() {
			if entry.ModelID == id && (provider == "" || entry.ProviderID == provider) && entry.DisplayName != "" {
				return entry.DisplayName
			}
		}
	}
	for _, model := range ai.ListModels(provider) {
		if model.ID == id && model.DisplayName != "" {
			return model.DisplayName
		}
	}
	return id
}

// formatCompactTokens renders a token count as 1.2M, 12.3K, or the integer.
func formatCompactTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

// themeHex returns a theme token's hex color, or "" when it is unset or the
// terminal default.
func themeHex(token string) string {
	hex := tui.ActiveTheme().Colors()[token]
	if !strings.HasPrefix(hex, "#") {
		return ""
	}
	return hex
}

// promptStatusShown reports whether the prompt has its status row. The home
// screen draws no sidebar, so it always has the row. With the sidebar showing, the sidebar carries the directory, context usage, and
// command key, and the busy indicator moves onto the meta line.
func (m *InteractiveMode) promptStatusShown() bool {
	return m.homeVisible() || !m.sidebarShown(m.tuiInst.Width())
}

// promptPanel is the fullscreen prompt: the phosphor bar, the model meta
// line, and the busy/idle status row.
func (m *InteractiveMode) promptPanel() *tui.PromptPanel {
	return &tui.PromptPanel{
		Bar: func() string {
			if m.leaderPending() {
				return tui.ActiveTheme().Fg("border")
			}
			if m.inWar() {
				return tui.ThemeHexFg(warHex())
			}
			return tui.ThemeHexFg(phosphorHex())
		},
		Meta:        m.renderPromptMeta,
		Status:      m.renderPromptStatus,
		StatusShown: m.promptStatusShown,
		Placeholder: m.promptPlaceholder,
		Cursor:      m.phosphorCursor,
	}
}

// promptExamples are the example prompts in the home placeholder.
var promptExamples = []string{"Fix a TODO in the codebase", "What is the tech stack of this project?", "Fix broken tests"}

// promptPlaceholder is the home screen's question with an example prompt.
func (m *InteractiveMode) promptPlaceholder() string {
	if !m.homeVisible() || (m.editor.IsBashMode()) {
		return ""
	}
	return fmt.Sprintf("What game shall we play?  %q", promptExamples[m.promptExample%len(promptExamples)])
}

// renderPromptMeta renders the routing mode ("auto", "speed", ...) while the
// router picks the model, and "Model Provider · thinking ▮▮▮▯▯" for a model
// the user picked, followed by the subagents' choice when it differs.
func (m *InteractiveMode) renderPromptMeta(width int) string {
	return m.withBusyIndicator(m.promptMeta(width), width)
}

// promptMeta is the meta line without the busy indicator.
func (m *InteractiveMode) promptMeta(width int) string {
	th := tui.ActiveTheme()
	if m.editor.IsBashMode() {
		return th.FgText("primary", "Shell")
	}
	snap := m.statusLine.Snapshot()
	if m.inWar() {
		return m.warMeta(width)
	}
	running := ""
	if text := m.runningAgentsText(); text != "" {
		running = " " + th.FgText("textMuted", "·") + " " + hexFg(phosphorHex(), text)
	}
	subagents := ""
	if note := m.subagentsNote(); note != "" {
		subagents = " " + th.FgText("textMuted", "·") + " " + hexFg(phosphorHex(), note)
	}
	if m.routingEnabled() {
		// The router picks the model and the thinking level for each prompt.
		return hexFg(phosphorHex(), modeBadge(m.sessionRouter().Objective())) + subagents + running
	}
	var parts []string
	modelName, provider := "No provider selected", "Connect a provider"
	if model := snap.model; model != nil {
		modelName = cmp.Or(model.DisplayName, model.ID)
		provider = ""
		if model.Provider != nil {
			provider = m.providerName(model.Provider.ID())
		} else if model.ProviderMeta.ProviderID != "" {
			provider = m.providerName(model.ProviderMeta.ProviderID)
		}
	}
	level := m.thinkingLevel
	modelFg := "text"
	if m.leaderPending() {
		modelFg = "textMuted"
	}
	parts = append(parts, th.FgText(modelFg, modelName))
	if provider != "" {
		parts = append(parts, th.FgText("textMuted", provider))
	}
	parts = append(parts, th.FgText("textMuted", "·"), thinkingMeter(level))
	return widthx.TruncateToWidth(strings.Join(parts, " ")+subagents+running, width, "…", false)
}

// warMeta is the meta line in Global Thermonuclear War, all in war red:
// "☢ GLOBAL THERMONUCLEAR WAR · Model · thinking ▮▮▮▮▮ · council N", or
// "☢ GTW · Model · council N" where that doesn't fit.
func (m *InteractiveMode) warMeta(width int) string {
	name := m.modelLabel()
	council := ""
	if w := m.warSession(); w != nil {
		council = fmt.Sprintf(" · council %d", w.WarCouncilSize())
	}
	running := ""
	if text := m.runningAgentsText(); text != "" {
		running = " · " + text
	}
	full := "☢ GLOBAL THERMONUCLEAR WAR · " + name + " · max thinking" + council + running
	if widthx.VisibleWidth(full) > width {
		full = "☢ GTW · " + name + council + running
	}
	return bold(hexFg(warHex(), widthx.TruncateToWidth(full, width, "…", false)))
}

// promptBusy reports whether a turn or a status indicator is active.
func (m *InteractiveMode) promptBusy() bool {
	return !m.isIdle || m.activeStatusIndicator != nil
}

// busyIndicator is the blocks spinner followed by the status message or the
// interrupt hint.
func (m *InteractiveMode) busyIndicator() string {
	th := tui.ActiveTheme()
	frame := int(time.Since(m.spinnerEpoch) / (tui.BlocksSpinnerIntervalMs * time.Millisecond))
	accent := phosphorHex()
	if m.inWar() {
		accent = warHex()
	}
	out := tui.BlocksSpinner(frame, accent, themeHex("background"))
	switch indicator := m.activeStatusIndicator; {
	case indicator != nil && indicator.Kind != "working" && indicator.Message != "":
		return out + " " + th.FgText("warning", strings.TrimSpace(indicator.Message))
	case m.interruptArmed():
		return out + " " + th.FgText("primary", "esc again to interrupt")
	}
	return out + " " + th.FgText("text", "esc") + th.FgText("textMuted", " interrupt")
}

// withBusyIndicator right-aligns the busy indicator on the meta line when
// the status row is hidden.
func (m *InteractiveMode) withBusyIndicator(meta string, width int) string {
	if m.promptStatusShown() || !m.promptBusy() {
		return meta
	}
	busy := m.busyIndicator()
	room := width - widthx.VisibleWidth(busy) - 2
	if room < 1 {
		return widthx.TruncateToWidth(busy, width, "", false)
	}
	meta = widthx.TruncateToWidth(meta, room, "…", false)
	return meta + strings.Repeat(" ", width-widthx.VisibleWidth(meta)-widthx.VisibleWidth(busy)) + busy
}

// renderPromptStatus renders the row under the prompt: while busy the blocks
// spinner and the interrupt hint; when idle the working directory; on the
// right the context usage and the command palette key.
func (m *InteractiveMode) renderPromptStatus(width int) string {
	th := tui.ActiveTheme()
	snap := m.statusLine.Snapshot()
	left := ""
	busy := m.promptBusy()
	switch {
	case busy:
		left = " " + m.busyIndicator()
	case !m.homeVisible() && snap.cwd != "":
		if home, err := os.UserHomeDir(); err == nil {
			snap.cwd = formatCwdForFooter(snap.cwd, home)
		}
		left = " " + th.FgText("textMuted", snap.cwd)
	}
	if !busy && m.inWar() {
		// The war stays named under the prompt while idle.
		left = " " + hexFg(warHex(), "☢") + left
	}
	leftIsPath := !busy && left != "" && !m.inWar()
	var right []string
	if m.editor.IsBashMode() {
		right = append(right, th.FgText("text", "esc")+th.FgText("textMuted", " exit shell mode"))
	} else {
		if usage := m.contextUsageText(snap); usage != "" {
			right = append(right, th.FgText("textMuted", usage))
		}
		if key := m.keyHint(appCommandPalette); key != "" {
			right = append(right, th.FgText("text", key)+th.FgText("textMuted", " commands"))
		}
	}
	rightText := strings.Join(right, "  ")
	rightWidth := widthx.VisibleWidth(rightText)
	leftWidth := widthx.VisibleWidth(left)
	if leftWidth+rightWidth+2 > width {
		if leftIsPath {
			left = " " + th.FgText("textMuted", truncateLeft(snap.cwd, max(1, width-rightWidth-3)))
			leftWidth = widthx.VisibleWidth(left)
		}
		if leftWidth+rightWidth+2 > width {
			rightText = widthx.TruncateToWidth(rightText, max(0, width-leftWidth-2), "", false)
			rightWidth = widthx.VisibleWidth(rightText)
		}
	}
	return left + strings.Repeat(" ", max(0, width-leftWidth-rightWidth)) + rightText
}

// contextUsageText renders "12.3K (12%) · $0.05": the last turn's context
// tokens, the share of the context window, and the session cost when
// non-zero. It is "" before the first turn.
func (m *InteractiveMode) contextUsageText(snap footerData) string {
	if snap.contextTokens <= 0 {
		return ""
	}
	text := formatCompactTokens(snap.contextTokens)
	if window := snap.contextWindow(); window > 0 && !snap.contextUnknown {
		text += fmt.Sprintf(" (%d%%)", int(math.Round(float64(snap.contextTokens)/float64(window)*100)))
	}
	if snap.usage.cost > 0 && !math.IsNaN(snap.usage.cost) || snap.usage.costUnknown {
		text += fmt.Sprintf(" · $%.2f", snap.usage.cost)
		if snap.usage.costUnknown {
			text += "+?"
		}
	}
	return text
}

// appendTurnFooter closes a finished run with its agent, the model of its
// last reply, and its duration, or "interrupted" when it was aborted.
func (m *InteractiveMode) appendTurnFooter(messages []agent.AgentMessage) {
	var last *agent.AssistantMessage
	for i := len(messages) - 1; i >= 0 && last == nil; i-- {
		last = messages[i].Assistant
	}
	if last == nil {
		return
	}
	footer := &tui.TurnFooter{
		Model:       m.modelName(last.Provider, last.ModelID),
		Interrupted: last.StopReason == ai.StopReasonAborted,
	}
	// A turn the mode found no model for never reached one: name the mode.
	if routed, ok := m.opts.SessionHandle.(interface{ RouteError() error }); ok && m.routingEnabled() && last.StopReason == ai.StopReasonError && routed.RouteError() != nil {
		footer.Model = modeTitle(m.sessionRouter().Objective()) + " mode"
	}
	if !m.workStart.IsZero() {
		footer.Duration = time.Since(m.workStart)
	}
	m.appendToChat(footer)
}

// truncateLeft shortens s to width columns by dropping its start behind "…".
func truncateLeft(s string, width int) string {
	if widthx.VisibleWidth(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && widthx.VisibleWidth(string(runes))+1 > width {
		runes = runes[1:]
	}
	return "…" + string(runes)
}
