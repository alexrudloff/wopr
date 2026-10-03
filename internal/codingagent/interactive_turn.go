package codingagent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/tui"
)

func (m *InteractiveMode) handleSubmit(ctx context.Context, prompt string) {
	m.handleSubmitWithImages(ctx, prompt, nil)
}

func (m *InteractiveMode) handleSubmitWithImages(ctx context.Context, prompt string, images []ai.ImageContent) {
	if m.routingSavePending {
		// Tab landed here: this is the choice to keep.
		m.saveRouting()
	}
	// Defensive drain for messages restored after a failed compaction-queue
	// delivery. Normal compaction completion flushes immediately, including the
	// WillRetry path that steers into the imminent retry turn.
	if !m.isCompacting && len(m.compactionQueue) > 0 {
		m.flushCompactionQueue(ctx, false)
	}

	// Queue model-bound inputs during compaction, but let local
	// slash commands and bash run immediately. Only plain prompts, prompt
	// templates, and skill commands (which expand into model input) are
	// queued. The registry holds the builtins (/model, /session, /fork, /tree,
	// /new, ...), so a prompt that resolves in
	// the registry is a local/UI dispatch that must run now, even mid-compaction.
	// Queueing it (as prior wopr did) showed "/session" as a steering message
	// and stalled it until compaction finished.
	if m.isCompacting && !strings.HasPrefix(prompt, "!") && !m.resolvableSlashCommand(prompt) {
		m.compactionQueue = append(m.compactionQueue, compactionQueuedMessage{text: prompt, images: images, mode: compactionQueueSteer})
		m.statusLine.Flash("Queued message for after compaction")
		m.editor.SetText("")
		// The queued message stays
		// visible in the pending container, not just a transient status.
		m.updatePendingMessagesDisplay()
		return
	}

	// `!cmd` and `!!cmd` bash-prefix interception. Runs
	// the command directly via the executor; output renders inline
	// as a `BashExecutionBlock` and persists to the session as a
	// `bash_execution` entry. `!!cmd` (double-bang) sets
	// excludeFromContext=true so the LLM doesn't see the entry on
	// its next turn.
	if strings.HasPrefix(prompt, "!") {
		exclude := strings.HasPrefix(prompt, "!!")
		var cmd string
		if exclude {
			cmd = strings.TrimSpace(prompt[2:])
		} else {
			cmd = strings.TrimSpace(prompt[1:])
		}
		if cmd == "" {
			return // bare `!`: ignore
		}
		// Keep the text and warn instead of starting a second
		// command, whose completion would mark the UI idle under the first.
		if m.bashCancel != nil {
			m.showWarning("A bash command is already running. Press Esc to cancel it first.")
			m.editor.SetText(prompt)
			return
		}
		m.handleBashCommand(ctx, cmd, exclude)
		return
	}

	// Builtin commands and their aliases run
	// immediately, even while a run streams: they are handled before anything
	// is queued.
	if m.resolvableSlashCommand(prompt) {
		m.dispatchSlash(prompt)
		return
	}
	m.promptUserInput(ctx, prompt, images, false)
}

// promptUserInput handles a prompt after command dispatch, including steer
// and follow-up queueing. Skill commands and prompt templates expand first;
// then the text queues into the active run as a steering message, or as a
// follow-up with followUp, or starts a new turn when no run is active. An
// unresolved `/foo` is ordinary user text: it goes to the model rather than
// reporting an unknown command.
func (m *InteractiveMode) promptUserInput(ctx context.Context, text string, images []ai.ImageContent, followUp bool) {
	// Finished background work leaves the strip with the next prompt.
	m.bg.since = time.Now()
	if _, secret := router.ScanSecrets(text); secret {
		// Sent as typed: the model may need it. The user should know where it goes.
		m.showToast("warning", "That looks like a secret", "It goes to the model's provider and is saved in this session's file. Rotate it if it matters.")
	}
	streaming := m.runStreaming()
	// Expand skill commands first, then prompt templates on the result.
	if expanded, ok := m.expandSkillCommand(text); ok {
		text = expanded
	}
	if expanded, ok := ExpandPromptTemplate(text, withBuiltinPromptCommands(m.promptTemplates)); ok {
		text = expanded
	}
	if streaming && m.enqueueIfTurnActive(func() {
		if followUp {
			m.followUpMessageWithImages(text, images)
		} else {
			m.steerMessageWithImages(text, images)
		}
	}) {
		return
	}
	m.runPromptTurnWithImages(ctx, text, images)
}

