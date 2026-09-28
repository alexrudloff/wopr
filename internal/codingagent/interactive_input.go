package codingagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/tui"
)

// inputLoop reads terminal input from source and dispatches to the editor or
// agent.
func (m *InteractiveMode) inputLoop(ctx context.Context, source io.Reader) error {
	readCh := make(chan inputChunk)
	errCh := make(chan error, 1)
	go m.pumpTerminalInput(ctx, source, readCh, errCh)

	dispatchInput := func(input inputChunk) error {
		defer input.ticket.settle()
		chunk := string(input.data)
		if chunk == "" {
			return nil
		}
		if os.Getenv("WOPR_DEBUG_KEYS") != "" {
			debugLog("key %q -> %q", chunk, keyAction(chunk, m.keybindings))
		}
		if err := m.dispatchInputChunk(ctx, chunk, input.ticket); err != nil {
			return err
		}
		// Paint the keystroke immediately, before asynchronous autocomplete
		// results request their throttled follow-up frame.
		m.tuiInst.Render()
		return nil
	}

	for {
		if m.requestExit.Load() {
			m.stopInteractiveTui()
			if err := m.requestedExitError(); err != nil {
				return err
			}
			m.printResumeHint()
			return nil
		}
		// Prioritize terminal input over agent/render events. During tool output or
		// streaming, eventCh/uiTaskCh can stay hot; without this pre-check a waiting
		// keystroke can sit behind repeated render work.
		switch kind, buf := priorityInput(readCh, nil); kind {
		case priorityInputClosed:
			readCh = nil
			continue
		case priorityInputRead:
			if err := dispatchInput(buf); err != nil {
				return err
			}
			continue
		case priorityInputFlush, priorityInputNone:
		}
		select {
		case <-ctx.Done():
			// Signal-triggered shutdown (SIGTERM cancels the root ctx);
			// in-flight ops are already aborting because m.abortCtx derives
			// from ctx.
			return nil
		case err := <-errCh:
			return err
		case fn := <-m.uiTaskCh:
			// A background worker posted a UI mutation (e.g. async autocomplete
			// results). Run it here so editor/component state is touched only on
			// this goroutine, single-threaded with keystroke handling.
			fn()
		case <-m.renderWakeCh:
			m.runScheduledRender()
		case ev, ok := <-m.eventCh:
			// Agent live events (streaming deltas, tool exec, compaction). Handle
			// on this goroutine so the component tree is mutated + rendered
			// single-threaded with keystrokes and posted UI tasks. m.eventCh
			// closes only at session
			// shutdown; nil-out so the disabled case stops selecting.
			if !ok {
				m.eventCh = nil
				continue
			}
			m.handleAgentEvent(ev)
		case buf, ok := <-readCh:
			if !ok {
				readCh = nil
				continue
			}
			if err := dispatchInput(buf); err != nil {
				return err
			}
		}
	}
}

// normalizeInputSequence applies native Shift+Enter normalization to one
// complete StdinBuffer sequence.
var normalizeInputSequence = tui.NormalizeProcessInputSequence

// normalizeInputSequences normalizes freshly read sequences in place. Input
// retained across a startup prompt was normalized when it was read.
func normalizeInputSequences(sequences []string) []string {
	for i, sequence := range sequences {
		sequences[i] = normalizeInputSequence(sequence)
	}
	return sequences
}

