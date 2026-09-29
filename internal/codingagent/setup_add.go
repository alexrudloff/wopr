package codingagent

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/tui"
)

// Adding a model: pick it and it is saved; measuring starts by itself.
// New models join the bottom of the ranking (strongest first, in
// router.json), which only model routing uses: it is reordered in Model
// routing, and routing's strength numbers come from it (see
// router.Strengths). Nobody types a number.

// addModels saves the new models at the bottom of the ranking and starts
// measuring them. It returns the models added.
func (w *setupWizard) addModels(picked []*setupModel) []*setupModel {
	if len(picked) == 0 {
		return nil
	}
	ranking := w.currentRanking()
	var added []string
	for _, s := range picked {
		ranking = append(ranking, s.spec())
		added = append(added, cmp.Or(s.Name, s.Model))
	}
	if err := w.saveAdded(picked, ranking); err != nil {
		w.showError("Could not add the models", err)
		return nil
	}
	w.reload()
	cost := 0.0
	for _, s := range picked {
		w.m.measureModel(s)
		if s.Tier == router.CostPaid {
			cost += requestCost(s.spec())
		}
	}
	message := "Added " + strings.Join(added, ", ") + ". Measuring speed in the background."
	if cost > 0 {
		message += fmt.Sprintf(" Measuring costs about $%.4f.", cost)
	}
	w.m.showFlash(message)
	return picked
}

// saveAdded writes new models: an endpoint's models.json entries and key,
// their places in the ranking, and the strengths the ranking gives every
// ranked model.
func (w *setupWizard) saveAdded(models []*setupModel, ranking router.Ranking) error {
	var defined []*setupModel
	for _, s := range models {
		if s.Endpoint == nil {
			continue
		}
		if s.Endpoint.APIKey != "" && !s.Endpoint.Existing {
			if err := w.setStoredKey(s.Provider, s.Endpoint.APIKey, "add a key"); err != nil {
				return err
			}
		}
		if s.Context == 0 {
			s.Context = defaultContextWindow
		}
		defined = append(defined, s)
	}
	if len(defined) > 0 {
		if err := w.writeConfig("models.json", func(src string) (string, error) { return mergeModelsJSON(src, defined) }); err != nil {
			return err
		}
		if w.m.opts.ModelRegistry != nil {
			w.m.opts.ModelRegistry.Refresh()
		}
	}
	return w.saveRanking(ranking, models, nil)
}

// defaultContextWindow is used when a server doesn't report its window;
// the model's settings can change it.
const defaultContextWindow = ai.DefaultContextWindow

// ─── The ranking ─────────────────────────────────────────────────────────

// currentRanking is the saved ranking, limited to the models configured
// now; a configured model it doesn't list (or a whole configuration from
// before rankings) is placed by its current capability.
func (w *setupWizard) currentRanking() router.Ranking {
	var specs []string
	caps := map[string]float64{}
	for _, c := range w.loadConnections() {
		for _, s := range c.Models {
			specs = append(specs, s.spec())
			caps[s.spec()] = s.Capability
		}
	}
	var saved router.Ranking
	if r := w.m.sessionRouter(); r != nil {
		saved = r.Config().Ranking
	}
	var out router.Ranking
	placed := map[string]bool{}
	for _, spec := range saved {
		if _, ok := caps[spec]; ok && !placed[spec] {
			out = append(out, spec)
			placed[spec] = true
		}
	}
	var rest []string
	for _, spec := range specs {
		if !placed[spec] {
			rest = append(rest, spec)
		}
	}
	if len(out) == 0 {
		return router.RankingFrom(caps, rest)
	}
	for _, spec := range rest {
		out = insertByStrength(out, spec, caps[spec])
	}
	return out
}

// insertByStrength places spec at the position its capability implies:
// above the first weaker model.
func insertByStrength(r router.Ranking, spec string, capability float64) router.Ranking {
	strengths := router.Strengths(r)
	for i, other := range r {
		if strengths[other] < capability {
			return slices.Insert(r, i, spec)
		}
	}
	return append(r, spec)
}

