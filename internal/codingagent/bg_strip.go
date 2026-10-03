package codingagent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/tui"
	"github.com/alexrudloff/wopr/tui/widthx"
)

// The background-work strip sits just above the prompt while subagents or
// background shell jobs run: one line each (a summary line past
// bgStripMaxRows). Down on an empty prompt focuses it; Up and Down move,
// Enter opens the item's view, Esc goes back to the prompt. A finished item
// stays, marked done or failed, until it is viewed or the next prompt.

// bgStripState is the strip's focus and what has been seen.
type bgStripState struct {
	focus  bool
	cursor int
	// seen holds the finished items the user opened; they leave the strip.
	seen map[string]bool
	// since is when the last prompt was sent: items that finished before it
	// leave the strip.
	since time.Time
}

// bgStripMaxRows is how many items the unfocused strip lists before it
// collapses to a summary line; focused, it shows bgStripFocusRows.
const (
	bgStripMaxRows   = 3
	bgStripFocusRows = 6
)

// bgItem is one row of the strip: a subagent or a background shell job.
type bgItem struct {
	id      string
	shell   bool
	title   string
	detail  string
	state   string
	running bool
	elapsed time.Duration
}

// backgroundShells returns the session's background jobs, or nil.
func (m *InteractiveMode) backgroundShells() *tools.BackgroundShells {
	if host, ok := m.opts.SessionHandle.(interface {
		BackgroundShells() *tools.BackgroundShells
	}); ok {
		return host.BackgroundShells()
	}
	return nil
}

// bgItems lists the strip's rows at now: running subagents and jobs, then
// the finished ones not yet seen and finished since the last prompt.
func (m *InteractiveMode) bgItems(now time.Time) []bgItem {
	var running, finished []bgItem
	keep := func(id string, ended time.Time) bool {
		return !m.bg.seen[id] && !ended.Before(m.bg.since)
	}
	if registry := m.agentsRegistry(); registry != nil {
		for _, a := range registry.List() {
			item := bgItem{id: a.ID, title: a.Label(), state: a.State, running: a.Running(), elapsed: a.Elapsed(now)}
			kind := a.Spec.Type
			if kind == "" {
				kind = "task"
			}
			parts := []string{kind}
			if a.Model != "" {
				parts = append(parts, a.Model)
			}
			if a.Running() && a.Current != "" {
				parts = append(parts, a.Current)
			} else if !a.Running() && len(a.Files) > 0 {
				parts = append(parts, fmt.Sprintf("changed %d", len(a.Files)))
			}
			item.detail = strings.Join(parts, " · ")
			switch {
			case a.Running():
				running = append(running, item)
			case keep(a.ID, a.Finished):
				finished = append(finished, item)
			}
		}
	}
	for _, job := range m.backgroundShells().List() {
		item := bgItem{id: job.ID, shell: true, title: "$ " + job.Command, state: job.State, running: job.Running(), elapsed: job.Elapsed(now)}
		detail := []string{fmt.Sprintf("pid %d", job.PGID)}
		if job.ExitCode != nil && !job.Running() {
			detail = append(detail, fmt.Sprintf("exit %d", *job.ExitCode))
		}
		item.detail = strings.Join(detail, " · ")
		switch {
		case job.Running():
			running = append(running, item)
		case keep(job.ID, job.Finished):
			finished = append(finished, item)
		}
	}
	return append(running, finished...)
}

// bgLive reports whether the strip changes with the clock.
func (m *InteractiveMode) bgLive(now time.Time) bool {
	return slices.ContainsFunc(m.bgItems(now), func(it bgItem) bool { return it.running })
}

