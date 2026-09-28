package codingagent

import (
	"strings"
	"time"

	"github.com/alexrudloff/wopr/tui"
)

// Editor keys the key dispatcher handles itself, besides the app.* actions.
const (
	keySubmit  = "submit"  // Enter
	keyNewline = "newline" // Shift+Enter, Ctrl+J
	keyPaste   = "paste"   // a bracketed paste payload
)

// dispatchedActions are the app.* actions dispatchKey runs. A key bound to
// any other action reaches the editor.
var dispatchedActions = map[string]bool{
	"app.clear": true, "app.tools.expand": true, "app.editor.external": true,
	"app.model.select": true, "app.suspend": true, "app.message.followUp": true,
	"app.message.dequeue": true, "app.thinking.cycle": true, "app.thinking.toggle": true,
	"app.model.cycleForward": true, "app.model.cycleBackward": true,
	"app.session.new": true, "app.session.tree": true, "app.session.fork": true,
	"app.session.resume": true, "app.message.copy": true, appCommandPalette: true,
}

// keyAction resolves one keystroke to the app action it triggers, one of the
// editor keys above, or "" when the editor handles it.
func keyAction(data string, kb *KeybindingsManager) string {
	if kb != nil {
		// These are checked first and in this order, even when a user binds
		// another action to the same key.
		for _, action := range []string{"app.clipboard.pasteImage", "app.interrupt", "app.exit"} {
			if kb.Matches(data, action) {
				return action
			}
		}
		// Explicit history bindings precede every remaining app action, so a
		// user can bind ctrl+p to history although it opens the palette by
		// default.
		if kb.MatchesEditorHistory(data) {
			return ""
		}
		if action := kb.Resolve(data); dispatchedActions[action] {
			return action
		}
	}
	switch {
	case tui.MatchesKeyID(data, "shift+enter") || tui.MatchesKeyID(data, "ctrl+j"):
		return keyNewline
	case tui.MatchesKeyID(data, "enter"):
		return keySubmit
	// A bracketed paste arrives as one framed payload. StdinBuffer holds the
	// body across terminal reads, so embedded newlines cannot become submits.
	case strings.HasPrefix(data, "\x1b[200~") && strings.HasSuffix(data, "\x1b[201~"):
		return keyPaste
	}
	return ""
}

// ctrlCExits reports whether a Ctrl+C at time now should exit the app
// rather than clear the editor: true when it is the second Ctrl+C within
// 500ms of the previous one (last). A zero last (no prior Ctrl+C) never exits.
func ctrlCExits(last, now time.Time) bool {
	return !last.IsZero() && now.Sub(last) < 500*time.Millisecond
}
