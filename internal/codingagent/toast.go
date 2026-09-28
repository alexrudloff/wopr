package codingagent

import (
	"strings"
	"time"

	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// Toast geometry and lifetime.
const (
	toastMaxWidth = 60
	toastMargin   = 2
	toastDuration = 5 * time.Second
)

// toast is a transient notice at the top right: a panel with heavy bars in
// the variant color on both sides, an optional bold title, and the message.
type toast struct {
	tui.BaseComponent
	variant, title, message string
}

func (t *toast) Render(width int) []string {
	th := tui.ActiveTheme()
	bar := th.Fg(t.variant) + "┃" + tui.SGRFgReset
	panel := th.Bg("backgroundPanel")
	inner := max(1, width-2)
	textWidth := max(1, inner-4)
	row := func(text string) string {
		return bar + tui.FillBackground("  "+text, inner, panel) + bar
	}
	out := []string{row("")}
	if t.title != "" {
		for _, line := range widthx.WrapTextWithAnsi(t.title, textWidth) {
			out = append(out, row(bold(th.FgText("text", line))))
		}
		out = append(out, row(""))
	}
	for _, line := range widthx.WrapTextWithAnsi(t.message, textWidth) {
		out = append(out, row(th.FgText("text", line)))
	}
	return append(out, row(""))
}

// showToast shows a notice for a few seconds, replacing any toast already
// showing. variant is a theme token: info, success, warning, or error.
func (m *InteractiveMode) showToast(variant, title, message string) {
	if m.toastHandle != nil {
		m.toastHandle.Close()
		m.toastHandle = nil
	}
	component := &toast{variant: variant, title: title, message: strings.TrimSpace(message)}
	width := min(toastMaxWidth, max(20, m.tuiInst.Width()-3*toastMargin))
	handle := m.tuiInst.OpenOverlay(component, tui.OverlayOptions{
		Width:        width,
		Anchor:       "top-right",
		Margin:       tui.OverlayMargin{Top: toastMargin, Right: toastMargin},
		NonCapturing: true,
	})
	m.toastHandle, m.toastUntil = handle, time.Now().Add(toastDuration)
	m.tuiInst.RequestRender()
	time.AfterFunc(toastDuration, func() {
		m.postUITask(func() {
			if m.toastHandle == handle {
				handle.Close()
				m.toastHandle = nil
				m.tuiInst.RequestRender()
			}
		})
	})
}

// showToastQueued shows a notice after the toast already showing, if any,
// instead of replacing it.
func (m *InteractiveMode) showToastQueued(variant, title, message string) {
	if m.toastHandle == nil || !time.Now().Before(m.toastUntil) {
		m.showToast(variant, title, message)
		return
	}
	time.AfterFunc(time.Until(m.toastUntil)+100*time.Millisecond, func() {
		m.postUITask(func() { m.showToastQueued(variant, title, message) })
	})
}

// showFlash shows a brief notice (a mode or level change, a copy
// confirmation) as a toast, so the transcript keeps only the conversation.
func (m *InteractiveMode) showFlash(message string) {
	m.showToast("info", "", message)
}

// expireToast closes the toast once its time is up. Modal loops that run
// long (setup) call it, since the timer's own close waits for the main
// loop.
func (m *InteractiveMode) expireToast() {
	if m.toastHandle != nil && time.Now().After(m.toastUntil) {
		m.toastHandle.Close()
		m.toastHandle = nil
		m.tuiInst.RequestRender()
	}
}
