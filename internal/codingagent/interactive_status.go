package codingagent

import (
	"fmt"
	"time"

	"github.com/alexrudloff/wopr/tui"
)

func (m *InteractiveMode) showStatusIndicator(indicator *tui.StatusIndicator) {
	m.clearStatusIndicator("")
	m.activeStatusIndicator = indicator
	m.activeWorkingIndicatorEmbedded = m.editor.EmbedWorkingStatus
	m.statusLastFrame = time.Now()
	m.statusContainer.Clear()
	if m.activeWorkingIndicatorEmbedded {
		m.editor.SetWorkingStatusIndicator(indicator)
	} else {
		m.statusContainer.Add(indicator)
	}
	m.resetSpinnerInterval()
}

func (m *InteractiveMode) clearStatusIndicator(kind string) {
	previous := m.activeStatusIndicator
	if kind != "" && (previous == nil || previous.Kind != kind) {
		return
	}
	if previous != nil && previous.Kind == "retry" && m.retryCountdownStop != nil {
		m.retryCountdownStop()
		m.retryCountdownStop = nil
	}
	m.activeStatusIndicator = nil
	m.activeWorkingIndicatorEmbedded = false
	m.editor.SetWorkingStatusIndicator(nil)
	if m.statusContainer != nil {
		m.statusContainer.Clear()
	}
	m.resetSpinnerInterval()
}

func (m *InteractiveMode) statusCancelHint() string {
	return "(" + m.keybindings.KeyText("app.interrupt") + " to cancel)"
}

func (m *InteractiveMode) showCompactionStatusIndicator(reason string) {
	label := "Auto-compacting... "
	switch reason {
	case "manual":
		label = "Compacting context... "
	case "overflow":
		label = "Context overflow detected, Auto-compacting... "
	case "fit":
		label = "Compacting to fit the routed model... "
	}
	m.showStatusIndicator(&tui.StatusIndicator{Kind: "compaction", Loader: tui.NewStyledLoader(tui.ActiveTheme().Accent, tui.ActiveTheme().Muted, label+m.statusCancelHint(), nil)})
}

func (m *InteractiveMode) showBranchSummaryStatusIndicator() {
	m.showStatusIndicator(&tui.StatusIndicator{Kind: "branchSummary", Loader: tui.NewStyledLoader(tui.ActiveTheme().Accent, tui.ActiveTheme().Muted, "Summarizing branch... "+m.statusCancelHint(), nil)})
}

// showRetryStatusIndicator owns one countdown until replacement, disposal, or shutdown.
// The owner loop applies its queued labels; disposal joins the worker even if it is waiting for queue space.
// attempt counts retries, so the attempt about to run is attempt+1 of
// maxAttempts+1; reason, when set, says why the last one failed.
func (m *InteractiveMode) showRetryStatusIndicator(attempt, maxAttempts, delayMs int, reason string) {
	m.clearStatusIndicator("")
	remaining := int((delayMs + 999) / 1000)
	cancelHint := m.statusCancelHint()
	message := func(seconds int) string {
		label := fmt.Sprintf("Attempt %d of %d in %ds", attempt+1, maxAttempts+1, seconds)
		if reason != "" {
			label += " · " + reason
		}
		return label + "... " + cancelHint
	}
	m.setStatusContainerLabel(message(remaining))
	stop := make(chan struct{})
	stopped := make(chan struct{})
	m.retryCountdownStop = func() {
		close(stop)
		<-stopped
	}
	var done <-chan struct{}
	if m.runCtx != nil {
		done = m.runCtx.Done()
	}
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-done:
				return
			case <-ticker.C:
				remaining--
				m.postRetryStatusUpdate(stop, message(remaining))
				if remaining <= 0 {
					return
				}
			}
		}
	}()
}

func (m *InteractiveMode) statusFrameInterval() time.Duration {
	return 80 * time.Millisecond
}

func (m *InteractiveMode) resetSpinnerInterval() {
	if m.spinnerIntervalCh == nil {
		return
	}
	select {
	case <-m.spinnerIntervalCh:
	default:
	}
	select {
	case m.spinnerIntervalCh <- m.statusFrameInterval():
	default:
	}
}

// postRetryStatusUpdate delivers one countdown second to the owner loop.
// Every second runs, late under load but never skipped: the countdown
// goroutine waits for queue space (its ticker coalesces seconds that come due
// meanwhile) until the countdown is stopped or the session ends. Queued
// frames belong to their original operation, even if a new status replaces it
// before the owner loop runs them.
func (m *InteractiveMode) postRetryStatusUpdate(stop <-chan struct{}, label string) {
	var done <-chan struct{}
	if m.runCtx != nil {
		done = m.runCtx.Done()
	}
	update := func() {
		select {
		case <-stop:
			return
		default:
		}
		if m.activeStatusIndicator == nil || m.activeStatusIndicator.Kind != "retry" {
			return
		}
		m.activeStatusIndicator.SetMessage(label)
		m.tuiInst.Render()
	}
	select {
	case m.uiTaskCh <- update:
	case <-stop:
	case <-done:
	}
}
