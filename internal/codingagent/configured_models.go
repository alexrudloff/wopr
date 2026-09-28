package codingagent

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/internal/codingagent/router"
)

// configuredModel is a model set up in /setup and checked there (in use):
// what the model picker lists and Tab cycles.
type configuredModel struct {
	spec, name string
	// group is the connection's name, the picker's heading.
	group string
}

// configuredModels lists the configured models grouped by connection, in
// setup's order; while routing is on, each connection's models follow the
// routing order (strongest first). A model chosen with /model <name> or
// --model without being set up is not among them.
func (m *InteractiveMode) configuredModels() []configuredModel {
	w := &setupWizard{m: m}
	var ranking router.Ranking
	if r := m.sessionRouter(); r != nil && r.Available() {
		ranking = r.Config().Ranking
	}
	var out []configuredModel
	for _, c := range w.loadConnections() {
		var models []configuredModel
		for _, s := range c.Models {
			if !s.NoRouting {
				models = append(models, configuredModel{spec: s.spec(), name: cmp.Or(s.Name, s.Model), group: c.Name})
			}
		}
		if ranking != nil {
			rank := func(spec string) int {
				if i := slices.Index(ranking, spec); i >= 0 {
					return i
				}
				return len(ranking)
			}
			slices.SortStableFunc(models, func(a, b configuredModel) int { return cmp.Compare(rank(a.spec), rank(b.spec)) })
		}
		out = append(out, models...)
	}
	return out
}

// modeSpec parses a routing mode name as the model picker and Tab carry it;
// model specs always contain a slash.
func modeSpec(value string) (router.Objective, bool) {
	if strings.Contains(value, "/") {
		return "", false
	}
	return router.ParseObjective(value)
}

// stepModelMode is where Tab (step 1) and shift+tab (step -1) move: through
// the routing modes in order, then the configured models in order, wrapping
// around. Without routing there are no modes and it cycles the models. auto
// reports that the router picks the orchestrator under mode; otherwise
// current is the pinned model. It returns a mode name or a model spec.
func stepModelMode(step int, auto bool, mode, current string, modes, models []string) string {
	stops := append(slices.Clone(modes), models...)
	if len(stops) == 0 {
		return current
	}
	i := slices.Index(models, current)
	if i >= 0 {
		i += len(modes)
	}
	if auto && len(modes) > 0 {
		i = slices.Index(modes, mode)
	}
	if i < 0 {
		// A model outside the list: start at the first or last model.
		if len(models) > 0 && step > 0 {
			return models[0]
		}
		return stops[len(stops)-1]
	}
	return stops[((i+step)%len(stops)+len(stops))%len(stops)]
}

// cycleModelMode moves step stops through what Tab offers: the routing modes
// (only while routing is on), then the configured models, which run with the
// auto mode for subagents. Tab steps forward, shift+tab back.
func (m *InteractiveMode) cycleModelMode(ctx context.Context, step int) {
	r := m.sessionRouter()
	if r == nil {
		return
	}
	var modes []string
	for _, o := range r.Objectives() {
		modes = append(modes, string(o))
	}
	var models []string
	for _, c := range m.configuredModels() {
		models = append(models, c.spec)
	}
	m.tabbing = true
	defer func() { m.tabbing = false }()
	auto, mode, current := r.Auto(), string(r.Objective()), modelSpec(m.opts.Model)
	// A model that no longer switches (signed out, server gone) is skipped,
	// so Tab never sticks on it.
	for range len(models) + 1 {
		next := stepModelMode(step, auto, mode, current, modes, models)
		if o, ok := modeSpec(next); ok {
			m.selectRoutingMode(o, false)
			return
		}
		if r.Objective() != router.ObjectiveAuto {
			_ = r.SetObjective(router.ObjectiveAuto)
		}
		if next == modelSpec(m.opts.Model) {
			r.PinOrchestrator()
			m.saveRouting()
			break
		}
		err := m.switchModel(next)
		if err == nil {
			break
		}
		// Dropping the failed entry and asking again from the same state
		// yields the entry after it.
		models = slices.DeleteFunc(models, func(s string) bool { return s == next })
		m.showToast("warning", "", "Skipped "+next+": "+err.Error())
	}
	m.tuiInst.RequestRender()
}
