package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/goal"

	"golang.org/x/term"
)

// ─── Print Mode ───────────────────────────────────────────────────────────────

// errPrintModeHandled signals that runPrintMode already wrote the error to
// stderr (no "error: " prefix). The caller should
// os.Exit(1) without re-printing.
var errPrintModeHandled = fmt.Errorf("print mode: error already written")

// signalExitError reports that print mode stopped because the process received
// a termination signal rather than because the run failed.
//
// Print mode disposes its runtime and then exits 128+signum (129 for SIGHUP,
// 143 for SIGTERM, 130 for SIGINT). Callers distinguish "a timeout or
// supervisor killed the run" from "the run failed" by exit code, so no
// internal cancellation error is printed. Recording the signal and returning
// normally, rather than exiting from the handler, keeps the deferred runtime
// teardown before the exit.
type signalExitError struct {
	signal syscall.Signal
}

func (e *signalExitError) Error() string {
	return fmt.Sprintf("terminated by signal %s", e.signal)
}

func (e *signalExitError) ExitCode() int {
	return 128 + int(e.signal)
}

// receivedTerminationSignal records the termination signal that stopped this
// process, if any. It is set by the process-wide SIGTERM handler in main and by
// print mode's own handler, so a signal arriving before print mode starts (for
// example while resources are still loading) reports the same exit code as one
// arriving mid-run.
var receivedTerminationSignal atomic.Int32

// printModeRuntime is what main resolved for the print-mode session: the
// runtime services and the options the session starts with.
type printModeRuntime struct {
	Services    *coding.Services
	Session     coding.SessionStartOptions
	SessionName string
}

// printModeOptions configures runPrintMode.
type printModeOptions struct {
	// Mode is "text" for the final response only, "json" for all events.
	Mode string
	// Messages are sent one by one after the initial message.
	Messages []string
	// InitialMessage is the first message to send (may contain @file content).
	InitialMessage string
	// InitialImages are attached to the initial message.
	InitialImages []ai.ImageContent
	// GTW starts the session in Global Thermonuclear War.
	GTW bool
	// Deadline is the run's time budget; zero has none.
	Deadline time.Duration

	// stdout and stderr default to the process's raw stdout and stderr.
	stdout io.Writer
	stderr io.Writer
	// convertEvent defaults to rpcAgentEvent.
	convertEvent func(agent.AgentEvent) ([]any, error)
}