// bgItemGlyph is an item's mark: a spinner while it runs, else its state.
func bgItemGlyph(it bgItem, now time.Time) string {
	th := tui.ActiveTheme()
	if it.running {
		return hexFg(phosphorHex(), agentSpinner[int(now.Unix())%len(agentSpinner)])
	}
	switch it.state {
	// The shell states "done" and "failed" match the subagent ones.
	case subagent.StateDone, tools.ShellExited:
		return th.FgText("success", "✓")
	case subagent.StateFailed:
		return th.FgText("error", "✗")
	}
	return th.FgText("warning", "■")
}

// bgStrip renders the strip; it is empty when nothing runs or waits.
type bgStrip struct {
	tui.BaseComponent
	m *InteractiveMode
}

func (s *bgStrip) Render(width int) []string {
	m := s.m
	now := time.Now()
	items := m.bgItems(now)
	if len(items) == 0 {
		m.bg.focus = false
		return nil
	}
	th := tui.ActiveTheme()
	m.bg.cursor = max(0, min(m.bg.cursor, len(items)-1))
	running := 0
	for _, it := range items {
		if it.running {
			running++
		}
	}
	if !m.bg.focus && len(items) > bgStripMaxRows {
		left := bgItemGlyph(items[0], now) + " " + th.FgText("text", fmt.Sprintf("%d background %s", len(items), plural(len(items), "task", "tasks")))
		if running > 0 && running < len(items) {
			left += th.FgText("textMuted", fmt.Sprintf(" · %d running", running))
		}
		return []string{"", spread(" "+left, th.FgText("textMuted", "↓ to view "), width)}
	}
	start, end := 0, len(items)
	if m.bg.focus && len(items) > bgStripFocusRows {
		start = max(0, min(m.bg.cursor-bgStripFocusRows/2, len(items)-bgStripFocusRows))
		end = start + bgStripFocusRows
	}
	out := []string{""}
	for i := start; i < end; i++ {
		it := items[i]
		selected := m.bg.focus && i == m.bg.cursor
		right := formatDuration(it.elapsed.Truncate(time.Second))
		if !it.running {
			right = it.state
		}
		if i == start && !m.bg.focus {
			right += " · ↓ to view"
		}
		right = th.FgText("textMuted", right+" ")
		marker := "  "
		if selected {
			marker = th.FgText("primary", "› ")
		}
		titleW := max(8, (width-widthx.VisibleWidth(right)-6)*3/5)
		title := widthx.TruncateToWidth(strings.Join(strings.Fields(it.title), " "), titleW, "…", false)
		titleText := th.FgText("text", title)
		if !it.running {
			titleText = th.FgText("textMuted", title)
		}
		if selected {
			titleText = bold(th.FgText("text", title))
		}
		detailW := max(0, width-widthx.VisibleWidth(right)-widthx.VisibleWidth(title)-7)
		left := marker + bgItemGlyph(it, now) + " " + titleText
		if detailW > 3 && it.detail != "" {
			left += th.FgText("textMuted", "  "+widthx.TruncateToWidth(it.detail, detailW, "…", false))
		}
		row := spread(left, right, width)
		if selected {
			row = tui.FillBackground(row, width, th.Bg("backgroundElement"))
		}
		out = append(out, row)
	}
	if m.bg.focus {
		out = append(out, th.FgText("textMuted", "  ↑↓ select · enter open · esc back to the prompt"))
	}
	return out
}

// focusBgStrip moves the keyboard into the strip when it lists anything.
func (m *InteractiveMode) focusBgStrip() bool {
	if len(m.bgItems(time.Now())) == 0 {
		return false
	}
	m.bg.focus, m.bg.cursor = true, 0
	m.tuiInst.RequestRender()
	return true
}

