package codingagent

import (
	"cmp"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/tui"
)

func (m *InteractiveMode) appendToChat(comp tui.Component) {
	m.chatContainer.Add(comp)
}

func (m *InteractiveMode) newUserMessageBlock(text string) *tui.UserMessageBlock {
	block := tui.NewUserMessageBlock(text)
	m.firstPrompt = cmp.Or(m.firstPrompt, text)
	m.userBlocks = append(m.userBlocks, block)
	return block
}

func (m *InteractiveMode) newAssistantMessageBlock() *tui.AssistantMessageBlock {
	block := tui.NewAssistantMessageBlock(m.hideThinking)
	block.SetOutputPad(m.outputPad)
	block.SetMarkdownTransform(m.assistantMarkdownTransform(block, MarkdownMessageAssistant))
	block.SetThinkingMarkdownTransform(m.assistantMarkdownTransform(block, MarkdownMessageAssistantThinking))
	block.SetMarkdownTransformState(m.assistantMarkdownTransformState(block))
	return block
}

// updateAssistantMessageBlock applies the authoritative content snapshot and terminal state on both live events and session redraws. Tool calls are invisible boundaries between thinking runs.
func updateAssistantMessageBlock(block *tui.AssistantMessageBlock, message *agent.AssistantMessage) {
	segments := make([]tui.AssistantSegment, 0, len(message.Content))
	hasToolCalls := false
	for _, content := range message.Content {
		switch c := content.(type) {
		case ai.TextContent:
			segments = append(segments, tui.AssistantSegment{Text: c.Text})
		case ai.ThinkingContent:
			segments = append(segments, tui.AssistantSegment{Thinking: true, Text: c.Thinking})
		case ai.ToolCall:
			hasToolCalls = true
			segments = append(segments, tui.AssistantSegment{})
		}
	}
	block.SetContent(segments)
	block.SetHasToolCalls(hasToolCalls)
	block.SetTerminalError(string(message.StopReason), message.ErrorMessage)
}

// assistantMarkdownTransform builds the display-only transform for assistant text or thinking with its distinct messageType and this block's live streaming state, mode, and theme.
//
// The block is "streaming" while it is the current assistant block and the
// agent turn is active; once the turn ends or a later block becomes current it
// freezes to non-streaming (Mermaid then shows warnings / renders in final
// mode). Mode and theme are read per render so live setting/theme changes apply.
//
// None of those three inputs is a function of (markdown, width), so
// assistantMarkdownTransformState reports them into the render cache key.
// Without it the cache serves the render made under the previous state: with
// mermaidRendering "final" a diagram is skipped while streaming and then never
// drawn, because the turn ending changes neither the text nor the width.
//
// Only the built-in Mermaid transform runs; if it panics, the message keeps
// its markdown.
func (m *InteractiveMode) assistantMarkdownTransform(block *tui.AssistantMessageBlock, messageType MarkdownMessageType) func(string, int) string {
	return func(markdown string, width int) (out string) {
		streaming := m.evCurrentBlock == block && m.hasActiveAgentTurn()
		theme := tui.ActiveTheme()
		out = markdown
		defer func() { _ = recover() }()
		return transformMermaid(m.mermaidRenderingMode(), markdown, MarkdownTransformContext{MessageType: messageType, IsStreaming: streaming, AvailableWidth: width}, theme)
	}
}

// assistantMarkdownTransformState fingerprints the live state
// assistantMarkdownTransform reads, for the Markdown render cache key.
func (m *InteractiveMode) assistantMarkdownTransformState(block *tui.AssistantMessageBlock) func() string {
	return func() string {
		streaming := m.evCurrentBlock == block && m.hasActiveAgentTurn()
		state := m.mermaidRenderingMode()
		if streaming {
			state += " streaming"
		}
		if theme := tui.ActiveTheme(); theme != nil {
			state += " " + theme.Name
		}
		return state
	}
}

// mermaidRenderingMode returns the active Mermaid rendering mode
// ("off"/"final"/"streaming"), defaulting to "streaming" when no settings
// manager is present.
func (m *InteractiveMode) mermaidRenderingMode() string {
	if m.opts.SettingsManager != nil {
		return m.settings().GetMermaidRenderingMode()
	}
	return "streaming"
}

// appendChatBlock appends a standalone text/markdown block preceded by a
// blank spacer line. Use this for error messages, status messages, and
// login flow messages: any content that is NOT an AssistantMessageBlock
// (which handles its own leading spacer).
func (m *InteractiveMode) appendChatBlock(comp tui.Component) {
	m.chatContainer.Add(tui.NewSpacer(1))
	m.chatContainer.Add(comp)
}

// binaryUpdateNoticeBody builds the heading+instruction line for the
// self-update notification: bold-warning "Update Available", newline, muted
// instruction with the accent update command.
func binaryUpdateNoticeBody(t *tui.Theme, latestVersion, command string) string {
	const bold, reset = "\x1b[1m", "\x1b[0m"
	return bold + t.Warning + "Update Available" + reset +
		"\n" + t.Muted + fmt.Sprintf("New version %s is available. Run ", latestVersion) + reset + t.Accent + command + reset
}