// runPrintMode runs print (single-shot) mode: it sends the prompts and writes
// the result. It serves both `wopr -p "prompt"` (text: the final response
// only) and `wopr --mode json "prompt"` (every session event as one JSON line,
// after the session header) from one function, so both modes build the same session from the same
// options. Normal completion drains output; signal termination disposes the
// runtime without waiting for stdout, then the caller exits with 128+signum.
func runPrintMode(ctx context.Context, host printModeRuntime, opts printModeOptions) (err error) {
	// Take over os.Stdout in non-TTY contexts so any stdout
	// writes from tools / agent infra are redirected to
	// stderr instead of corrupting the print-mode output framing.
	if opts.stdout == nil {
		if !term.IsTerminal(int(os.Stdout.Fd())) {
			if err := codingagent.TakeOverStdout(); err == nil {
				defer codingagent.RestoreStdout()
			}
		}
		opts.stdout = codingagent.RawStdoutWriter()
	}
	if opts.stderr == nil {
		opts.stderr = os.Stderr
	}
	if opts.convertEvent == nil {
		opts.convertEvent = rpcAgentEvent
	}
	// Print mode owns SIGINT: there's no interactive editor to deliver
	// Ctrl+C as a byte, so the signal is the only abort path. SIGTERM and
	// SIGHUP are owned here too so the exit code reports the signal
	// (128+signum after disposing the runtime).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	intCh := make(chan os.Signal, 1)
	termSignals := []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	if runtime.GOOS != "windows" {
		termSignals = append(termSignals, syscall.SIGHUP)
	}
	signal.Notify(intCh, termSignals...)
	defer signal.Stop(intCh)
	go func() {
		select {
		case sig := <-intCh:
			if sysSig, ok := sig.(syscall.Signal); ok {
				receivedTerminationSignal.Store(int32(sysSig))
			}
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() {
		// A termination signal outranks whatever error the cancelled run
		// surfaced, so the caller reports the signal's exit code instead of a
		// generic failure.
		if sig := receivedTerminationSignal.Load(); sig != 0 {
			err = &signalExitError{signal: syscall.Signal(sig)}
		}
	}()

	sess, err := coding.StartSession(host.Services, host.Session)
	if err != nil {
		return fmt.Errorf("construct session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	sess.SetDeadline(opts.Deadline)
	if opts.GTW {
		spec, err := sess.StartWar()
		if err != nil {
			return fmt.Errorf("global thermonuclear war: %w", err)
		}
		if opts.Mode != "json" {
			_, _ = fmt.Fprintf(opts.stderr, "Global Thermonuclear War: %s at max thinking, war council on\n", spec)
		}
	}

	// Persist --name to session_info so the display name survives resume.
	// This runs when the session is created, before any mode starts.
	if host.SessionName != "" {
		if err := sess.SetSessionName(host.SessionName); err != nil {
			return fmt.Errorf("set session name: %w", err)
		}
	}

	// Line 1 of JSON output is always the session header, matching the
	// session JSONL layout.
	if opts.Mode == "json" {
		if hdr := sess.Inner().Header(); hdr.ID != "" {
			writeJSONLine(opts.stdout, hdr)
		}
	}

	// Subscribe before prompting so no event of the run is missed. Every
	// event is consumed even when it is not written: forwardAgentEvents pushes
	// every agent event into the channel, and a consumer that stops reading
	// blocks the agent's emit and deadlocks the run. A JSON conversion failure
	// fails the run, but the loop keeps draining until Close ends the channel.
	var convertErr error
	eventsDone := make(chan struct{})
	go func() {
		defer close(eventsDone)
		for ev := range sess.Events() {
			if coding.AcknowledgeEvent(ev) || opts.Mode != "json" || convertErr != nil {
				continue
			}
			converted, err := opts.convertEvent(ev)
			if err != nil {
				convertErr = fmt.Errorf("convert session event: %w", err)
				cancel()
				continue
			}
			for _, out := range converted {
				writeJSONLine(opts.stdout, out)
			}
		}
	}()
	// Ordered teardown: closing the session ends the event channel and the
	// consumer exits. A failed
	// run reports the error's own text on stderr, exit 1. A conversion failure is the
	// cause of the cancellation the prompt then reports, so it wins. A
	// termination signal stays quiet and reports its exit code instead.
	var runErr error
	defer func() {
		_ = sess.Close()
		// A signal disposes the runtime and exits without flushing stdout.
		// The process owns any blocked stdout write on
		// this path; waiting for it would make a full client pipe prevent exit.
		// Ordinary cancellation still drains output. Read convertErr only after
		// the consumer has joined, never while it may still be converting.
		select {
		case <-eventsDone:
		case <-ctx.Done():
			if receivedTerminationSignal.Load() != 0 {
				return
			}
			<-eventsDone
		}
		if convertErr != nil {
			runErr = convertErr
		}
		if runErr != nil && receivedTerminationSignal.Load() == 0 {
			_, _ = fmt.Fprintln(opts.stderr, runErr.Error())
			err = errPrintModeHandled
		}
	}()

	if opts.InitialMessage != "" {
		runErr = sendPrintInput(ctx, sess, opts.InitialMessage, opts.InitialImages, opts.stderr)
	}
	for _, message := range opts.Messages {
		if runErr != nil {
			break
		}
		runErr = sendPrintInput(ctx, sess, message, nil, opts.stderr)
	}
	// Send returns while the session may still be dispatching the run's tail
	// (event writes). Wait for that before shutdown, or JSON output is cut
	// short.
	if err := sess.FlushEvents(ctx); err != nil && runErr == nil && ctx.Err() == nil {
		runErr = fmt.Errorf("flush session events: %w", err)
	}
	if runErr != nil {
		return nil // reported by the teardown above
	}
	if opts.Mode != "text" {
		// The reply is in the event stream; only a failed run is reported.
		return printModeFailure(sess.Messages(), opts.stderr)
	}
	return writePrintModeResult(sess.Messages(), opts.stdout, opts.stderr)
}

// sendPrintInput sends one message: a `/goal <objective>` runs the goal
// loop headless until the audit accepts it or a cap pauses it, anything
// else is a prompt.
func sendPrintInput(ctx context.Context, sess *coding.Session, text string, images []ai.ImageContent, stderr io.Writer) error {
	if args, ok := strings.CutPrefix(strings.TrimSpace(text), "/goal "); ok {
		return runPrintGoal(ctx, sess, args, stderr)
	}
	err := sendPrintPrompt(ctx, sess, text, images)
	return err
}

// runPrintGoal sets the goal and sends each automatic turn as a prompt. A
// met goal is reported on stderr; a paused one fails the run.
func runPrintGoal(ctx context.Context, sess *coding.Session, args string, stderr io.Writer) error {
	objective, caps, err := goal.ParseArgs(args)
	if err != nil {
		return err
	}
	msg, err := sess.SetGoal(objective, caps)
	for err == nil {
		text, _ := msg.Custom["content"].(string)
		if err = sendPrintPrompt(ctx, sess, text, nil); err != nil {
			break
		}
		step := sess.GoalNext(ctx)
		switch {
		case step.Event == "done":
			_, _ = fmt.Fprintln(stderr, "goal met: "+firstLine(step.Text))
			return nil
		case step.Event == "paused":
			return errors.New("goal paused: " + step.Text)
		case step.Message == nil:
			return nil
		}
		msg = *step.Message
	}
	return err
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// sendPrintPrompt is print and JSON mode's input boundary.
func sendPrintPrompt(ctx context.Context, sess *coding.Session, text string, images []ai.ImageContent) error {
	_, err := sess.SendContent(ctx, coding.BuildUserContent(text, images))
	return err
}

// writePrintModeResult writes text mode's result: the session's last message,
// when it is an assistant message. It reads the session's messages at the end
// rather than the messages a prompt produced, so compaction and retries during the run cannot hide the answer,
// and earlier assistant messages of the run (tool-use narration) are not
// printed. An error or aborted message goes to stderr and fails the run.
func writePrintModeResult(messages []agent.AgentMessage, stdout, stderr io.Writer) error {
	if err := printModeFailure(messages, stderr); err != nil {
		return err
	}
	last := lastAssistant(messages)
	if last == nil {
		return nil
	}
	for _, block := range last.Content {
		if text, ok := block.(ai.TextContent); ok {
			_, _ = io.WriteString(stdout, text.Text+"\n")
		}
	}
	return nil
}

// printModeFailure reports a run whose last reply ended in an error or an
// abort on stderr and returns errPrintModeHandled, so every mode exits
// nonzero and scripts and benchmark harnesses can see the failure.
func printModeFailure(messages []agent.AgentMessage, stderr io.Writer) error {
	last := lastAssistant(messages)
	if last == nil || last.StopReason != ai.StopReasonError && last.StopReason != ai.StopReasonAborted {
		return nil
	}
	errMsg := last.ErrorMessage
	if errMsg == "" {
		errMsg = "Request " + string(last.StopReason)
	}
	_, _ = fmt.Fprintln(stderr, errMsg)
	return errPrintModeHandled
}

// lastAssistant is the run's last reply, skipping any notices after it.
func lastAssistant(messages []agent.AgentMessage) *agent.AssistantMessage {
	for _, message := range slices.Backward(messages) {
		if a := message.Assistant; a != nil {
			return a
		}
		if message.User != nil || message.ToolResult != nil {
			return nil
		}
	}
	return nil
}
