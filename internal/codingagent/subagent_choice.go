package codingagent

import "github.com/alexrudloff/wopr/tui"

// The subagents' own choice, next to the orchestrator's in /model: a
// routing mode (routing on), a configured model, or the orchestrator's
// model. The latest choice wins per target.

// subagentsSame is the session's choice for running subagents on the
// orchestrator's model.
const subagentsSame = "same"

// subagentChooser is the session surface for the subagents' choice.
type subagentChooser interface {
	SubagentChoice() string
	SetSubagentChoice(string) error
}

func (m *InteractiveMode) subagentChooser() subagentChooser {
	c, _ := m.opts.SessionHandle.(subagentChooser)
	return c
}

// subagentChoice is the subagents' choice, subagentsSame without a session.
func (m *InteractiveMode) subagentChoice() string {
	if c := m.subagentChooser(); c != nil {
		return c.SubagentChoice()
	}
	return subagentsSame
}

// choiceLabel names a choice on screen: a mode's title, "Same as
// orchestrator", or the model's name.
func (m *InteractiveMode) choiceLabel(choice string) string {
	if choice == subagentsSame {
		return "Same as orchestrator"
	}
	if o, ok := modeSpec(choice); ok {
		return modeTitle(o)
	}
	provider, id := splitSpec(choice)
	if name := m.providerName(provider); name != "" {
		return m.modelName(provider, id) + " " + name
	}
	return m.modelName(provider, id)
}

// orchestratorChoice is the orchestrator's choice in the same terms: the
// mode while the router picks it, else its model's spec.
func (m *InteractiveMode) orchestratorChoice() string {
	if r := m.sessionRouter(); r != nil && r.Auto() {
		return string(r.Objective())
	}
	return modelSpec(m.opts.Model)
}

// subagentsDiffer reports whether the subagents run on something other
// than the orchestrator's choice, so the UI names both.
func (m *InteractiveMode) subagentsDiffer() bool {
	sub := m.subagentChoice()
	return sub != subagentsSame && sub != m.orchestratorChoice()
}

// modelMenu is /model: the orchestrator's and the subagents' choices, each
// opening its picker.
func (m *InteractiveMode) modelMenu() {
	for {
		orchestrator := "No model"
		if choice := m.orchestratorChoice(); choice != "" {
			orchestrator = m.choiceLabel(choice)
		}
		d := tui.NewDialogSelect("Models", []tui.DialogOption{
			{Title: "Orchestrator", Description: "holds the conversation", Footer: orchestrator, Value: "orchestrator"},
			{Title: "Subagents", Description: "run task briefs", Footer: m.choiceLabel(m.subagentChoice()), Value: "subagents"},
			{Title: "Back", Value: "back", Pinned: true},
		}, "orchestrator")
		chosen, ok := m.runDialogSelect(d, dialogMedium)
		if !ok || chosen.Value == "back" {
			return
		}
		if chosen.Value == "orchestrator" {
			spec, ok := m.pickModelDialog("")
			if !ok {
				continue
			}
			m.applyOrchestratorChoice(spec)
			return
		}
		if choice, ok := m.pickSubagentDialog(); ok {
			m.applySubagentChoice(choice)
			return
		}
	}
}

// applyOrchestratorChoice makes spec (a mode or a model) the orchestrator's
// choice, as the picker does.
func (m *InteractiveMode) applyOrchestratorChoice(spec string) {
	if o, ok := modeSpec(spec); ok {
		m.selectRoutingMode(o, true)
		return
	}
	if err := m.switchModel(spec); err != nil {
		m.appendToChat(tui.NewText("\033[31mmodel switch failed: " + err.Error() + "\033[0m"))
		return
	}
	m.showModelSelection(spec)
}

// pickSubagentDialog lists the subagents' choices: with routing on the
// modes, else "Same as orchestrator", then the configured models.
func (m *InteractiveMode) pickSubagentDialog() (string, bool) {
	var options []tui.DialogOption
	if r := m.sessionRouter(); r != nil && r.Available() {
		for _, o := range r.Objectives() {
			options = append(options, tui.DialogOption{Title: modeTitle(o), Category: "Modes", Description: modeDescriptions[o], Value: string(o)})
		}
	} else {
		options = append(options, tui.DialogOption{Title: "Same as orchestrator", Description: "subagents run on the orchestrator's model", Value: subagentsSame})
	}
	for _, c := range m.configuredModels() {
		options = append(options, tui.DialogOption{Title: c.name, Category: c.group, Value: c.spec})
	}
	d := tui.NewDialogSelect("Subagents", options, m.subagentChoice())
	d.Flat = true
	chosen, ok := m.runDialogSelect(d, dialogMedium)
	return chosen.Value, ok
}

// applySubagentChoice sets the subagents' choice and saves it.
func (m *InteractiveMode) applySubagentChoice(choice string) {
	c := m.subagentChooser()
	if c == nil {
		return
	}
	if err := c.SetSubagentChoice(choice); err != nil {
		m.showToast("error", "", err.Error())
		return
	}
	m.saveRouting()
	m.showFlash("Subagents: " + m.choiceLabel(choice))
	m.tuiInst.RequestRender()
}

// subagentsNote is the subagents' choice for the prompt line and sidebar,
// "" when they follow the orchestrator's.
func (m *InteractiveMode) subagentsNote() string {
	if !m.subagentsDiffer() {
		return ""
	}
	sub := m.subagentChoice()
	if o, ok := modeSpec(sub); ok {
		return "subagents " + string(o)
	}
	return "subagents " + m.choiceLabel(sub)
}