// runPromptTurnWithImages renders the user message and starts a turn for
// prompt, which input handlers and expansion have already processed.
func (m *InteractiveMode) runPromptTurnWithImages(ctx context.Context, prompt string, images []ai.ImageContent) {
	// Show user message in chat. Rendered as a styled
	// box (UserMessageBlock) instead of the prior markdown blockquote
	// (`> **You:** ...`). The blockquote rendering was visually
	// ambiguous: any LLM output containing literal `> ` lines (e.g.
	// when reciting documentation that quotes things) rendered with
	// the same `│ ` bar as the user's own messages, making it look
	// like the LLM was speaking as the user. The styled-box approach
	// uses raw ANSI bg paint, which markdown output cannot mimic by
	// construction.
	// Spacer before the user block, using
	// the chat container child count instead of a separate first-message flag.
	// The AssistantMessageBlock also adds a leading spacer when it has
	// visible content, so the assistant text gets its own separation.
	if !m.chatContainer.IsEmpty() {
		m.appendToChat(tui.NewSpacer(1))
	}
	m.appendToChat(m.newUserMessageBlock(prompt))
	m.skipNextUserMessageText = prompt
	m.tuiInst.Render()

	m.runTurnWithImages(ctx, prompt, images, func(runCtx context.Context) ([]agent.AgentMessage, error) {
		content := promptContent(prompt, images)
		autoResize := true
		if m.opts.SettingsManager != nil {
			autoResize = m.opts.SettingsManager.GetImageAutoResize()
		}
		content = NormalizePromptContent(content, autoResize, m.agent.Model())
		return m.agent.SendContent(runCtx, content)
	})
}

// runTurnWithImages owns the turn lifecycle shared by every entry point:
// idle/turnActive bookkeeping, the run goroutine, error surfacing, and settle.
// start performs the low-level agent call that seeds the run. prompt is empty
// for turns not seeded by typed input.
func (m *InteractiveMode) runTurnWithImages(ctx context.Context, prompt string, images []ai.ImageContent, start func(context.Context) ([]agent.AgentMessage, error)) {
	m.isIdle = false
	// runGen identifies this run to its own UI cleanup, which runs later on
	// the owner loop: a cleanup that finds a newer run leaves that run's
	// busy state alone.
	m.runGen++
	gen := m.runGen
	m.queueMu.Lock()
	m.turnActive.Store(true)
	m.turnSettled = make(chan struct{})
	m.queueMu.Unlock()
	m.workStart = time.Now()
	m.statusLine.SetWorking(true)
	// The run's cancellation is Esc's abort context at the time the run starts;
	// the main loop replaces m.abortCtx after an abort, so the goroutine never
	// reads the field.
	runCtx := m.abortCtx

	go func() {
		defer func() {
			// Turn-end state (isIdle, workStart, statusLine, loaders) and the
			// final flush/render are read/rendered by the main input loop, so
			// apply them there instead of on this goroutine, which races
			// keystroke handling (resolveOutcome reads isIdle every keystroke).
			// runCtx so cleanup still applies after an aborted turn; it is
			// dropped only when the whole session is shutting down.
			m.runOnMain(m.runCtx, func() {
				if m.runGen == gen {
					m.isIdle = true
					m.workStart = time.Time{}
					m.statusLine.SetWorking(false)
					m.stopWorkingLoader() // belt-and-suspenders: ensure loader is removed on abort
				}
				// Any `!cmd` invocations queued during the
				// agent's turn now promote to chat.
				m.flushPendingBashBlocks()
				m.updatePendingMessagesDisplay() // clear stale queue indicators
				m.tuiInst.Render()
				if m.runGen == gen {
					m.maybeContinueGoal()
				}
			})
			// Follow-up messages are now drained by the agent loop
			// itself (via followUpQueue). No explicit drain needed here.
			// The run has fully settled here (retries, recovery, compaction,
			// and queued input drained by runAgentPrompt and settleTurn).
			// Interactive drives its own settlement, so it notifies the
			// Session (cache warmer) directly here.
			if m.opts.SessionHandle != nil {
				m.opts.SessionHandle.OnAgentSettled()
			}
		}()
		// Pre-prompt compaction check: before sending the new user message,
		// compact if the prior context already exceeds the threshold, including
		// after an aborted turn. A custom-message seed skips this check.
		if prompt != "" {
			m.checkPromptCompaction(runCtx)
		}
		m.agent.SetSystemPrompt(m.currentSystemPrompt())

		// The user prompt, assistant messages, and tool results are all persisted
		// incrementally by the OnMessagePersist hook (wired in coding.NewSession),
		// driven by the agent's message_end events. This single persistence site
		// keeps a mid-turn kill from losing the turn.
		_, err := m.runAgentPrompt(runCtx, start)
		err = m.settleTurn(runCtx, err)
		// Only the run's own cancellation (Esc or shutdown) is silent; any
		// other error, a deadline from elsewhere included, is shown.
		if err != nil && runCtx.Err() == nil {
			m.showTurnError(err)
		}
		m.runOnMain(m.runCtx, func() { m.tuiInst.Render() })
	}()
}

