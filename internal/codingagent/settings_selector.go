package codingagent

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
)

// modelThinkingMenu sets per-model default thinking levels in two steps: pick
// a model, then its level. Esc on the level step goes back to the models,
// and a chosen level returns there too; Esc on the models closes the menu.
type modelThinkingMenu struct {
	models        []*ai.Model // current model first, then the default, then by provider
	overrides     map[string]string
	globalDefault string
	firstModel    string
	onChange      func(*ai.Model, string)
	onDone        func()

	model  *ai.Model // the model whose level is being picked, nil on the first step
	active *tui.SelectSubmenuComponent
}

func newModelThinkingMenu(settings Settings, models []*ai.Model, currentModel string, onChange func(*ai.Model, string), onDone func()) *modelThinkingMenu {
	defaultModel := settings.DefaultProvider + "/" + settings.DefaultModel
	sorted := slices.Clone(models)
	collator := collate.New(language.Und)
	rank := func(m *ai.Model) int {
		switch modelSpec(m) {
		case currentModel:
			return 0
		case defaultModel:
			return 1
		}
		return 2
	}
	slices.SortStableFunc(sorted, func(a, b *ai.Model) int {
		if c := cmp.Compare(rank(a), rank(b)); c != 0 || rank(a) < 2 {
			return c
		}
		return collator.CompareString(a.ProviderMeta.ProviderID, b.ProviderMeta.ProviderID)
	})
	menu := &modelThinkingMenu{
		models:        sorted,
		overrides:     maps.Clone(settings.ModelThinkingLevels),
		globalDefault: cmp.Or(settings.DefaultThinkingLevel, DefaultThinkingLevel),
		firstModel:    cmp.Or(currentModel, defaultModel),
		onChange:      onChange,
		onDone:        onDone,
	}
	if menu.overrides == nil {
		menu.overrides = map[string]string{}
	}
	menu.showModels()
	return menu
}

func (s *modelThinkingMenu) showModels() {
	s.model = nil
	items := make([]tui.SelectItem, 0, len(s.models))
	for _, model := range s.models {
		key := modelSpec(model)
		items = append(items, tui.SelectItem{Value: key, Label: model.ID + " " + tui.ActiveTheme().Muted + "[" + model.ProviderMeta.ProviderID + "]\x1b[39m", Description: s.overrides[key]})
	}
	if len(items) == 0 {
		items = append(items, tui.SelectItem{Value: "__none__", Label: "No models available", Description: "Log in to a provider or configure an API key first"})
	}
	s.active = tui.NewSelectSubmenu("Per-Model Thinking Level", "Step 1/2 · Select a model to configure", items, s.firstModel,
		tui.SelectSubmenuOptions{Searchable: true, MinPrimaryColumnWidth: 12, MaxPrimaryColumnWidth: 46})
}

func (s *modelThinkingMenu) showLevels(model *ai.Model) {
	s.model = model
	key := modelSpec(model)
	levels := levelsForModel(model)
	items := make([]tui.SelectItem, 0, len(levels)+1)
	for _, level := range levels {
		label := "  " + level
		if level == s.overrides[key] {
			label = "✓ " + level
		}
		items = append(items, tui.SelectItem{Value: level, Label: label, Description: thinkingDescriptions[level]})
	}
	if _, exists := s.overrides[key]; exists {
		items = append(items, tui.SelectItem{Value: modelThinkingClearOverrideValue, Label: "  (clear override)", Description: "Revert to global default (" + s.globalDefault + ")"})
	}
	title := fmt.Sprintf("Thinking Level for %s [%s]", model.ID, model.ProviderMeta.ProviderID)
	s.active = tui.NewSelectSubmenu(title, "Step 2/2 · Select default thinking level for this model", items, s.overrides[key])
}

func (s *modelThinkingMenu) Render(width int) []string { return s.active.Render(width) }
func (s *modelThinkingMenu) Invalidate()               { s.active.Invalidate() }

func (s *modelThinkingMenu) HandleInput(data string) {
	s.active.HandleInput(data)
	if !s.active.Done() {
		return
	}
	switch {
	case s.model == nil && s.active.Cancelled():
		s.onDone()
	case s.model == nil:
		if model := s.modelFor(s.active.SelectedValue()); model != nil {
			s.showLevels(model)
		} else {
			s.showModels()
		}
	case s.active.Cancelled():
		s.showModels()
	default:
		level := s.active.SelectedValue()
		s.onChange(s.model, level)
		if level == modelThinkingClearOverrideValue {
			delete(s.overrides, modelSpec(s.model))
		} else {
			s.overrides[modelSpec(s.model)] = level
		}
		s.showModels()
	}
}

func (s *modelThinkingMenu) modelFor(key string) *ai.Model {
	for _, model := range s.models {
		if modelSpec(model) == key {
			return model
		}
	}
	return nil
}

func (m *InteractiveMode) modelThinkingSettingsSubmenu(_ string, done func(*string)) tui.Component {
	var models []*ai.Model
	if m.opts.ModelRegistry != nil {
		for _, entry := range m.opts.ModelRegistry.GetAvailable() {
			generated := ai.CatalogModel{Provider: entry.ProviderID, ID: entry.ModelID, Reasoning: entry.Reasoning, ThinkingLevelMap: entry.ThinkingLevelMap}
			models = append(models, generated.ToModel())
		}
	}
	return newModelThinkingMenu(m.settings(), models, modelSpec(m.opts.Model), func(model *ai.Model, level string) {
		var err error
		if level == modelThinkingClearOverrideValue {
			err = m.opts.SettingsManager.RemoveModelThinkingLevel(model.ProviderMeta.ProviderID, model.ID)
		} else {
			err = m.opts.SettingsManager.SetModelThinkingLevel(model.ProviderMeta.ProviderID, model.ID, level)
		}
		if err != nil {
			m.showError(err.Error())
			return
		}
		if modelSpec(model) == modelSpec(m.opts.Model) {
			if level == modelThinkingClearOverrideValue {
				level = cmp.Or(m.settings().DefaultThinkingLevel, DefaultThinkingLevel)
			}
			if err := m.applyThinkingLevel(string(ai.ClampThinkingLevel(m.opts.Model, ai.ThinkingLevel(level))), false); err != nil {
				m.showError(err.Error())
			}
		}
	}, func() {
		summary := "none"
		if count := len(m.settings().ModelThinkingLevels); count != 0 {
			summary = fmt.Sprintf("%d configured", count)
		}
		done(&summary)
	})
}
