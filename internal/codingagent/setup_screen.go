package codingagent

import (
	"context"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// The setup screen replaces the home screen while setup runs: "WOPR SETUP"
// and the steps across the top, the current step's panel in the middle, and
// a status line at the bottom. Every step's content is an ordinary dialog
// body (DialogSelect, Form, LoginDialog), drawn on the panel background the
// dialogs use.

const (
	setupPanelWidth = 84
)

// setupScreen is the fullscreen setup view.
type setupScreen struct {
	tui.BaseComponent
	m *InteractiveMode
	// body is the current step's content; plain draws it full width
	// without the panel (the welcome page).
	body  tui.Component
	plain bool
	// err is a failure shown above the panel until the next step.
	err *tui.ErrorPanel
	// note is the status line's left side.
	note string
	// refresh, when set, runs a few times a second while the screen is up,
	// for rows that change in the background.
	refresh func()
	// leaving reports that esc on the showing screen leaves setup.
	leaving bool
}

// setupHost draws the setup screen while one is running; it is part of the
// layout for the life of the TUI.
type setupHost struct {
	tui.BaseComponent
	m *InteractiveMode
}

func (h *setupHost) Render(width int) []string {
	if h.m.setup == nil {
		return nil
	}
	return h.m.setup.Render(width)
}

func (s *setupScreen) height() int { return max(12, s.m.tuiInst.Height()) }

// room is the rows between the header and the status line.
func (s *setupScreen) room() int { return s.height() - 5 }

// panelRoom is the rows a panel's content may take: the room between the
// header and the status line, less the panel's top row and any error panel.
func (s *setupScreen) panelRoom() int {
	rows := s.room() - 1
	if s.err != nil {
		rows -= len(s.err.Render(setupPanelWidth)) + 1
	}
	return max(6, rows)
}

func (s *setupScreen) Render(width int) []string {
	th := tui.ActiveTheme()
	inner := max(1, width-4)
	brand := bold(tui.ThemeHexFg(phosphorHex()) + "WOPR SETUP" + tui.SGRFgReset)
	out := []string{"", "  " + brand, ""}

	var body []string
	panelWidth := min(setupPanelWidth, width-4)
	left := strings.Repeat(" ", max(0, (width-panelWidth)/2))
	if s.err != nil {
		for _, line := range s.err.Render(panelWidth) {
			body = append(body, left+line)
		}
		body = append(body, "")
	}
	if s.body != nil {
		if s.plain {
			body = append(body, s.body.Render(width)...)
		} else {
			panel := th.Bg("backgroundPanel")
			body = append(body, left+tui.FillBackground("", panelWidth, panel))
			for _, line := range s.body.Render(panelWidth) {
				body = append(body, left+tui.FillBackground(line, panelWidth, panel))
			}
		}
	}
	room := s.room()
	if len(body) > room {
		body = body[:room]
	}
	for len(body) < room {
		body = append(body, "")
	}
	out = append(out, body...)
	hint := "esc back"
	if s.leaving {
		hint = "esc leave setup"
	}
	return append(out, "", "  "+spread(th.FgText("textMuted", s.note), th.FgText("textMuted", hint), inner))
}

// setupWelcome is the first page: the WOPR wordmark, the greeting typing
// itself out in phosphor blue, and Connect or Not now.
type setupWelcome struct {
	tui.BaseComponent
	start time.Time
	// notNow is focused instead of Connect.
	notNow bool
	done   bool
	quit   bool
}

func (w *setupWelcome) HandleInput(data string) {
	switch {
	case tui.MatchesKeyID(data, "enter"), tui.MatchesKeyID(data, "space"):
		w.done, w.quit = true, w.notNow
	case tui.MatchesKeyID(data, "escape"), tui.MatchesKeyID(data, "ctrl+c"):
		w.done, w.quit = true, true
	case tui.MatchesKeyID(data, "left"), tui.MatchesKeyID(data, "right"), tui.MatchesKeyID(data, "tab"), tui.MatchesKeyID(data, "shift+tab"):
		w.notNow = !w.notNow
	}
}

func (w *setupWelcome) Done() bool { return w.done }

func (w *setupWelcome) Render(width int) []string {
	th := tui.ActiveTheme()
	greeting := "GREETINGS " + userDisplayName() + "."
	var lines []string
	for _, row := range woprWordmark() {
		lines = append(lines, bold(th.FgText("text", row)))
	}
	var subtitle []string
	for word := range strings.FieldsSeq("WAR OPERATION PLAN RESPONSE") {
		subtitle = append(subtitle, th.FgText("text", word[:1])+th.FgText("textMuted", word[1:]))
	}
	typed := []rune(greeting)
	typed = typed[:min(len(typed), max(0, int(time.Since(w.start)/greetingStep)))]
	lines = append(lines, "", strings.Join(subtitle, " "), "", "",
		bold(tui.ThemeHexFg(phosphorHex())+string(typed)+tui.SGRFgReset),
		th.FgText("textMuted", "Connect a model to get started."), "", "")
	button := func(label string, focused bool) string {
		if focused {
			return tui.FillBackground(" "+bold(th.FgText("selectedListItemText", label))+" ", len(label)+2, th.Bg("primary"))
		}
		return tui.FillBackground(" "+th.FgText("text", label)+" ", len(label)+2, th.Bg("backgroundElement"))
	}
	lines = append(lines, button("Connect", !w.notNow)+"  "+button("Not now", w.notNow))
	blockWidth := max(widthx.VisibleWidth(lines[0]), len(greeting))
	// Center the block as a whole, a third of the way down.
	indent := strings.Repeat(" ", max(0, (width-blockWidth)/2))
	out := make([]string, 0, len(lines)+4)
	for range 2 {
		out = append(out, "")
	}
	for _, line := range lines {
		out = append(out, indent+line)
	}
	return out
}

// Progress row states.
const (
	rowWaiting = iota
	rowRunning
	rowOK
	rowFailed
	rowSkipped
)

// progressRow is one line of a setupProgress.
type progressRow struct {
	label  string
	state  int
	detail string
}

// setupProgress shows work running in the background: a row per item with
// a spinner, a check, or a cross, and the result under it. Esc stops the
// work; once finished, enter continues and esc goes back.
type setupProgress struct {
	tui.BaseComponent
	title string
	rows  []progressRow
	// autoClose ends the view as soon as the work returns.
	autoClose bool
	running   bool
	stopped   bool
	done      bool
	back      bool
	cancel    context.CancelFunc
	start     time.Time
}

func (p *setupProgress) Done() bool { return p.done }

func (p *setupProgress) HandleInput(data string) {
	switch {
	case tui.MatchesKeyID(data, "escape"), tui.MatchesKeyID(data, "ctrl+c"):
		if p.running {
			p.cancel()
			p.stopped = true
			for i := range p.rows {
				if p.rows[i].state == rowWaiting || p.rows[i].state == rowRunning {
					p.rows[i].state, p.rows[i].detail = rowSkipped, "skipped"
				}
			}
			p.running = false
			return
		}
		p.done, p.back = true, true
	case tui.MatchesKeyID(data, "enter"):
		if !p.running {
			p.done = true
		}
	}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (p *setupProgress) Render(width int) []string {
	th := tui.ActiveTheme()
	pad := strings.Repeat(" ", 4)
	inner := max(1, width-8)
	out := []string{pad + spread(bold(th.FgText("text", p.title)), th.FgText("textMuted", "esc"), inner), ""}
	frame := spinnerFrames[int(time.Since(p.start)/(80*time.Millisecond))%len(spinnerFrames)]
	for _, row := range p.rows {
		var mark string
		switch row.state {
		case rowWaiting:
			mark = th.FgText("textMuted", "·")
		case rowRunning:
			mark = tui.ThemeHexFg(phosphorHex()) + frame + tui.SGRFgReset
		case rowOK:
			mark = th.FgText("success", "✓")
		case rowFailed:
			mark = th.FgText("error", "✗")
		default:
			mark = th.FgText("textMuted", "–")
		}
		out = append(out, pad+mark+" "+th.FgText("text", widthx.TruncateToWidth(row.label, inner-2, "…", false)))
		if row.detail != "" {
			token := "textMuted"
			if row.state == rowFailed {
				token = "error"
			}
			for _, line := range widthx.WrapTextWithAnsi(row.detail, max(1, inner-2)) {
				out = append(out, pad+"  "+th.FgText(token, line))
			}
		}
	}
	out = append(out, "")
	hint := func(title, key string) string {
		return bold(th.FgText("text", title)) + " " + th.FgText("textMuted", key)
	}
	if p.running {
		out = append(out, pad+hint("Stop", "esc"))
	} else {
		out = append(out, pad+hint("Continue", "enter")+"  "+hint("Back", "esc"))
	}
	return append(out, "")
}

// runProgress shows p while work runs in the background. work reports row
// changes through update, which applies them on the UI loop; it must stop
// when ctx ends. runProgress returns false when the user went back.
func (m *InteractiveMode) runProgress(p *setupProgress, work func(ctx context.Context, update func(func()))) bool {
	ctx, cancel := context.WithCancel(m.runCtxOrBackground())
	defer cancel()
	p.cancel, p.running, p.start = cancel, true, time.Now()
	tasks := make(chan func())
	finished := make(chan struct{})
	update := func(fn func()) {
		select {
		case tasks <- fn:
		case <-ctx.Done():
		}
	}
	go func() {
		defer close(finished)
		work(ctx, update)
	}()
	// The spinner's frames, and the end of the work.
	wake := make(chan struct{}, 1)
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	go func() {
		for {
			select {
			case <-tick.C:
			case <-finished:
				select {
				case tasks <- func() {
					p.running = false
					if p.autoClose && !p.stopped {
						p.done = true
					}
				}:
				case <-ctx.Done():
				}
				return
			case <-ctx.Done():
				return
			}
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}()
	md := modalOf(p)
	md.wake, md.tasks = wake, tasks
	if !m.runSetupModal(md) {
		return false
	}
	return !p.back
}

// runCtxOrBackground is the run's context, or a background one before Run.
func (m *InteractiveMode) runCtxOrBackground() context.Context {
	if m.runCtx != nil {
		return m.runCtx
	}
	return context.Background()
}
