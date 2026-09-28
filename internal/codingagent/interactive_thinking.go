package codingagent

import (
	"cmp"
	"slices"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
)

// ─── Thinking level helpers ──────────────────────────────────────

// levelsForModel returns the supported thinking levels for the model. The returned
// slice excludes levels explicitly mapped to null in the model's
// ThinkingLevelMap (e.g. gpt-5-mini maps "off" → null, so the user
// cannot disable thinking for that model).
func levelsForModel(model *ai.Model) []string {
	levels := ai.GetSupportedThinkingLevels(model)
	out := make([]string, len(levels))
	for i, l := range levels {
		out[i] = string(l)
	}
	return out
}

// refreshThinkingLevel re-reads the thinking level from the agent after a
// model switch, which applies the target model's level. The footer and
// editor border cache it, so each switch refreshes them.
func (m *InteractiveMode) refreshThinkingLevel() {
	if m.agent == nil {
		return
	}
	level := string(cmp.Or(m.agent.ThinkingLevel(), ai.ThinkingOff))
	m.thinkingLevel = level
	m.editor.ThinkingLevel = level
	m.editor.Invalidate()
	m.statusLine.SetThinkingLevel(level)
}

// maxThinkingIndex returns the highest index in the model's level slice that
// the model supports. 0 means no thinking support (level is always "off").
// Uses slices.Index so adding new levels never requires updating this function.
func maxThinkingIndex(model *ai.Model) int {
	if model == nil {
		return 0
	}
	levels := levelsForModel(model)
	idx := slices.Index(levels, string(model.Capabilities.MaxThinking))
	if idx < 0 {
		return 0
	}
	return idx
}

// initThinkingLevel sets the initial thinking level from settings, clamped
// to model capabilities. Called once during Run() setup.
func (m *InteractiveMode) initThinkingLevel() {
	m.hideThinking = m.settings().GetHideThinkingBlock()

	// Start from the persisted default (if any), fallback to "medium".
	// --thinking flag overrides the setting.
	start := m.settings().DefaultThinkingLevel
	if model := m.opts.Model; model != nil {
		if perModel := m.settings().ModelThinkingLevels[model.ProviderMeta.ProviderID+"/"+model.ID]; perModel != "" {
			start = perModel
		}
	}
	start = cmp.Or(m.opts.ThinkingLevel, start)
	start = cmp.Or(start, "medium")

	levels := levelsForModel(m.opts.Model)
	maxIdx := maxThinkingIndex(m.opts.Model)
	if maxIdx == 0 {
		start = "off"
	} else {
		// Find the level in the cycle and clamp to max supported.
		idx := min(max(slices.Index(levels, start), 0), maxIdx)
		start = levels[idx]
	}

	m.thinkingLevel = start
	m.agent.SetThinkingLevel(ai.ThinkingLevel(start))
	m.editor.ThinkingLevel = start
	m.statusLine.SetThinkingLevel(start)
}

// cycleThinkingLevel advances to the next thinking level and updates state.
func (m *InteractiveMode) cycleThinkingLevel() {
	if m.routingEnabled() && m.sessionRouter().Engine() == router.EngineJev {
		m.showFlash("Jev sets the thinking level while it routes")
		return
	}
	maxIdx := maxThinkingIndex(m.opts.Model)
	if maxIdx == 0 {
		m.statusLine.Flash("Current model does not support thinking")
		return
	}

	levels := levelsForModel(m.opts.Model)
	cur := max(slices.Index(levels, m.thinkingLevel), 0)
	next := (cur + 1) % (maxIdx + 1)
	m.selectThinkingLevel(levels[next], false)
}

// selectThinkingLevel applies an in-session choice and reports it to the user.
// Only an explicit save also changes the global default.
func (m *InteractiveMode) selectThinkingLevel(level string, persist bool) {
	if err := m.applyThinkingLevel(level, persist); err != nil {
		m.showError(err.Error())
		return
	}
	message := "Thinking level: " + level
	if persist {
		message = "Default thinking level: " + level
	}
	m.statusLine.Flash(message)
}

// applyThinkingLevel updates reasoning without adding a selection notice to the transcript.
func (m *InteractiveMode) applyThinkingLevel(level string, persist bool) error {
	prev := m.thinkingLevel
	m.thinkingLevel = level
	m.agent.SetThinkingLevel(ai.ThinkingLevel(level))
	m.editor.ThinkingLevel = level
	m.editor.Invalidate()

	if persist {
		if m.opts.SettingsManager != nil {
			if err := m.opts.SettingsManager.SetDefaultThinkingLevel(level); err != nil {
				return err
			}
		}
	}
	if level != prev && m.currentSession() != nil {
		if err := m.currentSession().AppendThinkingLevelChange(level); err != nil {
			return err
		}
	}
	m.statusLine.SetThinkingLevel(level)
	return nil
}

// showThinkingSelector runs the /thinking selector in the editor slot. Enter
// selects a level for this session; app.thinking.save also saves it as the
// default.
func (m *InteractiveMode) showThinkingSelector() {
	done := false
	selectLevel := func(level string, persist bool) {
		m.selectThinkingLevel(level, persist)
		done = true
	}
	current := cmp.Or(m.thinkingLevel, DefaultThinkingLevel)
	defaultLevel := cmp.Or(m.settings().DefaultThinkingLevel, DefaultThinkingLevel)
	selector := NewThinkingSelectorComponent(
		current,
		levelsForModel(m.opts.Model),
		func(level string) { selectLevel(level, false) },
		func() { done = true },
		func(level string) { selectLevel(level, true) },
		defaultLevel,
	)
	m.runDialog(modal{component: selector, handleInput: selector.HandleInput, done: func() bool { return done }}, dialogLarge)
}

// toggleThinkingVisibility flips hideThinking and updates all visible blocks.
func (m *InteractiveMode) toggleThinkingVisibility() {
	m.hideThinking = !m.hideThinking

	// Update every assistant block in the session. Single method call per block.
	for _, b := range m.assistantBlocks {
		b.SetHiddenThinking(m.hideThinking)
	}

	// Persist preference, so /reload re-reads the toggled value.
	if m.opts.SettingsManager != nil {
		hide := m.hideThinking
		_ = m.opts.SettingsManager.UpdateGlobal(func(s *Settings) { s.HideThinkingBlock = &hide })
	}

	visibility := "visible"
	if m.hideThinking {
		visibility = "hidden"
	}
	m.statusLine.Flash("Thinking blocks: " + visibility)
}
