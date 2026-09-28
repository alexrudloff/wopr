package codingagent

import (
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/ai"
)

func (m *InteractiveMode) handleModelPicker() { m.modelMenu() }

// cycleModel moves to the next (or previous) configured model, the models
// /model lists. Bound to app.model.cycleForward / cycleBackward.
func (m *InteractiveMode) cycleModel(forward bool) {
	var specs []string
	for _, c := range m.configuredModels() {
		specs = append(specs, c.spec)
	}
	if len(specs) <= 1 {
		m.statusLine.Flash(map[bool]string{true: "No models set up yet: run /setup", false: "Only one model is set up"}[len(specs) == 0])
		return
	}
	i := slices.Index(specs, modelSpec(m.opts.Model))
	step := 1
	if !forward {
		step = len(specs) - 1
	}
	next := specs[(max(i, 0)+step)%len(specs)]
	if i < 0 && forward {
		next = specs[0]
	}
	if err := m.switchModel(next); err != nil {
		m.statusLine.Flash("Model switch failed: " + err.Error())
		return
	}
	m.statusLine.Flash("Switched to " + m.modelName(splitSpec(next)))
}

func modelSpec(model *ai.Model) string {
	if model == nil {
		return ""
	}
	providerID := model.ProviderMeta.ProviderID
	if providerID == "" && model.Provider != nil {
		providerID = model.Provider.ID()
	}
	if providerID == "" || strings.HasPrefix(model.ID, providerID+"/") {
		return model.ID
	}
	return providerID + "/" + model.ID
}