func splitSpec(spec string) (string, string) {
	provider, model, _ := strings.Cut(spec, "/")
	return provider, model
}

// saveRanking writes the ranking and the strengths it gives: new models
// join routing with theirs, and every configured model whose strength
// changed is updated. changes are other edits of configured models
// (routing on or off, abliterated) saved with it.
func (w *setupWizard) saveRanking(r router.Ranking, added, changes []*setupModel) error {
	strengths := router.Strengths(r)
	for _, s := range added {
		s.Capability = strengths[s.spec()]
	}
	existing := slices.Clone(changes)
	edited := map[string]bool{}
	for _, s := range changes {
		edited[s.spec()] = true
		if v, ok := strengths[s.spec()]; ok {
			s.Capability = v
		}
	}
	for _, c := range w.loadConnections() {
		for _, s := range c.Models {
			if edited[s.spec()] || s.NoRouting || s.Ref == nil {
				continue
			}
			if v, ok := strengths[s.spec()]; ok && v != s.Capability {
				s.Capability, s.Edited = v, true
				existing = append(existing, s)
			}
		}
	}
	for _, s := range changes {
		s.Edited = true
	}
	ranking, err := compactJSON(r)
	if err != nil {
		return err
	}
	return w.writeConfig(router.ConfigFileName, func(src string) (string, error) {
		out, err := mergeRouterJSON(src, routerPlan{Models: added, Existing: existing})
		if err != nil {
			return "", err
		}
		return jsoncSet(out, ranking, "ranking")
	})
}

// ─── Picking ─────────────────────────────────────────────────────────────

// pickCatalogModels shows a signed-in provider's models, as the provider
// lists them, checked when routing uses them, and applies what the user
// changes; it returns the new models to add.
func (w *setupWizard) pickCatalogModels(provider, providerName string, kind setupKind) []*setupModel {
	var inUse []string
	for _, c := range w.loadConnections() {
		if c.ID == provider {
			for _, s := range c.Models {
				if !s.NoRouting {
					inUse = append(inUse, s.Model)
				}
			}
		}
	}
	models, note := w.providerModels(provider, providerName, inUse)
	if len(models) == 0 {
		w.showError("No models are available from "+providerName+".", errors.New(note))
		return nil
	}
	items := make([]tui.ModelSelectorItem, 0, len(models))
	byID := map[string]remoteModel{}
	for _, m := range models {
		items = append(items, tui.ModelSelectorItem{Provider: provider, ID: m.ID, Name: m.Name})
		byID[m.ID] = m
	}
	return w.chooseModels("Models from "+providerName, items, note, func(item tui.ModelSelectorItem) *setupModel {
		m := byID[item.ID]
		return &setupModel{Kind: kind, Provider: provider, Model: item.ID, Name: m.Name, Tier: inferTier(kind, "", provider), Context: m.Context, MaxOutput: m.MaxOutput}
	})
}

// pickEndpointModels shows an endpoint's models, checked when routing uses
// them, and applies what the user changes; it returns the new models to
// add.
func (w *setupWizard) pickEndpointModels(ep *setupEndpoint, models []remoteModel) []*setupModel {
	items := make([]tui.ModelSelectorItem, 0, len(models))
	byID := map[string]remoteModel{}
	for _, model := range models {
		items = append(items, tui.ModelSelectorItem{Provider: ep.ID, ID: model.ID, Name: model.Name})
		byID[model.ID] = model
	}
	return w.chooseModels(fmt.Sprintf("Models from %s (%d)", ep.Name, len(items)), items, "", func(item tui.ModelSelectorItem) *setupModel {
		remote := byID[item.ID]
		return &setupModel{Kind: setupViaEndpoint, Provider: ep.ID, Model: item.ID, Name: remote.Name, Endpoint: ep,
			Context: remote.Context, MaxOutput: remote.MaxOutput, Tier: inferTier(setupViaEndpoint, ep.BaseURL, ep.ID)}
	})
}