// checkPromptCompaction runs the Session's pre-prompt compaction check. A
// failure is shown, and the prompt is still sent; compaction reports its own
// failures through compaction_end.
func (m *InteractiveMode) checkPromptCompaction(ctx context.Context) {
	if m.opts.SessionHandle == nil {
		return
	}
	if err := m.opts.SessionHandle.CheckPromptCompaction(ctx); err != nil && ctx.Err() == nil {
		m.runOnMain(m.runCtx, func() {
			m.showError(err.Error())
		})
	}
}

// runAgentPrompt runs start to settlement through the Session's run loop:
// automatic retry, overflow and length recovery,
// threshold compaction, and continuation from queued input. Without a Session
// (a mode constructed directly in a unit test) there is no retry or
// compaction, and only queued input continues the run.
func (m *InteractiveMode) runAgentPrompt(ctx context.Context, start func(context.Context) ([]agent.AgentMessage, error)) ([]agent.AgentMessage, error) {
	if m.opts.SessionHandle != nil {
		return m.opts.SessionHandle.RunAgentPrompt(ctx, start)
	}
	messages, err := start(ctx)
	for err == nil && ctx.Err() == nil && m.agent.HasQueuedMessages() {
		messages, err = m.agent.Continue(ctx)
	}
	return messages, err
}

// settleTurn ends the run: input queued
// after the run loop's last check starts another run instead of waiting in the
// queue. The check and the turnActive clear happen under queueMu, the lock
// every main-loop path holds while it decides between queueing into this run
// and starting a new one, so no message can land in a queue nothing drains.
// It returns the error of the last run.
func (m *InteractiveMode) settleTurn(ctx context.Context, err error) error {
	for {
		m.queueMu.Lock()
		if err != nil || ctx.Err() != nil || !m.agent.HasQueuedMessages() {
			m.turnActive.Store(false)
			if m.turnSettled != nil {
				close(m.turnSettled)
				m.turnSettled = nil
			}
			m.queueMu.Unlock()
			return err
		}
		m.queueMu.Unlock()
		_, err = m.runAgentPrompt(ctx, m.agent.Continue)
	}
}

// submitInitialMessages sends each message as its own prompt after the
// previous run settles. It runs off the owner loop and
// stops when ctx ends.
func (m *InteractiveMode) submitInitialMessages(ctx context.Context, messages []string) {
	for _, message := range messages {
		if m.waitForIdle(ctx) != nil {
			return
		}
		submitted := make(chan struct{})
		if m.postToMain(ctx, func() {
			defer close(submitted)
			m.handleSubmit(ctx, message)
		}) != nil {
			return
		}
		select {
		case <-submitted:
		case <-ctx.Done():
			return
		}
	}
}