// pumpTerminalInput owns the one StdinBuffer for the process input stream and
// routes only complete sequences. Input is parsed before focus dispatch, so
// switching between the editor and a modal cannot split one
// terminal read differently or lose a partial escape sequence.
//
// Terminal-input listeners answer synchronously before anything else sees a chunk. A chunk routed to the main loop therefore holds all input after it until its listeners settle. Ctrl+C follows the same ordering as every other key. While a chunk is unsettled the pump stops receiving raw input, leaving at most one read ahead in the reader worker.
func (m *InteractiveMode) pumpTerminalInput(ctx context.Context, source io.Reader, readCh chan<- inputChunk, errCh chan<- error) {
	rawCh := make(chan []byte)
	rawErrCh := make(chan error, 1)
	// The reader never waits on routing: it counts ctrl+c presses as it
	// reads, so three quick presses exit even when routing is wedged, and
	// queues what it read, in order, for the pump.
	readerCh := make(chan []byte)
	go func() {
		var escape emergencyExit
		for {
			buf, err := tui.ReadInput(source)
			if err != nil {
				rawErrCh <- err
				return
			}
			for range ctrlCPresses(buf) {
				if escape.press(time.Now()) {
					m.emergencyExit()
				}
			}
			select {
			case readerCh <- append([]byte(nil), buf...):
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		var queue [][]byte
		for {
			var out chan []byte
			var head []byte
			if len(queue) > 0 {
				out, head = rawCh, queue[0]
			}
			select {
			case buf := <-readerCh:
				queue = append(queue, buf)
			case out <- head:
				queue[0] = nil
				queue = queue[1:]
			case <-ctx.Done():
				return
			}
		}
	}()

	stdinBuf := newProcessStdinBuffer()
	var flush stdinFlushTimer
	var backlog inputBacklog
	route := func(chunks []string) {
		for _, chunk := range chunks {
			if chunk == "" || tui.HandleKeyboardProtocolNegotiationSequence(chunk) {
				continue
			}
			backlog.held = append(backlog.held, chunk)
		}
		for ctx.Err() == nil && backlog.waiting == nil {
			if len(backlog.held) == 0 {
				backlog.held = nil
				return
			}
			chunk := backlog.held[0]
			backlog.held[0] = ""
			backlog.held = backlog.held[1:]
			backlog.waiting = m.routeInputChunk(ctx, []byte(chunk), readCh)
		}
	}
	process := func(buf []byte) {
		route(normalizeInputSequences(stdinBuf.ProcessTerminalBytes(buf)))
		flush.sync(stdinBuf)
	}
	flushPending := func() {
		flush.stop()
		route(normalizeInputSequences(stdinBuf.Flush()))
	}

	// Startup dialogs (notably project trust) can finish in the middle of one
	// decoded terminal read. Deliver the type-ahead after the main editor takes
	// focus instead of letting terminal teardown discard it.
	route(takeStartupInput())

	defer close(readCh)
	var readErr error
	for {
		if backlog.waiting != nil {
			select {
			case <-ctx.Done():
				flush.stop()
				return
			case <-backlog.waiting.done:
				backlog.waiting = nil
				route(nil)
				continue
			}
		}
		if readErr != nil && len(backlog.held) == 0 {
			select {
			case errCh <- readErr:
			case <-ctx.Done():
			}
			return
		}
		switch kind, buf := priorityInput(rawCh, flush.C); kind {
		case priorityInputRead:
			process(buf)
			continue
		case priorityInputFlush:
			flushPending()
			continue
		case priorityInputClosed, priorityInputNone:
		}
		select {
		case <-ctx.Done():
			flush.stop()
			return
		case err := <-rawErrCh:
			// Everything read before the error is still delivered in order.
			rawErrCh = nil
			readErr = err
			flushPending()
		case buf := <-rawCh:
			process(buf)
		case <-flush.C:
			flushPending()
		}
	}
}

// emergencyExitPresses ctrl+c presses within emergencyExitWindow exit
// wopr from the input pump, before any routing, so a wedged modal or main
// loop can never trap the user.
const (
	emergencyExitPresses = 3
	emergencyExitWindow  = 1500 * time.Millisecond
)

// ctrlCPresses counts the ctrl+c presses in raw terminal input: the legacy
// byte, and the kitty keyboard protocol's press (not release) reports.
func ctrlCPresses(buf []byte) int {
	text := string(buf)
	n := strings.Count(text, "\x03")
	for _, press := range []string{"\x1b[99;5u", "\x1b[99;5:1u"} {
		n += strings.Count(text, press)
	}
	return n
}

// emergencyExit counts recent ctrl+c presses.
type emergencyExit struct {
	presses []time.Time
}

// press records a ctrl+c and reports whether it completes the escape.
func (e *emergencyExit) press(now time.Time) bool {
	e.presses = append(e.presses, now)
	for len(e.presses) > 0 && now.Sub(e.presses[0]) > emergencyExitWindow {
		e.presses = e.presses[1:]
	}
	return len(e.presses) >= emergencyExitPresses
}

// emergencyExit asks for the normal shutdown, and if the process is still
// running two seconds later (the main loop is stuck), restores the terminal
// and exits.
func (m *InteractiveMode) emergencyExit() {
	m.requestShutdown()
	time.AfterFunc(2*time.Second, func() {
		tui.LeaveFullscreenFromSignal()
		tui.RestoreTerminalFromSignal()
		os.Exit(130)
	})
}

type priorityInputKind int

const (
	priorityInputNone priorityInputKind = iota
	priorityInputRead
	priorityInputClosed
	priorityInputFlush
)

// priorityInput returns the ready input-loop work that runs before agent and
// render events, without blocking. Waiting terminal input wins over an expired
// flush timeout: the continuation of a split escape sequence must join its
// prefix, not find the prefix already flushed as a key.
func priorityInput[T any](readCh <-chan T, flushC <-chan time.Time) (priorityInputKind, T) {
	var none T
	select {
	case buf, ok := <-readCh:
		if !ok {
			return priorityInputClosed, none
		}
		return priorityInputRead, buf
	default:
	}
	select {
	case <-flushC:
		return priorityInputFlush, none
	default:
	}
	return priorityInputNone, none
}

// dispatchKey processes a single keystroke (post-splitting) that no input pump
// waits on.
func (m *InteractiveMode) dispatchKey(ctx context.Context, data string) error {
	return m.dispatchInputChunk(ctx, data, nil)
}

// dispatchInputChunk processes one keystroke routed with ticket.
func (m *InteractiveMode) dispatchInputChunk(ctx context.Context, data string, ticket *inputTicket) error {
	// Viewport input (mouse wheel/click, focus events) is handled by the
	// renderer and must not reach the editor.
	consumed := m.tuiInst.HandleViewportInput(data)
	m.syncEditorFocusWithSearch()
	if consumed {
		return nil
	}
	// Terminal-input listeners run first. If any consume the input, skip
	// normal dispatch.
	return m.passTerminalInput(ctx, data, ticket, m.handleKey)
}

// handleKey handles a keystroke the terminal-input listeners passed on.
func (m *InteractiveMode) handleKey(ctx context.Context, data string) error {
	// Consume the terminal's reply to the startup cell-size query so it never
	// reaches the editor. This runs after the input listeners and before the
	// focused component.
	if m.tuiInst.ConsumeCellSizeResponse(data) {
		return nil
	}

	// While fullscreen transcript search has focus it is the focused
	// component, so the remaining keys edit its query instead of the editor.
	if m.tuiInst.HandleFocusedSearchInput(data) {
		return nil
	}

	// Generic overlays and mouse-focused nested controls use the renderer's
	// focus target. The application editor keeps the driver-owned action routing
	// below; every other focused component receives input directly.
	focused := m.tuiInst.FocusedComponent()
	if focused != nil && focused != m.editor {
		if tui.ShouldDeliverKey(focused, data) {
			if input, ok := focused.(tui.InputHandler); ok {
				input.HandleInput(data)
				m.tuiInst.RequestRender()
			}
		}
		return nil
	}

	// Drop Kitty key-release events before the editor / keybinding dispatch.
	// The alt-screen viewport handler (above) and terminal-input
	// listeners see raw input, but the focused editor must not act on a release
	// or every keystroke fires twice under the Kitty keyboard protocol
	// (extendedKeyInit pushes \x1b[>7u, whose flag 2 reports event types).
	if !tui.ShouldDeliverKey(m.editor, data) {
		return nil
	}

	m.cursorEpoch = time.Now()
	if m.handleLeaderKey(ctx, data) {
		return nil
	}
	// Tab on an empty prompt cycles auto and the recent models.
	if m.editor.Text() == "" && !m.editor.AutocompleteOpen() && tui.MatchesKeyID(data, "tab") {
		m.cycleModelMode(ctx)
		return nil
	}

	action := keyAction(data, m.keybindings)
	// app.exit requires a truly empty editor. Spaces and newlines are editor content: Ctrl+D must fall through to the editor's
	// delete-char-forward binding rather than exit.
	editorEmpty := m.editor.Text() == ""
	// Escape cancels compaction while it runs. Compaction itself may be active while the agent is idle,
	// so routing through the normal idle/working outcome table makes Escape a
	// no-op. Preserve the same priority explicitly before that table.
	if action == "app.interrupt" && m.isCompacting && m.branchSummaryCancel == nil && m.opts.SessionHandle != nil {
		m.opts.SessionHandle.AbortCompaction()
		return nil
	}

	// A background OAuth login (github-copilot / anthropic device/PKCE flow)
	// polls while the main loop stays live and shows a "Ctrl+C to cancel" hint.
	// Esc or Ctrl+C aborts that polling window, matching the hint and the
	// login-dialog abort. This takes priority over the idle clear-editor /
	// double-Esc handling; cancelActiveLogin is a no-op when no login is active.
	if action == "app.interrupt" || action == "app.clear" {
		if m.cancelActiveLogin() {
			m.tuiInst.Render()
			return nil
		}
	}

	// When the slash-autocomplete popup is open, intercept
	// Esc (dismiss) and Enter (accept + maybe submit) before the
	// idle/working state machine sees them. Tab and arrows are
	// dispatched into the editor as normal `actionInsert`s and the
	// editor's HandleInput honors the popup-open guard.
	if m.editor.AutocompleteOpen() {
		switch action {
		case "app.interrupt":
			m.editor.AutocompleteCancel()
			m.tuiInst.Render()
			return nil
		case keySubmit:
			submit := m.editor.AutocompleteAccept()
			if submit {
				text := strings.TrimSpace(m.editor.Text())
				if text != "" {
					m.editor.Clear()
					m.handleSubmit(ctx, text)
				}
			} else {
				// Argument completion accepted (not a slash-name prefix).
				// Autocomplete is synchronous, so Enter lands on the popup
				// instead of the normal submit path; submit here as well
				// when the completed text is a slash command.
				text := strings.TrimSpace(m.editor.Text())
				if text != "" && strings.HasPrefix(text, "/") {
					m.editor.AddToHistory(text)
					m.editor.Clear()
					m.handleSubmit(ctx, text)
				}
			}
			m.tuiInst.Render()
			return nil
		}
	}

	if action == "app.interrupt" && m.branchSummaryCancel != nil {
		m.branchSummaryCancel()
		m.opts.SessionHandle.AbortBranchSummary()
		return nil
	}

	if run, ok := renderedKeyActions[action]; ok {
		run(m, ctx)
		m.tuiInst.Render()
		m.tuiInst.RequestRender()
		return nil
	}
	if command, ok := sessionKeyCommands[action]; ok {
		m.dispatchSlash(command)
		return nil
	}
	switch action {
	case "app.exit":
		// Ctrl+D is also the editor's delete-char-forward: it exits only on
		// an empty editor.
		if !editorEmpty {
			m.editor.HandleInput(data)
			break
		}
		m.requestQuit()
		return nil

	case "app.interrupt":
		if m.isIdle {
			return m.idleEscape(editorEmpty)
		}
		// Esc while working. During an automatic-retry countdown Esc only
		// cancels the retry delay, so the run itself settles with the failed
		// attempt.
		if m.retryCountdownStop != nil && m.opts.SessionHandle != nil {
			m.opts.SessionHandle.AbortRetry()
			return nil
		}
		// The first Esc arms the interrupt; a second one within the window
		// aborts, so a stray Esc never kills a run.
		if !m.interruptArmed() {
			m.armInterrupt()
			return nil
		}
		m.interruptDeadline = time.Time{}
		// Queued steering and follow-up messages go back to the editor
		// before the abort, so
		// they cannot leak into the next, unrelated prompt.
		m.restoreQueuedMessagesToEditor(true)
		// Freeze any tool still mid-execution right now, at abort time,
		// instead of waiting for agent_end. A hung tool (e.g. an ssh that
		// ignores the cancelled context) does not return promptly, so its
		// component would stay ToolStateRunning and keep recomputing the
		// live "Elapsed X.Xs" footer on every keystroke and agent chunk -
		// and once it has scrolled above the viewport that forces a full
		// clearing repaint each time (the flicker users saw after pressing
		// Esc).
		m.finalizeRunningTools()
		// No separate abort banner: the assistant message block's
		// SetTerminalError("aborted", ...) shows "Operation aborted" in
		// error color.
		m.tuiInst.Render()

	case "app.clear":
		// A second Ctrl+C within 500ms exits; otherwise clear the editor and arm the
		// exit timer. Ctrl+C never aborts a running turn: abort is Esc.
		now := time.Now()
		if ctrlCExits(m.lastSigintTime, now) {
			m.lastSigintTime = time.Time{}
			m.requestQuit()
			return nil
		}
		m.lastSigintTime = now
		// Clearing just clears the text and re-renders, with no status.
		m.editor.Clear()
		m.tuiInst.Render()

	case "app.message.copy":
		// app.message.copy copies the active fullscreen selection before
		// falling back to the last assistant message.
		m.handleCopyCommand(true, true)
		return nil

	case "app.suspend":
		m.handleSuspend()

	case keyPaste:
		// Pass the full bracketed paste to the editor, which handles
		// buffering, normalization, and large-paste marker collapse internally.
		// The editor's HandleInput detects \x1b[200~ and
		// routes through handlePasteFlush which inserts markers for pastes
		// >10 lines or >1000 chars.
		m.editor.HandleInput(data)
		m.tuiInst.Render()
		return nil

	case keySubmit:
		text := strings.TrimSpace(m.editor.GetExpandedText())
		if text == "" {
			return nil
		}
		m.editor.AddToHistory(text)
		m.editor.Clear()
		// handleSubmit: commands and `!` bash run
		// immediately, input during compaction queues for after it, and
		// anything else goes through prompt() with streamingBehavior "steer"
		// while a run is active.
		m.handleSubmit(ctx, text)

	case keyNewline:
		m.editor.HandleInput("\n")

	case "app.message.followUp":
		// Alt+Enter: if idle, act as regular submit; if working, enqueue
		// as follow-up (delivered after the agent has no more tool calls
		// or steering messages).
		text := m.editor.GetExpandedText()
		if text == "" {
			break
		}
		m.editor.AddToHistory(text)
		m.editor.SetText("")
		m.editor.ClearPastes()
		// During compaction the text queues for after it; while a run is
		// active it queues as a follow-up; otherwise Alt+Enter acts like
		// Enter.
		trimmed := strings.TrimSpace(text)
		switch {
		case m.isCompacting:
			m.compactionQueue = append(m.compactionQueue, compactionQueuedMessage{text: trimmed, mode: compactionQueueFollowUp})
			m.statusLine.Flash("Queued message for after compaction")
			m.updatePendingMessagesDisplay()
		case m.runStreaming():
			m.promptUserInput(ctx, trimmed, nil, true)
		default:
			m.handleSubmit(ctx, trimmed)
		}
		m.tuiInst.Render()

	case "app.message.dequeue":
		// Alt+Up: restore all queued messages to the editor. This must
		// include messages typed during an in-flight compaction, which
		// live in m.compactionQueue rather than the agent's steering/
		// follow-up queues. updatePendingMessagesDisplay already merges
		// them ("Steering:"/"Follow-up:" lines with the Alt+Up hint), so
		// dequeue must clear the same set or the hint restores nothing.
		n := m.restoreQueuedMessagesToEditor(false)
		switch n {
		case 0:
			m.statusLine.Flash("No queued messages to restore")
		case 1:
			m.statusLine.Flash("Restored 1 queued message to editor")
		default:
			m.statusLine.Flash(fmt.Sprintf("Restored %d queued messages to editor", n))
		}
		m.tuiInst.Render()

	default:
		m.editor.HandleInput(data)
	}
	m.tuiInst.RequestRender()
	return nil
}

// renderedKeyActions are the key actions that run and then render.
var renderedKeyActions = map[string]func(*InteractiveMode, context.Context){
	"app.tools.expand":         func(m *InteractiveMode, _ context.Context) { m.toggleAllTools() },
	"app.editor.external":      (*InteractiveMode).openExternalEditor,
	"app.clipboard.pasteImage": func(m *InteractiveMode, _ context.Context) { m.handleClipboardImagePaste() },
	"app.model.select":         func(m *InteractiveMode, _ context.Context) { m.handleModelPicker() },
	appCommandPalette:          (*InteractiveMode).openCommandPalette,
	"app.thinking.cycle":       func(m *InteractiveMode, _ context.Context) { m.cycleThinkingLevel() },
	"app.thinking.toggle":      func(m *InteractiveMode, _ context.Context) { m.toggleThinkingVisibility() },
	"app.model.cycleForward":   func(m *InteractiveMode, _ context.Context) { m.cycleModel(true) },
	"app.model.cycleBackward":  func(m *InteractiveMode, _ context.Context) { m.cycleModel(false) },
}

// sessionKeyCommands are the key actions that run a slash command.
var sessionKeyCommands = map[string]string{
	"app.session.new":    "/new",
	"app.session.tree":   "/tree",
	"app.session.fork":   "/fork",
	"app.session.resume": "/resume",
}

// idleEscape handles Esc while no run is active: it leaves bash mode, or a
// second Esc within 500ms on an empty editor runs the doubleEscapeAction
// (/tree by default, /fork, or nothing).
func (m *InteractiveMode) idleEscape(editorEmpty bool) error {
	// Esc while in bash mode: clear editor and exit bash mode.
	if m.editor.IsBashMode() {
		m.editor.SetText("")
		m.tuiInst.Render()
		return nil
	}
	// Idle Esc with empty editor arms / fires the
	// double-Esc shortcut. Default action is "tree"; reads from
	// doubleEscapeAction setting ("fork"/"tree"/"none"). 500ms window.
	if editorEmpty {
		dblAction := m.settings().GetDoubleEscapeAction()
		if dblAction != "none" {
			now := time.Now()
			if !m.lastEscapeTime.IsZero() && now.Sub(m.lastEscapeTime) < 500*time.Millisecond {
				m.lastEscapeTime = time.Time{}
				switch dblAction {
				case "fork":
					m.dispatchSlash("/fork")
				default: // "tree"
					m.dispatchSlash("/tree")
				}
				return nil
			}
			m.lastEscapeTime = now
		}
	}
	return nil
}

// expandSkillCommand checks if prompt starts with "/skill:name" and if so,
// expands it to the skill's XML block. Returns (expanded, true) on match.
// Delegates to ExpandSkillCommand with the session's loaded skills.
func (m *InteractiveMode) expandSkillCommand(prompt string) (string, bool) {
	if len(m.opts.Skills) == 0 {
		return "", false
	}
	return ExpandSkillCommand(prompt, m.opts.Skills)
}

// resolvableSlashCommand reports whether prompt is a slash command that
// resolves to a builtin command. These are local/UI
// dispatches that run immediately even during compaction. Prompt templates, skill commands, and
// unresolved slashes are not resolvable here: they expand into model prompts
// and stay queued during compaction.
func (m *InteractiveMode) resolvableSlashCommand(prompt string) bool {
	if !strings.HasPrefix(prompt, "/") {
		return false
	}
	name, _ := parseSlashLine(prompt)
	_, ok := m.slashRegistry.Resolve(name)
	return ok
}

// restoreQueuedMessagesToEditor moves every queued message (the agent's
// steering and follow-up queues plus messages queued during compaction) into
// the editor ahead of its current text, and reports how many it restored.
// With abort it then aborts the active run.
func (m *InteractiveMode) restoreQueuedMessagesToEditor(abort bool) int {
	var steering, followUps []agent.AgentMessage
	if m.agent != nil {
		steering, followUps = m.agent.PendingMessages()
	}
	steeringTexts, followUpTexts := collectQueuedTexts(steering, followUps, m.compactionQueue)
	texts := slices.Concat(steeringTexts, followUpTexts)
	if len(texts) > 0 {
		if m.agent != nil {
			m.agent.ClearAllQueues()
		}
		m.compactionQueue = nil
		combined := strings.Join(texts, "\n\n")
		if current := m.editor.Text(); strings.TrimSpace(current) != "" {
			combined += "\n\n" + current
		}
		m.editor.SetText(combined)
	}
	m.updatePendingMessagesDisplay()
	if abort {
		m.abortRun(m.runCtx)
	}
	return len(texts)
}

// abortRun cancels the active run, its retry delay and compaction included,
// and arms a fresh abort context for the next run.
func (m *InteractiveMode) abortRun(ctx context.Context) {
	m.abortFn()
	if ctx == nil {
		ctx = context.Background()
	}
	m.abortCtx, m.abortFn = context.WithCancel(ctx)
}

// settleActiveRun aborts the active run and waits until it settles, before
// session replacement touches the session, so the aborted turn persists to the
// outgoing session rather than the one replacing it. It runs on the owner
// loop, so while it waits it keeps handling Session events and posted UI
// tasks, which the settling run needs. It fails only when the mode shuts
// down first; the caller must then not replace the session.
func (m *InteractiveMode) settleActiveRun() error {
	m.queueMu.Lock()
	settled := m.turnSettled
	m.queueMu.Unlock()
	if settled == nil {
		return nil
	}
	m.abortRun(m.runCtx)
	done := context.Background().Done()
	if m.runCtx != nil {
		done = m.runCtx.Done()
	}
	for {
		select {
		case <-settled:
			return nil
		case <-done:
			return errors.New("interactive mode shut down before the active run settled")
		case ev, ok := <-m.eventCh:
			if !ok {
				m.eventCh = nil
				continue
			}
			m.handleAgentEvent(ev)
		case fn := <-m.uiTaskCh:
			fn()
		case <-m.renderWakeCh:
			m.runScheduledRender()
		}
	}
}

// runStreaming reports whether a run is active.
func (m *InteractiveMode) runStreaming() bool {
	return m.turnActive.Load() || (m.agent != nil && m.agent.IsStreaming())
}

func (m *InteractiveMode) hasActiveAgentTurn() bool {
	// A turn is active from the synchronous commit in runPromptTurn until the
	// run goroutine returns, not merely while the provider streams tokens.
	// Agent.streaming flips true only once the goroutine reaches runLoop, so a
	// submit in the window before that (goroutine dispatch, before_agent_start
	// hook, pre-prompt auto-compaction) saw IsStreaming()==false and started a
	// second concurrent turn: persisting back-to-back assistant messages that
	// break tool_use/tool_result pairing ("tool_use_id ... has no corresponding
	// tool_use"). turnActive marks exactly the goroutine's lifetime, so it stays
	// true across that window yet reads false once the turn ends (unlike the
	// runOnMain-reset isIdle, which can be stale-false: see
	// TestPendingDisplay_EnterAfterAgentStoppedStartsNewTurn).
	if m.turnActive.Load() || m.isCompacting {
		return true
	}
	if m.agent == nil {
		return false
	}
	return m.agent.IsStreaming()
}

// handleSubmit fires before_agent_start, then starts the agent in a goroutine.

// syncEditorFocusWithSearch toggles the editor's Focusable flag: while fullscreen transcript search holds focus the editor
// emits no hardware-cursor marker, so the cursor lands in the search box.
func (m *InteractiveMode) syncEditorFocusWithSearch() {
	m.editor.Focused = !m.tuiInst.IsSearchFocused()
}
