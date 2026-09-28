package codingagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// WOPR has no native clipboard module, so the platform commands (pbcopy,
// clip, the Linux tools) are the direct writers.

const maxOSC52EncodedLength = 100_000

// clipboardCommandOptions are runClipboardCommand's options.
type clipboardCommandOptions struct {
	input   *string
	timeout time.Duration
}

// clipboardOutputLimit bounds what runClipboardCommand reads from a helper.
const clipboardOutputLimit = 50 * 1024 * 1024

// runClipboardCommand runs a clipboard helper. ok is false when the command
// fails, times out, or exceeds clipboardOutputLimit; empty output with ok true
// is a successful result.
func runClipboardCommand(name string, args []string, options clipboardCommandOptions) (output []byte, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), options.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	if options.input != nil {
		// Clipboard writers can daemonize; give them no output pipe to retain.
		cmd.Stdin = strings.NewReader(*options.input)
	} else {
		cmd.Stdout = &limitedWriter{w: &stdout, remaining: clipboardOutputLimit}
	}
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	return stdout.Bytes(), true
}

type limitedWriter struct {
	w         io.Writer
	remaining int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > l.remaining {
		return 0, errors.New("clipboard output exceeds the buffer limit")
	}
	l.remaining -= len(p)
	return l.w.Write(p)
}

// clipboardCopier holds copyToClipboard's environment so tests inject every
// dependency instead of touching the host.
type clipboardCopier struct {
	platform string // runtime.GOOS
	getenv   func(string) string
	isWSL    func() bool
	run      func(name string, args []string, options clipboardCommandOptions) ([]byte, bool)
	stdout   io.Writer
	tempDir  func() string
}

func hostClipboardCopier() clipboardCopier {
	return clipboardCopier{
		platform: runtime.GOOS,
		getenv:   os.Getenv,
		isWSL:    isHostWSL,
		run:      runClipboardCommand,
		stdout:   os.Stdout,
		tempDir:  os.TempDir,
	}
}

// copyToClipboard writes text to the system clipboard.
func copyToClipboard(text string) error {
	return hostClipboardCopier().copy(text)
}

func (c clipboardCopier) isRemoteSession() bool {
	return c.getenv("SSH_CONNECTION") != "" || c.getenv("SSH_CLIENT") != "" || c.getenv("MOSH_CONNECTION") != ""
}

func (c clipboardCopier) emitOSC52(text string) bool {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	if len(encoded) > maxOSC52EncodedLength {
		return false
	}
	_, _ = io.WriteString(c.stdout, "\x1b]52;c;"+encoded+"\x07")
	return true
}

// writerCommands lists the direct clipboard writers for the platform.
func (c clipboardCopier) writerCommands() [][]string {
	switch c.platform {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{{"clip"}}
	}
	var commands [][]string
	for _, route := range linuxClipboards {
		if c.getenv(route.env) != "" {
			commands = append(commands, route.writers...)
		}
	}
	return commands
}

// linuxClipboards are the Linux clipboard routes in preference order: the
// variable that makes each available, its writers and readers, and what to
// install when none of its tools works.
var linuxClipboards = []struct {
	env              string
	writers, readers [][]string
	hint             string
}{
	{"TERMUX_VERSION", [][]string{{"termux-clipboard-set"}}, [][]string{{"termux-clipboard-get"}},
		"install the Termux:API app and `termux-api` package"},
	{"WAYLAND_DISPLAY", [][]string{{"wl-copy"}}, [][]string{{"wl-paste", "--no-newline", "--type", "text"}},
		"install `wl-clipboard` (`wl-copy`) or check Wayland access"},
	{"DISPLAY", [][]string{{"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}},
		[][]string{{"xclip", "-selection", "clipboard", "-out"}, {"xsel", "--clipboard", "--output"}},
		"install `xclip` or `xsel`, or check X11 access"},
}

// copyViaWindowsClipboard writes the Windows clipboard from WSL without WSLg.
// PowerShell reads the text from a file because clip.exe and PowerShell stdin
// decode piped bytes with the console code page, which mangles UTF-8.
func (c clipboardCopier) copyViaWindowsClipboard(text string) bool {
	suffix := make([]byte, 16)
	_, _ = rand.Read(suffix)
	tmpFile := filepath.Join(c.tempDir(), "wopr-wsl-clip-"+hex.EncodeToString(suffix)+".txt")
	if err := os.WriteFile(tmpFile, []byte(text), 0o600); err != nil {
		return false
	}
	defer func() { _ = os.Remove(tmpFile) }()
	output, ok := c.run("wslpath", []string{"-w", tmpFile}, clipboardCommandOptions{timeout: time.Second})
	winPath := strings.TrimSpace(string(output))
	if !ok || winPath == "" {
		return false
	}
	script := "Set-Clipboard -Value ([System.IO.File]::ReadAllText('" + strings.ReplaceAll(winPath, "'", "''") + "', [System.Text.Encoding]::UTF8))"
	_, ok = c.run("powershell.exe", []string{"-NoProfile", "-Command", script}, clipboardCommandOptions{timeout: 5 * time.Second})
	return ok
}

func (c clipboardCopier) copy(text string) error {
	copied := false
	for _, command := range c.writerCommands() {
		if _, ok := c.run(command[0], command[1:], clipboardCommandOptions{input: &text, timeout: 5 * time.Second}); ok {
			copied = true
			break
		}
	}
	osc52Emitted := false
	if !copied && c.platform == "linux" && c.isWSL() {
		// Windows Terminal supports OSC 52; prefer it over the slower PowerShell round trip.
		if c.getenv("WT_SESSION") != "" {
			osc52Emitted = c.emitOSC52(text)
		}
		copied = osc52Emitted || c.copyViaWindowsClipboard(text)
	}
	// OSC 52 cannot be verified, so a desktop session with a display reports
	// the failure instead. Without a display the terminal is the only route,
	// and remote sessions always emit it to reach the client clipboard.
	headless := c.platform == "linux" && len(c.writerCommands()) == 0
	oversized := false
	if !osc52Emitted && (c.isRemoteSession() || (!copied && headless)) {
		if c.emitOSC52(text) {
			copied = true
		} else {
			oversized = true
		}
	}
	if copied {
		return nil
	}
	return c.unavailableError(oversized)
}

func (c clipboardCopier) unavailableError(oversized bool) error {
	if oversized {
		return errors.New("Clipboard unavailable: text exceeds the OSC 52 size limit")
	}
	if c.platform == "linux" {
		for _, route := range linuxClipboards {
			if c.getenv(route.env) != "" {
				return errors.New("Clipboard unavailable: " + route.hint)
			}
		}
	}
	return errors.New("Clipboard unavailable")
}