// handleBgKey handles a key while the strip has focus. It reports false for
// a key that leaves the strip and belongs to the prompt.
func (m *InteractiveMode) handleBgKey(ctx context.Context, data string) bool {
	items := m.bgItems(time.Now())
	if len(items) == 0 {
		m.bg.focus = false
		return false
	}
	defer m.tuiInst.RequestRender()
	switch {
	case tui.MatchesKeyID(data, "up"):
		if m.bg.cursor == 0 {
			m.bg.focus = false
			return true
		}
		m.bg.cursor--
	case tui.MatchesKeyID(data, "down"):
		m.bg.cursor = min(m.bg.cursor+1, len(items)-1)
	case tui.MatchesKeyID(data, "enter"):
		it := items[max(0, min(m.bg.cursor, len(items)-1))]
		m.postUITask(func() { m.openBgItem(ctx, it) })
	case tui.MatchesKeyID(data, "escape"):
		m.bg.focus = false
	default:
		m.bg.focus = false
		return false
	}
	return true
}

// openBgItem opens an item's view; a finished item then leaves the strip.
func (m *InteractiveMode) openBgItem(_ context.Context, it bgItem) {
	if it.shell {
		m.openShellDialog(it.id)
	} else {
		m.openAgentDialog(it.id)
	}
	m.markBgSeen(it.id)
	m.tuiInst.RequestRender()
}

// markBgSeen drops a finished item from the strip once it is viewed.
func (m *InteractiveMode) markBgSeen(id string) {
	running := false
	if registry := m.agentsRegistry(); registry != nil {
		if a, ok := registry.Get(id); ok {
			running = a.Running()
		}
	}
	if job, ok := m.backgroundShells().Get(id); ok {
		running = job.Running()
	}
	if running {
		return
	}
	if m.bg.seen == nil {
		m.bg.seen = map[string]bool{}
	}
	m.bg.seen[id] = true
}

// openShellDialog shows a background job: its command, state, and the live
// tail of its output, with Stop and Close.
func (m *InteractiveMode) openShellDialog(id string) {
	shells := m.backgroundShells()
	if _, ok := shells.Get(id); !ok {
		m.showWarning("No background job " + id)
		return
	}
	// Close is focused first, so a stray Enter never stops the job.
	view := &shellView{shells: shells, id: id, height: func() int { return max(6, m.tuiInst.Height()/2) }, follow: true, button: 1}
	wake, stop := everySecond()
	defer stop()
	m.runDialog(modal{component: view, handleInput: view.HandleInput, done: func() bool { return view.done }, wake: wake}, dialogLarge)
	m.markBgSeen(id)
}

// everySecond wakes a live dialog once a second, so a running job's output
// and an agent's progress repaint without a keypress; stop ends it.
func everySecond() (<-chan struct{}, func()) {
	wake := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				select {
				case wake <- struct{}{}:
				default:
				}
			}
		}
	}()
	return wake, func() { close(done) }
}

// shellTailBytes is how much of a job's log its view reads.
const shellTailBytes = 64 << 10

// shellView is a background job's dialog.
type shellView struct {
	tui.BaseComponent
	shells *tools.BackgroundShells
	id     string
	height func() int
	top    int
	// follow keeps the view at the end of the log as it grows.
	follow bool
	button int
	done   bool
}

func (v *shellView) buttons() []string {
	if job, ok := v.shells.Get(v.id); ok && job.Running() {
		return []string{"Stop", "Close"}
	}
	return []string{"Close"}
}

func (v *shellView) HandleInput(data string) {
	page := max(1, v.height()-2)
	buttons := v.buttons()
	v.button = min(v.button, len(buttons)-1)
	switch {
	case tui.MatchesKeyID(data, "escape"), tui.MatchesKeyID(data, "ctrl+c"), tui.MatchesKeyID(data, "q"):
		v.done = true
	case tui.MatchesKeyID(data, "enter"):
		if buttons[v.button] == "Stop" {
			v.shells.Stop(v.id)
			v.button = 0
		} else {
			v.done = true
		}
	case tui.MatchesKeyID(data, "tab"), tui.MatchesKeyID(data, "right"):
		v.button = (v.button + 1) % len(buttons)
	case tui.MatchesKeyID(data, "shift+tab"), tui.MatchesKeyID(data, "left"):
		v.button = (v.button + len(buttons) - 1) % len(buttons)
	case tui.MatchesKeyID(data, "up"), tui.MatchesKeyID(data, "k"):
		v.top, v.follow = v.top-1, false
	case tui.MatchesKeyID(data, "down"), tui.MatchesKeyID(data, "j"):
		v.top++
	case tui.MatchesKeyID(data, "pageUp"):
		v.top, v.follow = v.top-page, false
	case tui.MatchesKeyID(data, "pageDown"), tui.MatchesKeyID(data, "space"):
		v.top += page
	case tui.MatchesKeyID(data, "home"):
		v.top, v.follow = 0, false
	case tui.MatchesKeyID(data, "end"):
		v.follow = true
	}
	v.Invalidate()
}

