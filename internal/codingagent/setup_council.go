package codingagent

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/alexrudloff/wopr/tui"
)

// councilLimits are the time limits the War council screen offers.
var councilLimits = []time.Duration{2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 20 * time.Minute}

// councilSummary is the war council's state on the main screen.
func (w *setupWizard) councilSummary() string {
	models := w.m.configuredModels()
	if len(models) == 0 {
		return ""
	}
	excluded, timeout := w.m.settings().GetWarCouncil()
	in := 0
	for _, c := range models {
		if !slices.Contains(excluded, c.spec) {
			in++
		}
	}
	return fmt.Sprintf("%d of %d · %s limit", in, len(models), formatLimit(timeout))
}

// councilScreen is the war council's checklist: every model you set up
// proposes in Global Thermonuclear War unless unchecked here, plus the
// time limit a member gets. Save keeps it, Cancel (or esc) discards.
func (w *setupWizard) councilScreen() {
	const save, cancel, limit = "\x00save", "\x00cancel", "\x00limit"
	models := w.m.configuredModels()
	excluded, timeout := w.m.settings().GetWarCouncil()
	checked := map[string]bool{}
	for _, c := range models {
		checked[c.spec] = !slices.Contains(excluded, c.spec)
	}
	build := func() []tui.DialogOption {
		var options []tui.DialogOption
		for _, c := range models {
			mark := "[ ] "
			if checked[c.spec] {
				mark = "[x] "
			}
			options = append(options, tui.DialogOption{Title: mark + c.name, Description: c.group, Category: "Members", Value: c.spec})
		}
		return append(options,
			tui.DialogOption{Title: "Time limit", Description: "a member still working after this is dropped", Footer: formatLimit(timeout), Category: "Members", Value: limit},
			tui.DialogOption{Title: "Save", Value: save, Pinned: true},
			tui.DialogOption{Title: "Cancel", Value: cancel, Pinned: true})
	}
	toggle := func(value string) {
		switch value {
		case save, cancel:
		case limit:
			i := slices.Index(councilLimits, timeout)
			timeout = councilLimits[(i+1)%len(councilLimits)]
		default:
			checked[value] = !checked[value]
		}
	}
	intro := []string{
		"Global Thermonuclear War runs your top model at max thinking; every checked model proposes on each prompt, in parallel, and the top model builds on the best parts. The top model itself sits out.",
		"enter or space checks · Save keeps",
	}
	highlight := ""
	for {
		d := tui.NewDialogSelect("War council", build(), "")
		d.Intro = intro
		d.Actions = []tui.DialogAction{{Title: "Check", Key: "space", Silent: true, Run: func(option tui.DialogOption) {
			toggle(option.Value)
			d.SetOptions(build())
		}}}
		if highlight != "" {
			d.Select(highlight)
		}
		chosen, ok := w.sel(d, "")
		switch {
		case !ok || chosen.Value == cancel:
			return
		case chosen.Value == save:
			var out []string
			for _, c := range models {
				if !checked[c.spec] {
					out = append(out, c.spec)
				}
			}
			if err := w.m.opts.SettingsManager.UpdateGlobal(func(s *Settings) {
				s.WarCouncil = &WarCouncilSettings{Excluded: out, TimeoutSeconds: int(timeout / time.Second)}
			}); err != nil {
				w.showError("Couldn't save the war council", err)
				continue
			}
			w.m.showFlash("War council saved")
			return
		}
		toggle(chosen.Value)
		highlight = chosen.Value
	}
}

// formatLimit is a time limit as the council screen shows it.
func formatLimit(d time.Duration) string {
	if d%time.Minute == 0 {
		return fmt.Sprintf("%d min", int(d/time.Minute))
	}
	return cmp.Or(d.Round(time.Second).String(), "0s")
}
