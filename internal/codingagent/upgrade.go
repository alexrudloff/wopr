package codingagent

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/alexrudloff/wopr/tui"
)

// upgradeCommand is /upgrade: the verified self-update `wopr update` runs,
// with its stages shown as progress rows, then a choice to restart into the
// new version now or on the next start.
func (m *InteractiveMode) upgradeCommand(string) error {
	if m.opts.ReleaseSource == nil {
		m.showError("Upgrade unavailable: this build has no release source. " + SelfUpdateFallback())
		return nil
	}
	source := m.opts.ReleaseSource()
	progress := &setupProgress{title: "Upgrading " + AppName, autoClose: true}
	var result SelfUpdateResult
	var upgradeErr error
	m.runProgress(progress, func(ctx context.Context, update func(func())) {
		res, err := UpdateSelf(ctx, source, m.opts.AppVersion, false, func(stage, version string) {
			update(func() { progress.advance(upgradeStageLabel(stage, version)) })
		})
		update(func() {
			result, upgradeErr = res, err
			var refused *SelfUpdateRefused
			switch {
			case errors.As(err, &refused):
				// The check succeeded; this installation can't take the update.
				progress.advance("Installing " + res.Latest)
				progress.finish(rowFailed, refused.Message)
				progress.autoClose = false
			case err != nil:
				progress.finish(rowFailed, err.Error())
				progress.autoClose = false
			default:
				progress.finish(rowOK, "")
			}
		})
	})
	switch {
	case progress.stopped:
		m.showFlash("Upgrade stopped")
	case upgradeErr != nil:
		// The progress view showed the error until the user continued.
	case result.UpToDate:
		m.showFlash(fmt.Sprintf("%s %s is the latest", AppName, m.opts.AppVersion))
	default:
		m.availableUpdate = ""
		m.offerRestart(result.Latest)
	}
	return nil
}

// upgradeStageLabel names an UpdateSelf stage as a progress row.
func upgradeStageLabel(stage, version string) string {
	switch stage {
	case UpdateStageVerify:
		return "Verifying the " + version + " signature"
	case UpdateStageDownload:
		return "Downloading and installing " + version
	default:
		return "Checking for a newer release"
	}
}

// advance marks the running row done and starts a new one.
func (p *setupProgress) advance(label string) {
	p.finish(rowOK, "")
	p.rows = append(p.rows, progressRow{label: label, state: rowRunning})
}

// finish sets the running row's final state.
func (p *setupProgress) finish(state int, detail string) {
	for i := range p.rows {
		if p.rows[i].state == rowRunning {
			p.rows[i].state, p.rows[i].detail = state, detail
		}
	}
}

// offerRestart asks whether to restart into the installed version now.
func (m *InteractiveMode) offerRestart(version string) {
	dialog := tui.NewDialogSelect(fmt.Sprintf("%s %s installed", AppName, version), []tui.DialogOption{
		{Title: "Restart now", Description: "Reopen this session in " + version, Value: "restart"},
		{Title: "Later", Description: version + " runs the next time you start " + AppName, Value: "later"},
	}, "restart")
	chosen, ok := m.runDialogSelect(dialog, dialogMedium)
	if !ok || chosen.Value != "restart" {
		return
	}
	// A session with no messages has no file yet; restart into a new one.
	m.restartRequested, m.restartSession = true, ""
	if path := m.crashSessionFile(); path != "" {
		if _, err := os.Stat(path); err == nil {
			m.restartSession = path
		}
	}
	m.requestQuit()
	if !m.requestExit.Load() {
		m.restartRequested, m.restartSession = false, ""
	}
}

// RestartRequested reports whether the user chose to restart into an
// upgraded binary, and the session file to reopen ("" when unsaved).
func (m *InteractiveMode) RestartRequested() (string, bool) {
	return m.restartSession, m.restartRequested
}