// chooseModels is the checklist of a connection's models, where checked
// means routing uses the model: models in use start checked, and nothing
// else. Enter applies both ways: a newly checked model that was configured
// before goes back into routing, one that wasn't is returned to be added,
// and an unchecked one stops being used (after one confirmation).
func (w *setupWizard) chooseModels(title string, items []tui.ModelSelectorItem, note string, newModel func(tui.ModelSelectorItem) *setupModel) []*setupModel {
	configured := map[string]*setupModel{}
	for _, c := range w.loadConnections() {
		for _, s := range c.Models {
			configured[s.spec()] = s
		}
	}
	inUse := map[string]bool{}
	checked := map[string]bool{}
	for _, item := range items {
		if s := configured[item.FQ()]; s != nil && !s.NoRouting {
			inUse[item.FQ()], checked[item.FQ()] = true, true
		}
	}
	if !w.multiSelect(title, items, checked, note) {
		return nil
	}
	var picked, stops, returning []*setupModel
	var stopNames []string
	for _, item := range items {
		spec := item.FQ()
		s := configured[spec]
		switch {
		case checked[spec] && !inUse[spec] && s != nil:
			s.NoRouting = false
			returning = append(returning, s)
		case checked[spec] && !inUse[spec]:
			picked = append(picked, newModel(item))
		case !checked[spec] && inUse[spec]:
			s.NoRouting = true
			stops = append(stops, s)
			stopNames = append(stopNames, cmp.Or(s.Name, s.Model))
		}
	}
	if len(stops)+len(returning) > 0 {
		stopped := map[string]bool{}
		for _, s := range stops {
			stopped[s.spec()] = true
		}
		ranking := slices.DeleteFunc(w.currentRanking(), func(spec string) bool { return stopped[spec] })
		if err := w.saveRanking(ranking, nil, append(stops, returning...)); err != nil {
			w.showError("Could not save", err)
		} else {
			w.reload()
			if len(stops) > 0 {
				w.m.showFlash("Stopped using " + strings.Join(stopNames, ", "))
			}
		}
	}
	return picked
}

// multiSelect is a checklist: enter or space checks or unchecks the
// highlighted model, Next applies, and Cancel (or esc) discards. False
// means cancelled.
func (w *setupWizard) multiSelect(title string, items []tui.ModelSelectorItem, checked map[string]bool, note string) bool {
	const next, cancel = "\x00next", "\x00cancel"
	build := func() []tui.DialogOption {
		options := make([]tui.DialogOption, 0, len(items)+2)
		for _, item := range items {
			mark := "[ ] "
			if checked[item.FQ()] {
				mark = "[x] "
			}
			options = append(options, tui.DialogOption{Title: mark + cmp.Or(item.Name, item.ID), Description: descIfDifferent(item.ID, item.Name), Value: item.FQ()})
		}
		return append(options,
			tui.DialogOption{Title: "Next", Value: next, Pinned: true},
			tui.DialogOption{Title: "Cancel", Value: cancel, Pinned: true})
	}
	d := tui.NewDialogSelect(title, build(), "")
	intro := []string{"enter or space checks · Next saves"}
	if note != "" {
		intro = append([]string{note}, intro...)
	}
	d.Intro = intro
	d.Actions = []tui.DialogAction{{Title: "Check", Key: "space", Silent: true, Run: func(option tui.DialogOption) {
		if option.Value != next && option.Value != cancel {
			checked[option.Value] = !checked[option.Value]
			d.SetOptions(build())
		}
	}}}
	for {
		chosen, ok := w.sel(d, "")
		switch {
		case !ok || chosen.Value == cancel:
			return false
		case chosen.Value == next:
			return true
		}
		// Enter on a model checks or unchecks it, and the list stays.
		checked[chosen.Value] = !checked[chosen.Value]
		d = tui.NewDialogSelect(title, build(), "")
		d.Intro = intro
		d.Actions = []tui.DialogAction{{Title: "Check", Key: "space", Silent: true, Run: func(option tui.DialogOption) {
			if option.Value != next && option.Value != cancel {
				checked[option.Value] = !checked[option.Value]
				d.SetOptions(build())
			}
		}}}
		d.Select(chosen.Value)
	}
}

func descIfDifferent(id, name string) string {
	if name == "" || name == id {
		return ""
	}
	return id
}
