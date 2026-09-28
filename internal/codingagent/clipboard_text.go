package codingagent

import (
	"context"
	"runtime"
	"strings"
	"time"
)

// clipboardTextTimeout bounds each clipboard text command.
const clipboardTextTimeout = 5 * time.Second

// clipboardGOOS is the platform readClipboardText reads for; tests override it.
var clipboardGOOS = runtime.GOOS

// nativeClipboardText reads the clipboard through the host's native reader. A
// nil result means no text.
type nativeClipboardText func(context.Context) (*string, error)

// getNativeClipboardText is injectable so tests can mock the native reader
// separately from the Linux command chain.
var getNativeClipboardText = hostNativeClipboardText

// readClipboardText returns the system clipboard text, or "" when there is
// none or it cannot be read. On Linux it tries termux-clipboard-get, wl-paste, then
// xclip and xsel in that order before the native reader. WOPR loads no native
// addons, so its host native reader uses pbpaste or PowerShell where available.
func readClipboardText(parent context.Context) string {
	var commands [][]string
	if clipboardGOOS == "linux" {
		for _, route := range linuxClipboards {
			if clipboardEnv(route.env) != "" {
				commands = append(commands, route.readers...)
			}
		}
	}
	for _, command := range commands {
		if parent.Err() != nil {
			return ""
		}
		ctx, cancel := context.WithTimeout(parent, clipboardTextTimeout)
		out, err := clipboardRun(ctx, command[0], command[1:]...)
		cancel()
		if err == nil {
			return strings.ToValidUTF8(string(out), "�")
		}
	}
	native := getNativeClipboardText()
	if native == nil || parent.Err() != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(parent, clipboardTextTimeout)
	text, err := native(ctx)
	cancel()
	if err != nil || text == nil {
		return ""
	}
	return *text
}

func hostNativeClipboardText() nativeClipboardText {
	var name string
	var args []string
	switch clipboardGOOS {
	case "darwin":
		name = "pbpaste"
	case "windows":
		name = "powershell.exe"
		args = []string{"-NoProfile", "-NonInteractive", "-Command", "[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); [Console]::Write((Get-Clipboard -Raw))"}
	default:
		return nil
	}
	return func(ctx context.Context) (*string, error) {
		out, err := clipboardRun(ctx, name, args...)
		if err != nil {
			return nil, err
		}
		text := strings.ToValidUTF8(string(out), "�")
		return &text, nil
	}
}

// bracketedPaste wraps text as a terminal bracketed paste.
func bracketedPaste(text string) string { return "\x1b[200~" + text + "\x1b[201~" }

func (m *InteractiveMode) readClipboardTextAsync(apply func(string)) {
	if m.clipboardCtx == nil || m.clipboardCtx.Err() != nil {
		return
	}
	ctx, reads := m.clipboardCtx, m.clipboardReads
	reads.Go(func() {
		text := readClipboardText(ctx)
		if text == "" || ctx.Err() != nil {
			return
		}
		m.runOnMain(ctx, func() {
			if ctx.Err() == nil {
				apply(text)
			}
		})
	})
}

// handleRightClickPaste pastes clipboard text into the focused component, as
// a bracketed paste, when the fullscreen renderer reports a Windows
// right-click. The read runs off the owner loop; the paste lands only if focus
// has not moved meanwhile. Clipboard errors are ignored.
func (m *InteractiveMode) handleRightClickPaste() {
	if m.clipboardCtx == nil || m.clipboardCtx.Err() != nil {
		return
	}
	target := m.tuiInst.FocusedComponent()
	handler, ok := target.(interface{ HandleInput(data string) })
	if !ok {
		return
	}
	m.readClipboardTextAsync(func(text string) {
		if m.tuiInst.FocusedComponent() != target {
			return
		}
		handler.HandleInput(bracketedPaste(text))
		m.tuiInst.RequestRender()
	})
}