func (v *shellView) Render(width int) []string {
	th := tui.ActiveTheme()
	const padX = 4
	inner := max(1, width-2*padX)
	pad := strings.Repeat(" ", padX)
	job, ok := v.shells.Get(v.id)
	if !ok {
		return []string{pad + th.FgText("textMuted", "No background job "+v.id)}
	}
	title := bold(th.FgText("text", widthx.TruncateToWidth(job.ID+" · $ "+strings.Join(strings.Fields(job.Command), " "), max(1, inner-4), "…", false)))
	stats := []string{job.State, fmt.Sprintf("process group %d", job.PGID), formatDuration(job.Elapsed(time.Now()).Truncate(time.Second))}
	if job.ExitCode != nil {
		stats = append(stats, fmt.Sprintf("exit %d", *job.ExitCode))
	}
	if job.LogPath != "" {
		stats = append(stats, job.LogPath)
	}
	out := []string{
		pad + spread(title, th.FgText("textMuted", "esc"), inner),
		pad + th.FgText("textMuted", widthx.TruncateToWidth(strings.Join(stats, " · "), inner, "…", false)),
		"",
	}
	var lines []string
	text, err := v.shells.Tail(v.id, shellTailBytes)
	switch {
	case err != nil:
		lines = append(lines, th.FgText("textMuted", "Output isn't shown: "+err.Error()+"."))
	case strings.TrimSpace(text) == "":
		lines = append(lines, th.FgText("textMuted", "(no output yet)"))
	default:
		for line := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
			for _, wrapped := range widthx.WrapTextWithAnsi(line, inner) {
				lines = append(lines, th.FgText("text", wrapped))
			}
		}
	}
	height := v.height()
	if v.follow {
		v.top = len(lines)
	}
	v.top = max(0, min(v.top, len(lines)-height))
	if v.top >= len(lines)-height {
		v.follow = true
	}
	for _, line := range lines[v.top:min(len(lines), v.top+height)] {
		out = append(out, pad+line)
	}
	buttons := v.buttons()
	v.button = min(v.button, len(buttons)-1)
	var row []string
	for i, b := range buttons {
		row = append(row, dialogButton(b, i == v.button))
	}
	hint := "↑↓ scroll · ←→ choose · enter select · esc close"
	if len(lines) > height {
		hint = fmt.Sprintf("%d-%d of %d · ", v.top+1, min(len(lines), v.top+height), len(lines)) + hint
	}
	return append(out, "", pad+strings.Join(row, "  "), pad+th.FgText("textMuted", widthx.TruncateToWidth(hint, inner, "…", false)), "")
}

// dialogButton draws a dialog button, highlighted when focused.
func dialogButton(label string, focused bool) string {
	th := tui.ActiveTheme()
	if focused {
		return tui.FillBackground(" "+"\x1b[1m"+th.FgText("selectedListItemText", label)+tui.SGRBoldDimReset+" ", len([]rune(label))+2, th.Bg("primary"))
	}
	return tui.FillBackground(" "+th.FgText("text", label)+" ", len([]rune(label))+2, th.Bg("backgroundElement"))
}
