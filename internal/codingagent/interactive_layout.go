package codingagent

import (
	"context"
	"time"

	"github.com/alexrudloff/wopr/tui"
)

func (m *InteractiveMode) setStatusContainerLabel(label string) {
	if m.activeStatusIndicator != nil && m.activeStatusIndicator.Kind == "retry" {
		m.activeStatusIndicator.SetMessage(label)
		return
	}
	m.showStatusIndicator(&tui.StatusIndicator{Kind: "retry", Loader: tui.NewStyledLoader(tui.ActiveTheme().Warning, tui.ActiveTheme().Muted, label, nil)})
}

func (m *InteractiveMode) startWorkingLoader() {
	m.showStatusIndicator(&tui.StatusIndicator{Kind: "working", Loader: tui.NewStyledLoader(tui.ActiveTheme().Accent, tui.ActiveTheme().Muted, "Working", nil)})
	m.tuiInst.RequestRender()
}

func (m *InteractiveMode) stopWorkingLoader() { m.clearStatusIndicator("working") }

func (m *InteractiveMode) tickSpinner(ctx context.Context) {
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case interval := <-m.spinnerIntervalCh:
			ticker.Reset(interval)
		case <-ticker.C:
			// Like a Node setInterval on a blocked event loop, ticks that come
			// due while one is still queued collapse into it; a stalled loop
			// must not wake to a backlog of frames or a task queue full of
			// ticks that crowds out other posts.
			if !m.spinnerTickQueued.CompareAndSwap(false, true) {
				continue
			}
			if !m.postUITask(func() {
				m.spinnerTickQueued.Store(false)
				if ctx.Err() != nil {
					return
				}
				m.tickStatusIndicators(time.Now())
			}) {
				m.spinnerTickQueued.Store(false)
			}
		}
	}
}

func (m *InteractiveMode) tickStatusIndicators(now time.Time) {
	blinked := false
	if lit := m.cursorLit(); lit != m.cursorWasLit {
		m.cursorWasLit = lit
		m.editor.Invalidate()
		blinked = true
	}
	if home := m.homeVisible() && m.isIdle; home != m.homeShown {
		m.homeShown = home
		if home {
			m.promptExample = int(now.UnixNano() / 1e6)
		}
	}
	if m.isIdle && m.activeStatusIndicator == nil {
		// The home screen's lamps and greeting animate while it shows.
		if m.homeVisible() || blinked {
			m.tuiInst.Render()
		}
		return
	}
	m.statusLine.Invalidate()
	if indicator := m.activeStatusIndicator; indicator != nil && now.Sub(m.statusLastFrame) >= m.statusFrameInterval() {
		if len(indicator.Frames) > 1 {
			indicator.Tick()
		}
		m.statusLastFrame = now
		// Node's setInterval rearms from callback entry, not the previous deadline. Rearm only a due callback so dispatch jitter cannot skip a frame or postpone a replacement indicator's first frame.
		m.resetSpinnerInterval()
	}
	for _, block := range m.bashOrder {
		if loader := block.Loader(); loader != nil {
			loader.Tick()
		}
	}
	m.tuiInst.Render()
}

// openExternalEditor hands the terminal to $VISUAL || $EDITOR with the
// current editor buffer content, then loads the result back.
//
// Wraps OpenExternalEditor (the pure helper) with the TUI lifecycle:
//  1. show cursor (editor needs it visible);
//  2. restore cooked-mode terminal (rawRestore);
//  3. run editor;
//  4. re-enter raw mode + hide cursor;
//  5. SetText if successful, flash status either way;
//  6. RepaintAll: the editor may have left arbitrary ANSI state.
