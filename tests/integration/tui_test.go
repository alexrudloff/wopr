//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestSlashHelpRoundTrip starts wopr and dispatches a slash command. A
// leftover "regular" tuiMode setting still starts the fullscreen UI. The faux
// model makes readiness independent of credentials.
func TestSlashHelpRoundTrip(t *testing.T) {
	h := newHarness(t)
	woprHome := t.TempDir()
	agentDir := filepath.Join(woprHome, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(`{"tuiMode":"regular"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.startArgsAt([]string{"--model", "test-faux/faux-1"}, t.TempDir(), "WOPR_HOME="+woprHome, "WOPR_TEST_FAUX=1")
	h.expectContains(startupReadyWait, interactiveReadyMarker, "Test Faux")
	h.send("/hotkeys\r")
	h.expectContains(3*time.Second, "Shift+Enter       : insert newline")
}

// TestShiftEnterViaKeystroke uses tmux's logical `S-Enter` keystroke
// notation instead of raw byte injection. This exercises the chain
// that actually broke for the user:
//
//	tmux Shift+Enter event → (extended-keys protocol enabled by
//	wopr via modifyOtherKeys mode 1) → tmux encodes as CSI-u
//	`\x1b[13;2u` (because user's tmux has
//	`extended-keys-format csi-u`) → wopr classifyKey routes to
//	actionNewline.
//
// Without the protocol enable in tui.go::EnterRawMode, tmux falls
// back to legacy mode and S-Enter arrives as plain `\r`, which
// wopr treats as submit. That's the user-reported regression.
func TestShiftEnterViaKeystroke(t *testing.T) {
	h := newHarness(t)
	h.startArgs([]string{"--model", "test-faux/faux-1"}, "WOPR_TEST_FAUX=1")
	h.expectContains(startupReadyWait, interactiveReadyMarker, "Test Faux")

	h.send("line one")
	// Logical keystroke: Shift+Enter. Tmux encodes it according to
	// the protocol the running app has signalled for. When wopr
	// has enabled modifyOtherKeys mode 1, tmux 3.2+ with
	// extended-keys on translates this to a CSI-u sequence.
	// Without the protocol enable, tmux strips the modifier and
	// sends bare \r.
	h.sendKey("S-Enter")
	h.send("line two")
	time.Sleep(700 * time.Millisecond)

	pane := h.capture()
	if !strings.Contains(pane, "line one") || !strings.Contains(pane, "line two") {
		t.Fatalf("missing one of the lines:\n%s", pane)
	}
	if strings.Contains(pane, "line oneline two") {
		t.Fatalf("Shift+Enter via S-Enter keystroke regressed: protocol enable missing or wrong:\n%s", pane)
	}
	assertMultilineEditor(t, pane)
}

// promptPanelTwoLines matches both lines on consecutive rows inside the prompt panel's left rail.
var promptPanelTwoLines = regexp.MustCompile(`┃\s+line one\s*\n\s*┃\s+line two\s*\n`)

func assertMultilineEditor(t *testing.T, pane string) {
	t.Helper()
	if !promptPanelTwoLines.MatchString(pane) {
		t.Fatalf("Shift+Enter must retain both lines in the editor without submitting:\n%s", pane)
	}
}

// TestExitCleanAfterPromptAndTool sends a prompt that runs a tool, exits with
// Ctrl+D, and checks the resume hint and that the shell gets the terminal back.
func TestExitCleanAfterPromptAndTool(t *testing.T) {
	h := newHarness(t)
	woprHome := t.TempDir()
	agentDir := filepath.Join(woprHome, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"fullscreenExitOutput":"transcript"}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(t.TempDir(), "sessions")
	h.startArgs([]string{"--model", "test-faux/faux-1", "--session-dir", sessionDir}, "WOPR_HOME="+woprHome, "WOPR_TEST_FAUX=1")
	h.expectContains(startupReadyWait, interactiveReadyMarker, "Test Faux")

	marker := "Run: expr 20 + 22"
	h.send(marker + "\r")
	h.expectContains(10*time.Second, marker, "▣  faux-1 ·", "42")
	time.Sleep(300 * time.Millisecond)
	h.sendKey("C-d")
	h.expectContains(5*time.Second, "To resume this session:", "wopr --session")

	time.Sleep(700 * time.Millisecond)
	h.send("echo SHELL-RESTORED\r")
	restored := h.expectContains(3*time.Second, "SHELL-RESTORED")
	if isWoprStatusLine(lastNonEmptyLine(restored)) {
		t.Fatalf("terminal did not return to the shell:\n%s", restored)
	}
}

// TestExitCleanShutdown verifies the documented exit paths return to
// the shell without leaving wopr or the tmux session in a wedged state.
//
// wopr exposes /quit and accepts Ctrl+D as a quick-exit. We test both paths.
//
// The "did wopr exit?" check is intentionally NOT a substring match
// against the full pane: tmux capture-pane returns scrollback, so the
// wopr banner is still visible after exit. We instead inspect the last
// non-empty line of the pane: it should be a shell prompt, not the
// wopr footer.
func TestExitCleanShutdown(t *testing.T) {
	cases := []struct {
		name   string
		action func(h *harness)
	}{
		{
			name:   "slash-quit",
			action: func(h *harness) { h.send("/quit\r") },
		},
		{
			name:   "ctrl-d",
			action: func(h *harness) { h.sendKey("C-d") },
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.start()
			h.expectContains(startupReadyWait, interactiveReadyMarker)

			tc.action(h)
			time.Sleep(700 * time.Millisecond)
			pane := h.capture()

			// Inspect the last non-empty line. After clean exit it is
			// either the user's shell prompt (`❯`, `$`, `%`, `›`) or
			// blank (if the shell scrolled the pane). After failure
			// it's wopr's footer row.
			last := lastNonEmptyLine(pane)
			if isWoprStatusLine(last) {
				t.Fatalf("%s: wopr appears still running: last line is wopr status:\n  last=%q\n  full pane:\n%s",
					tc.name, last, pane)
			}
			t.Logf("%s: clean exit, last line = %q", tc.name, last)
		})
	}
}

// lastNonEmptyLine returns the trimmed last non-empty line of a tmux
// pane capture, or "" if the pane is entirely blank.
func lastNonEmptyLine(pane string) string {
	lines := strings.Split(pane, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimRight(lines[i], " \t\r")
		if line != "" {
			return line
		}
	}
	return ""
}

// isWoprStatusLine reports whether line looks like a running wopr footer, the
// bottom row ("WOPR <version>"). No shell prompt carries it.
func isWoprStatusLine(line string) bool {
	return strings.Contains(line, interactiveReadyMarker)
}
