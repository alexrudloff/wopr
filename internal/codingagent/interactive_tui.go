package codingagent

import (
	"context"
	"math/rand/v2"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/tempfiles"
	"github.com/alexrudloff/wopr/tui"
)

func (m *InteractiveMode) onTerminalResize() {
	m.postUITask(func() {
		m.applyEditorMaxVisible()
		// Resize only requests a render: the renderer repaints the
		// whole buffer only when the width or height changed, and it keeps
		// Termux keyboard height changes differential. SIGWINCH also arrives
		// without a geometry change (tmux window switches, terminal focus and
		// visibility changes), and a forced repaint there would replay the
		// entire transcript and snap the view to the top.
		m.tuiInst.RequestRender()
	})
}

// themedScrollbarTrackStyle and themedScrollbarThumbStyle style the fullscreen
// scrollbar with the live theme's scrollbarTrack and scrollbarThumb foregrounds
// (the theme resolver falls back to muted and text when a theme omits them).
// They read ActiveTheme() at call time: the layout invokes the styles on every
// paint, so a live theme change repaints the scrollbar on the next render.
func themedScrollbarTrackStyle(text string) string {
	return tui.ActiveTheme().FgText("scrollbarTrack", text)
}

func themedScrollbarThumbStyle(text string) string {
	return tui.ActiveTheme().FgText("scrollbarThumb", text)
}

// themeBgText wraps text in a theme background token.
func themeBgText(token, text string) string {
	bg := tui.ActiveTheme().Bg(token)
	if bg == "" {
		return text
	}
	return bg + text + tui.SGRBgReset
}

// styleSearchMatch paints a transcript search match with the live theme's
// search-match colors.
func styleSearchMatch(text string) string {
	return themeBgText("searchMatchBg", tui.ActiveTheme().FgText("searchMatchText", text))
}

// scrollToEndIndicatorLabel renders the fullscreen jump-to-latest label with
// the current tui.altScreen.bottom keys.
func scrollToEndIndicatorLabel() string {
	label := " ↓ Jump to latest message"
	if keys := tui.Keybindings().Keys(tui.KBAltScreenBottom); len(keys) > 0 {
		label += " · " + tui.FormatKeyText(strings.Join(keys, "/"), true)
	}
	return themeBgText("selectedBg", tui.ActiveTheme().FgText("text", label+" "))
}

// fullscreenTuiOptions returns the themed presentation options of the
// fullscreen renderer.
func fullscreenTuiOptions() tui.Options {
	return tui.Options{
		SearchMatchStyle: func(text string) string {
			return "\x1b[4m" + styleSearchMatch(text) + tui.SGRUnderlineReset
		},
		SearchCurrentMatchStyle: func(text string) string {
			return "\x1b[1m" + tui.ActiveTheme().Inverse(styleSearchMatch(text)) + tui.SGRBoldDimReset
		},
		SearchNavigationButtonStyle: func(text string, hovered bool) string {
			if hovered {
				return "\x1b[4m" + text + tui.SGRUnderlineReset
			}
			return text
		},
		ScrollToEndIndicator: scrollToEndIndicatorLabel,
		Background:           func() string { return tui.ActiveTheme().Bg("background") },
	}
}

// buildChatViewport constructs the fullscreen transcript over the input dock.
func (m *InteractiveMode) buildChatViewport() ChatViewport {
	return CreateChatViewport(ChatViewportOptions{
		Document:            m.chatContainer,
		PendingMessages:     m.pendingMessagesContainer,
		Status:              m.statusContainer,
		Editor:              m.editorContainer,
		WidgetsAbove:        &bgStrip{m: m},
		Sidebar:             &sidebar{m: m},
		SidebarVisible:      m.sidebarShown,
		Scrollbar:           m.settings().GetFullscreenScrollbar(),
		ScrollbarTrackStyle: themedScrollbarTrackStyle,
		ScrollbarThumbStyle: themedScrollbarThumbStyle,
	})
}

// mountInteractiveTui arranges the transcript scroll view over the dock,
// with the home screen in its place while no session is shown, and enters the
// alternate screen.
func (m *InteractiveMode) mountInteractiveTui() {
	m.editor.SetPanel(m.promptPanel())
	tui.AccentColor = func() string { return tui.ThemeHexFg(phosphorHex()) }
	if m.spinnerEpoch.IsZero() {
		m.spinnerEpoch = time.Now()
		m.cursorEpoch = m.spinnerEpoch
		m.homeSeed = rand.Uint64()
	}
	viewport := m.buildChatViewport()
	m.transcriptScrollView = viewport.Transcript
	onSetup := func(tui.LayoutViewport) bool { return m.setup != nil }
	onHome := func(tui.LayoutViewport) bool { return m.setup == nil && m.homeVisible() }
	inSession := func(tui.LayoutViewport) bool { return m.setup == nil && !m.homeVisible() }
	m.tuiInst.SetLayoutRoot(tui.NewVStack([]tui.StackChild{
		{Component: &setupHost{m: m}, Basis: new(0), Grow: new(1), Visible: onSetup},
		{Component: m.buildHomeRoot(), Basis: new(0), Grow: new(1), Visible: onHome},
		{Component: viewport.Root, Basis: new(0), Grow: new(1), Visible: inSession},
	}, tui.StackOptions{}))
	m.tuiInst.Start()
}