// updateTerminalTitle sets the title from the session name and the cwd.
func (m *InteractiveMode) updateTerminalTitle() {
	setTerminalTitle(tui.BuildTerminalTitle(m.currentSession().GetSessionName(), m.opts.CWD))
}

// setTerminalTitle writes the terminal title; tests replace it.
var setTerminalTitle = tui.SetTerminalTitle

// appendBorderedNotice wraps the supplied body components between two
// warning-colored DynamicBorders: a leading Spacer(1), a DynamicBorder, the
// body blocks, and a closing DynamicBorder, all in the warning color. Callers
// supply pre-colored components.
func (m *InteractiveMode) appendBorderedNotice(blocks ...tui.Component) {
	warning := tui.ActiveTheme().Warning
	m.chatContainer.Add(tui.NewSpacer(1))
	m.chatContainer.Add(tui.NewDynamicBorder(warning))
	for _, b := range blocks {
		m.chatContainer.Add(b)
	}
	m.chatContainer.Add(tui.NewDynamicBorder(warning))
	m.tuiInst.Render()
}

// handleCopyCommand copies to the clipboard and confirms it. With
// preferSelection, an active fullscreen selection is copied when automatic
// copy-on-select is off; otherwise the last assistant message is copied.
// flashConfirmation flashes "Copied!" in fullscreen, otherwise a status line.
func (m *InteractiveMode) handleCopyCommand(flashConfirmation, preferSelection bool) {
	if preferSelection && !m.tuiInst.CopyOnSelect() && m.tuiInst.HasActiveSelection() {
		m.tuiInst.CopyActiveSelectionToClipboard()
		return
	}
	text := m.lastAssistantText
	if text == "" {
		m.showError("No agent messages to copy yet.")
		return
	}
	if err := m.effectiveCopyClipboard()(text); err != nil {
		m.showError(err.Error())
		return
	}
	m.confirmMessageCopied(flashConfirmation)
}

// showError shows a failure as an error panel: the text before the first
// ": " as its title ("model switch to "x" failed"), the rest as detail.
func (m *InteractiveMode) showError(msg string) {
	title, detail, found := strings.Cut(msg, ": ")
	if !found {
		title, detail = msg, ""
	}
	if r, size := utf8.DecodeRuneInString(title); r != utf8.RuneError {
		title = string(unicode.ToUpper(r)) + title[size:]
	}
	if m.homeVisible() {
		m.showToast("error", title, detail)
		return
	}
	m.appendChatBlock(tui.NewErrorPanel(title, detail))
	m.tuiInst.Render()
}

// confirmMessageCopied flashes "Copied!" with flashConfirmation set,
// otherwise it shows the status line.
func (m *InteractiveMode) confirmMessageCopied(flashConfirmation bool) {
	if flashConfirmation {
		// Flash for the default 1000ms.
		m.tuiInst.Flash("Copied!", 1000)
		return
	}
	m.showStatus("Copied last agent message to clipboard")
}

// showManagedToolStatus shows a managed tool's status: a spacer
// before the first report, then each report as a padded line, warnings
// prefixed and colored as warnings, info dimmed.
func (m *InteractiveMode) showManagedToolStatus(status tools.ToolStatus) {
	if !m.managedToolStatusStarted {
		m.chatContainer.Add(tui.NewSpacer(1))
		m.managedToolStatusStarted = true
	}
	theme := tui.ActiveTheme()
	message, color := status.Message, theme.Dim
	if status.Type == "warning" {
		message, color = "Warning: "+status.Message, theme.Warning
	}
	m.chatContainer.Add(tui.NewPaddedText(themeFg(color, message), statusIndent, 0, nil))
	m.lastStatusSpacer = nil
	m.lastStatusText = nil
	m.tuiInst.Render()
}

// statusIndent lines status notices up with the transcript's text column.
const statusIndent = 3

func (m *InteractiveMode) showStatus(msg string) {
	// On the home screen a notice flashes instead, so it doesn't open the
	// transcript.
	if m.homeVisible() {
		m.showFlash(msg)
		return
	}
	status := tui.ActiveTheme().FgText("textMuted", msg)
	secondLast, last := m.chatContainer.LastTwoChildren()
	if last != nil && secondLast != nil && last == m.lastStatusText && secondLast == m.lastStatusSpacer {
		m.lastStatusText.SetText(status)
	} else {
		spacer := tui.NewSpacer(1)
		text := tui.NewPaddedText(status, statusIndent, 0, nil)
		m.chatContainer.Add(spacer)
		m.chatContainer.Add(text)
		m.lastStatusSpacer = spacer
		m.lastStatusText = text
	}
	// Request a render: status messages added inside synchronous handlers
	// (e.g. /tree empty-session, /branch-summarize cancelled) otherwise never
	// reach the screen, because nothing else triggers a render.
	m.tuiInst.Render()
}
