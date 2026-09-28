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

// nextModelMode is where Tab moves: through the routing modes in order,
// then the configured models in order, and from the last back to the first
// mode. Without routing there are no modes and Tab cycles the models. auto
// reports that the router picks the orchestrator under mode; otherwise
// current is the pinned model. It returns a mode name or a model spec.
func nextModelMode(auto bool, mode, current string, modes, models []string) string {
	if len(modes) == 0 {
		if len(models) == 0 {
			return current
		}
		return models[(slices.Index(models, current)+1)%len(models)]
	}
	if auto {
		i := slices.Index(modes, mode)
		switch {
		case i >= 0 && i < len(modes)-1:
			return modes[i+1]
		case i >= 0 && len(models) > 0:
			return models[0]
		}
		return modes[0]
	}
	i := slices.Index(models, current)
	switch {
	case len(models) == 0 || i == len(models)-1:
		return modes[0]
	case i < 0:
		return models[0]
	}
	return models[i+1]
}

// cycleModelMode moves to the next stop Tab offers: the routing modes (only
// while routing is on), then the configured models, which run with the
// auto mode for subagents.
func (m *InteractiveMode) cycleModelMode(ctx context.Context) {
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
		next := nextModelMode(auto, mode, current, modes, models)
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