// waitForIdle blocks until no run is active, resolving when the run settles
// rather than polling. It returns early only when ctx ends.
func (m *InteractiveMode) waitForIdle(ctx context.Context) error {
	m.queueMu.Lock()
	settled := m.turnSettled
	m.queueMu.Unlock()
	if settled == nil {
		return nil
	}
	select {
	case <-settled:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// showTurnError surfaces a failed run on the main loop.
func (m *InteractiveMode) showTurnError(err error) {
	// Show the error text; never start a login from it.
	errStr := err.Error()
	switch {
	case errors.Is(err, agent.ErrNoModelSelected):
		// No model / not logged in: show login + /model guidance
		// instead of a bare error.
		m.runOnMain(m.runCtx, func() {
			m.appendChatBlock(tui.NewText("\033[33m" + FormatNoModelSelectedMessage() + "\033[0m"))
			m.tuiInst.Render()
		})
	default:
		m.runOnMain(m.runCtx, func() {
			m.showError(errStr)
		})
	}
}

// handleBashCommand runs a `!cmd` (or `!!cmd` excluded) directly via
// the bash executor.
//
// Three behaviors:
//
//  1. While agent is streaming: queue the bash component to a pending
//     buffer; flush after agent_end so message ordering is preserved.
//  2. While agent is idle: render inline immediately, run the command,
//     persist the result as a `bash_execution` session entry.
//  3. Esc cancels the running command (parallels LLM abort).
func (m *InteractiveMode) handleBashCommand(ctx context.Context, command string, excludeFromContext bool) {
	// Pending-while-streaming queue: when agent is mid-turn, defer
	// the bash component into pendingBashBlocks; the goroutine that
	// runs the command publishes its block to chat after the agent's
	// turn finishes (handled by flushPendingBashBlocks below).
	deferred := !m.isIdle

	block := tui.NewBashExecutionBlock(command, excludeFromContext)

	// Apply the current global expansion state to blocks created after Ctrl+O.
	m.toolMu.Lock()
	m.bashOrder = append(m.bashOrder, block)
	if m.toolsExpanded {
		block.SetExpanded(true)
	}
	m.toolMu.Unlock()

	if deferred {
		m.pendingBashBlocksMu.Lock()
		m.pendingBashBlocks = append(m.pendingBashBlocks, block)
		m.pendingBashBlocksMu.Unlock()
	} else {
		m.appendToChat(block)
		m.tuiInst.Render()
	}

	// Mark bash as running so Esc can cancel it. resolveOutcome maps
	// Esc while busy to outcomeAbort. The bash context derives from m.abortCtx,
	// so cancellation reaps the process group.
	if !deferred {
		m.isIdle = false
	}
	bashCtx, bashCancel := context.WithCancel(m.abortCtx)
	prev := m.bashCancel
	m.bashCancel = bashCancel

	go func() {
		defer func() {
			// The process context is cancelled off-main (it only reaps the
			// process group). The shared fields it restores are touched on the
			// main loop, after the final render posted below (uiTaskCh is FIFO).
			bashCancel()
			m.runOnMain(m.runCtx, func() {
				m.bashCancel = prev
				if !deferred {
					m.isIdle = true
				}
			})
		}()

		// Apply the shell command prefix and run through local bash; the block
		// and the session record keep the command as typed.
		resolvedCommand := command
		if prefix := m.settings().GetCommandPrefix(); prefix != "" {
			resolvedCommand = prefix + "\n" + command
		}
		operations := tools.NewLocalBashOperations(m.settings(), filepath.Join(m.opts.AgentDir, "bin"))
		res, err := tools.ExecuteBashWithOperations(bashCtx, resolvedCommand, m.opts.CWD, operations, tools.BashExecOptions{
			// Streamed output must not be lost, so post reliably (backpressure)
			// onto the main loop rather than mutating the block + rendering from
			// this goroutine, which races keystroke handling. runCtx (not bashCtx)
			// so a cancelled bash still delivers output already produced.
			OnChunk: func(chunk string) {
				m.runOnMain(m.runCtx, func() {
					block.AppendOutput(chunk)
					m.tuiInst.Render()
				})
			},
		})
		if err != nil {
			// The block completes without an exit code, the failure
			// is shown, and nothing is recorded.
			m.runOnMain(m.runCtx, func() {
				block.SetComplete(nil, false, false)
				if deferred {
					m.flushPendingBashBlocks()
				}
				m.showError("Bash command failed: " + err.Error())
			})
			return
		}
		m.finishUserBash(block, command, excludeFromContext, res, deferred)
	}()
}

// finishUserBash records a user bash result in the session and completes its
// block on the main loop.
func (m *InteractiveMode) finishUserBash(block *tui.BashExecutionBlock, command string, excludeFromContext bool, res tools.BashResult, deferred bool) {
	// Persist to the session off the main loop (I/O); capture only a
	// warning to surface on the loop.
	var persistWarn string
	if m.currentSession() != nil {
		if _, perr := m.currentSession().AppendBashExecution(
			command, res.Output, res.ExitCode, res.Cancelled,
			res.Truncated, res.FullOutputPath, excludeFromContext,
		); perr != nil {
			persistWarn = perr.Error()
		}
	}
	// Apply the terminal block state on the main loop, after every OnChunk
	// post, so the block's output, completion, promotion, and render are
	// single-threaded with keystrokes.
	m.runOnMain(m.runCtx, func() {
		block.SetCompleteWithOutput(res.ExitCode, res.Cancelled, res.Truncated, res.Output, res.FullOutputPath)
		// A deferred block is appended after the current chat content, which
		// preserves the order observed by the user.
		if deferred {
			m.flushPendingBashBlocks()
		}
		if persistWarn != "" {
			m.appendToChat(tui.NewText("\033[33mbash session persist warning: " + persistWarn + "\033[0m"))
		}
		m.tuiInst.Render()
	})
}

// flushPendingBashBlocks promotes deferred bash components from the
// pending buffer to the chat container. Called when a bash goroutine
// finishes (and again from agent_end via runner so any blocks queued
// during streaming surface promptly). Idempotent: safe to call when
// there's nothing pending.
func (m *InteractiveMode) flushPendingBashBlocks() {
	m.pendingBashBlocksMu.Lock()
	pending := m.pendingBashBlocks
	m.pendingBashBlocks = nil
	m.pendingBashBlocksMu.Unlock()
	for _, b := range pending {
		m.appendToChat(b)
	}
	if len(pending) > 0 {
		m.tuiInst.Render()
	}
}

// dispatchSlash routes a `/cmd …` line through the registry. Builtins run on
// the input loop; command handlers start their awaited work off-loop
// so Promise-equivalent UI calls can post mutations back to that loop.
