package codingagent

// interactive_crash.go wires the crash log (crash_log.go) into interactive
// mode: UI panics and fatal create,
// resume, and import failures are recorded, and the next start announces them.

import (
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"time"

	"github.com/alexrudloff/wopr/tui"
)

// ErrInteractiveCrashed reports that Run recovered a crash it already
// printed and recorded; the caller exits 1 without printing it again.
var ErrInteractiveCrashed = errors.New("interactive mode crashed")

// showCrashNotice announces the newest unannounced crash.
func (m *InteractiveMode) showCrashNotice() {
	if m.opts.AgentDir == "" {
		return
	}
	if crash, ok := TakeUnnotifiedCrash(CrashLogPath(m.opts.AgentDir), time.Now()); ok {
		m.showWarning(crashNotice(crash))
	}
}

// crashMessage returns an error's message, or its type when the message is
// empty, and the formatted value otherwise.
func crashMessage(value any) string {
	if err, ok := value.(error); ok {
		if message := err.Error(); message != "" {
			return message
		}
		return fmt.Sprintf("%T", err)
	}
	return fmt.Sprint(value)
}

// crashSessionFile returns the current session file, if any.
func (m *InteractiveMode) crashSessionFile() string {
	if session := m.currentSession(); session != nil {
		return session.Path()
	}
	return ""
}

// recordCrash persists a crash so the next start can point at /bug. It reports
// false when nothing was written.
func (m *InteractiveMode) recordCrash(kind string, value any, stack string) bool {
	if m.opts.AgentDir == "" {
		return false
	}
	cwd := m.opts.CWD
	if session := m.currentSession(); session != nil && session.CWD() != "" {
		cwd = session.CWD()
	}
	_, ok := RecordCrash(CrashInput{
		Kind:        kind,
		Message:     crashMessage(value),
		Stack:       stack,
		SessionFile: m.crashSessionFile(),
		CWD:         cwd,
		Version:     m.opts.AppVersion,
	}, CrashLogPath(m.opts.AgentDir), time.Now())
	return ok
}

// handleFatalRuntimeError reports a runtime replacement failure and asks the
// input loop to exit with status 1 after terminal restoration.
func (m *InteractiveMode) handleFatalRuntimeError(prefix string, value any) error {
	message := crashMessage(value)
	stack := string(debug.Stack())
	m.showError(prefix + ": " + message)
	if m.recordCrash("fatal_error", value, stack) {
		m.appendChatBlock(tui.NewText(dim(crashReportInstructions(m.crashSessionFile()))))
	}
	m.fatalRuntime.Store(true)
	m.requestExit.Store(true)
	return fmt.Errorf("%w: %s: %s", ErrInteractiveCrashed, prefix, message)
}

func (m *InteractiveMode) requestedExitError() error {
	if m.fatalRuntime.Load() {
		return ErrInteractiveCrashed
	}
	return nil
}

// uncaughtCrash reports a crash recovered on the UI goroutine after the
// terminal has been restored.
func (m *InteractiveMode) uncaughtCrash(value any, stack []byte, stderr io.Writer) {
	_, _ = fmt.Fprintf(stderr, "%s exiting due to uncaughtException:\n%v\n%s", AppName, value, stack)
	if m.recordCrash("uncaught_exception", value, string(stack)) {
		_, _ = fmt.Fprintf(stderr, "\n%s\n", crashReportInstructions(m.crashSessionFile()))
	}
}