// createInteractiveTui constructs the fullscreen renderer for this Run and
// returns the cleanup that MUST run on every Run return.
//
// The selection auto-scroll tick is an owned state-machine tick, not a
// cosmetic render: it must reach the owner loop while the loop is alive, or
// the fired one-shot timer wedges (never re-arms). It binds a context whose
// lifetime is exactly this Run, not the turn-scoped m.abortCtx, which is
// canceled and replaced on every abort and new turn. The cleanup cancels that
// context so a tick goroutine blocked on a saturated uiTaskCh unblocks at
// shutdown instead of outliving the session.
// effectiveOpenURL returns the injected OSC 8 opener, defaulting to openBrowser.
func (m *InteractiveMode) effectiveOpenURL() func(url string) error {
	if m.openURL != nil {
		return m.openURL
	}
	return openBrowser
}

func (m *InteractiveMode) effectiveCopyClipboard() func(text string) error {
	if m.copyClipboard != nil {
		return m.copyClipboard
	}
	return copyToClipboard
}

func (m *InteractiveMode) createInteractiveTui(ctx context.Context) func() {
	uiCtx, cancelUI := context.WithCancel(ctx)
	reads := &sync.WaitGroup{}
	m.clipboardCtx, m.clipboardReads = uiCtx, reads
	copyOnSelect := m.settings().GetFullscreenCopyOnSelect()
	opts := fullscreenTuiOptions()
	opts.CopyOnSelect = &copyOnSelect
	opts.WheelScrollLines = m.settings().GetFullscreenScrollSpeed()
	opts.CopySelection = m.effectiveCopyClipboard()
	// Primary-button clicks on OSC 8 hyperlinks open the default browser.
	opts.OpenURL = func(url string) { _ = m.effectiveOpenURL()(url) }
	opts.OnRightClickPaste = m.handleRightClickPaste
	if m.rendererOut != nil {
		m.tuiInst = tui.NewWithOutput(m.rendererOut, 80, 24, opts)
	} else {
		m.tuiInst = tui.New(opts)
	}
	m.tuiInst.SetBackdrop(m.dialogBackdropState)
	m.tuiInst.SetTickDispatcher(m.autoScrollTickDispatcher(uiCtx))
	return func() { cancelUI(); reads.Wait() }
}

// autoScrollTickDispatcher returns the owner-loop dispatcher for the selection
// auto-scroll tick, bound to a STABLE owner-loop-scoped context (uiCtx), not the
// turn-scoped m.abortCtx. Delivery blocks on the owner loop's uiTaskCh while
// uiCtx is live and unblocks when uiCtx is canceled (Run return), so the tick is
// serialized with rendering and no dispatch
// goroutine outlives the owner loop. Extracted so the context binding is testable
// without driving Run().
func (m *InteractiveMode) autoScrollTickDispatcher(uiCtx context.Context) func(func()) {
	return func(fn func()) {
		m.runOnMain(uiCtx, fn)
	}
}

// requestShutdown asks the input loop to exit from any goroutine. Callers
// off the owner loop must not stop the renderer or dispose views directly:
// that would race the loop and the deferred teardown over renderer state. Instead this sets the atomic exit flag and wakes the loop
// with a non-blocking post; the loop then runs stopInteractiveTui on its own
// goroutine at the top of the next iteration. The wake may be dropped under
// saturation without losing the exit, because the loop re-reads requestExit at
// the top of every iteration and on every event. Safe on-loop too (the flag is
// simply observed on the next pass).
func (m *InteractiveMode) requestShutdown() {
	m.requestExit.Store(true)
	m.postUITask(func() {})
}

// stopInteractiveTui stops the renderer, restores cooked mode, and disposes the fullscreen transcript view exactly once. The input loop calls it before the resume hint; Run also defers it for cancellation and input errors. SIGHUP exits without terminal writes.
func (m *InteractiveMode) stopInteractiveTui() {
	if m.tuiTornDown {
		return
	}
	m.tuiTornDown = true
	if m.tuiInst != nil {
		switch m.settings().GetFullscreenExitOutput() {
		case "resume-hint":
			m.tuiInst.StopWithOptions(tui.StopOptions{PreserveScreen: true})
		case "transcript":
			m.tuiInst.SetLayoutRoot(tui.NewContainer(m.chatContainer, m.pendingMessagesContainer, m.statusContainer, m.editorContainer, m.statusLine))
			m.tuiInst.Stop()
		default:
			m.tuiInst.StopWithOptions(tui.StopOptions{PreserveScreen: true, Clear: true})
		}
	}
	// The resume hint runs only after cooked output is
	// restored; a bare LF in raw mode leaves the next writer mid-line.
	if m.rawRestore != nil {
		m.rawRestore()
		m.rawRestore = nil
	}
	// The fullscreen transcript view is owned here, not by the renderer's
	// implicitScrollView, so its scrollbar-hide timer must be disposed by the
	// owner or it can outlive the session and request a render after stop.
	if m.transcriptScrollView != nil {
		m.transcriptScrollView.Dispose()
	}
}

// handleInterruptSignal terminates the session on SIGINT after returning the
// terminal to cooked mode.
//
// Suspended sessions ignore the signal: SIGINT delivered while parked would otherwise land on resume and
// kill a session the user only backgrounded.
//
// Exits rather than unwinding Run because the input loop may be blocked in a
// read. Only the termios restore runs here; the rest of the teardown writes
// escape sequences and drains stdin, which the render loop owns.
func (m *InteractiveMode) handleInterruptSignal() {
	if m.suspended.Load() {
		return
	}
	// Restore terminal state before exit 130.
	tui.RestoreTerminalFromSignal()
	tempfiles.ExitCleanup()
	os.Exit(130)
}
