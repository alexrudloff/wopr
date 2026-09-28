package tui

import (
	"io"
	"os"
	"sync/atomic"

	"golang.org/x/term"
)

// cookedTerminal is the terminal state captured before the first raw-mode
// entry, kept so a signal handler can hand the terminal back.
type cookedTerminal struct {
	fd    int
	state *term.State
}

var cookedTerminalState atomic.Pointer[cookedTerminal]

// rememberSignalRestore records the pre-raw terminal state the first time raw
// mode is entered. Later entries (resuming from suspend, returning from an
// external editor) re-enter raw mode from raw-adjacent states, so only the
// first capture describes the terminal the user started with.
func rememberSignalRestore(fd int, state *term.State) {
	cookedTerminalState.CompareAndSwap(nil, &cookedTerminal{fd: fd, state: state})
}

// RestoreTerminalFromSignal returns the terminal to the mode it had before wopr
// took it over, and reports whether there was anything to restore.
//
// This is the subset of raw-mode teardown that is safe to run from a signal
// goroutine: it disables the extended-key protocols in a single write and then
// runs one tcsetattr against a state captured at startup. It deliberately
// skips the rest of the normal teardown, which drains stdin for up to a second,
// because that would race the input reader and the render loop.
//
// Without the tcsetattr, exiting on a signal leaves the terminal raw: ISIG
// stays off, so the user's shell has no working Ctrl+C until they run `reset`.
// Without the protocol disable, the Kitty flags wopr pushed stay on the
// terminal's stack after wopr is gone, so the shell that inherits the terminal
// receives CSI-u encoded keys it does not understand.
func RestoreTerminalFromSignal() bool {
	// D51: restore the terminal rather than leave it raw when a signal
	// terminates the session.
	restore := cookedTerminalState.Load()
	if restore == nil {
		return false
	}
	processTerminal.disableKeyboardProtocol()
	return term.Restore(restore.fd, restore.state) == nil
}

// LeaveFullscreenFromSignal takes down what the fullscreen renderer turned
// on (mouse reporting, bracketed paste, the alternate screen, a hidden
// cursor) with one write, for an exit that cannot run the renderer's own
// teardown. It is safe from any goroutine.
func LeaveFullscreenFromSignal() {
	var out io.Writer = os.Stdout
	if processTerminal.out != nil {
		out = processTerminal.out
	}
	_, _ = io.WriteString(out, altDisableMouse+"\x1b[?2004l"+altExitAltScreen+"\x1b[0m\x1b[?25h")
}
