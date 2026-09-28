package codingagent

import (
	"path/filepath"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
)

// availableModelItems uses the composed runtime catalog and provider auth rather than a second interactive provider allowlist.
func (m *InteractiveMode) availableModelItems() []tui.ModelSelectorItem {
	registry, agentDir := m.opts.ModelRegistry, m.opts.AgentDir
	if registry == nil {
		registry = NewModelRegistry(agentDir)
		if agentDir != "" {
			if auth, err := ai.NewAuthStorage(filepath.Join(agentDir, "auth.json")); err == nil {
				registry.SetAuthStorage(auth)
			}
		}
	}
	auth := make(map[string]bool)
	var items []tui.ModelSelectorItem
	for _, model := range registry.RuntimeModels() {
		ready, checked := auth[model.Provider]
		if !checked {
			ready = registry.HasConfiguredAuth(model.Provider)
			auth[model.Provider] = ready
		}
		if ready {
			items = append(items, tui.ModelSelectorItem{Provider: model.Provider, ID: model.ID, Name: model.Name})
		}
	}
	return items
}
