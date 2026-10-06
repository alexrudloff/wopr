package codingagent

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/alexrudloff/wopr/tui"
)

// systemPromptName is the saved prompt this session uses: its /prompt
// choice, else the setting, falling back to coding when that prompt is gone.
func (m *InteractiveMode) systemPromptName() string {
	name := m.sessionSystemPrompt
	if name == "" {
		name = m.settings().GetSystemPrompt()
	}
	if _, ok := SystemPromptText(m.opts.AgentDir, name); !ok {
		return CodingSystemPrompt
	}
	return name
}

// restoreSystemPrompt loads the session's /prompt choice and rebuilds the
// system prompt when it differs from the one in use: after startup on a
// resumed session, /new, or /resume.
func (m *InteractiveMode) restoreSystemPrompt() {
	m.sessionSystemPrompt = ""
	if session := m.currentSession(); session != nil {
		for _, entry := range session.Entries() {
			if entry.Base.Type != "custom" {
				continue
			}
			var custom struct {
				CustomType string `json:"customType"`
				Data       struct {
					Name string `json:"name"`
				} `json:"data"`
			}
			if json.Unmarshal(entry.Raw(), &custom) == nil && custom.CustomType == SystemPromptMessageType {
				m.sessionSystemPrompt = custom.Data.Name
			}
		}
	}
	m.refreshSystemPrompt()
}

// builtSystemPromptName is the saved prompt the agent's prompt was last
// built with.
func (m *InteractiveMode) builtSystemPromptName() string {
	return cmp.Or(m.builtSystemPrompt, m.settings().GetSystemPrompt())
}

// refreshSystemPrompt rebuilds the system prompt when the saved prompt it
// should use is not the one it was built with.
func (m *InteractiveMode) refreshSystemPrompt() {
	if m.systemPromptName() != m.builtSystemPromptName() {
		m.rebuildSystemPromptFromResources()
	}
}

// useSystemPrompt switches this session to the saved prompt name.
func (m *InteractiveMode) useSystemPrompt(name string) error {
	if _, ok := SystemPromptText(m.opts.AgentDir, name); !ok {
		return fmt.Errorf("no system prompt named %s (have: %s)", name, strings.Join(ListSystemPrompts(m.opts.AgentDir), ", "))
	}
	if session := m.currentSession(); session != nil {
		if err := session.AppendCustomEntry(SystemPromptMessageType, map[string]string{"name": name}); err != nil {
			return err
		}
	}
	m.sessionSystemPrompt = name
	m.rebuildSystemPromptFromResources()
	return nil
}

// promptCommand implements /prompt [name]: switch this session's system
// prompt, or pick one from a list.
func (m *InteractiveMode) promptCommand(args string) error {
	name := strings.TrimSpace(args)
	if name == "" {
		chosen, ok := m.pickSystemPrompt("System prompt for this session", m.systemPromptName())
		if !ok {
			return nil
		}
		name = chosen
	}
	if err := m.useSystemPrompt(name); err != nil {
		m.showError(err.Error())
		return nil
	}
	m.showStatus("System prompt: " + name)
	return nil
}

// pickSystemPrompt lists the saved prompts, current first in focus.
func (m *InteractiveMode) pickSystemPrompt(title, current string) (string, bool) {
	defaultName := m.settings().GetSystemPrompt()
	var options []tui.DialogOption
	for _, name := range ListSystemPrompts(m.opts.AgentDir) {
		var tags []string
		if name == m.systemPromptName() {
			tags = append(tags, "this session")
		}
		if name == defaultName {
			tags = append(tags, "default")
		}
		options = append(options, tui.DialogOption{Title: name, Footer: strings.Join(tags, " · "), Value: name})
	}
	chosen, ok := m.runDialogSelect(tui.NewDialogSelect(title, options, current), dialogMedium)
	return chosen.Value, ok
}

// manageSystemPrompts is /settings → System prompts: pick a prompt (or New
// prompt) and act on it, returning to the list after each action.
func (m *InteractiveMode) manageSystemPrompts() {
	const newPrompt = "\x00new"
	focus := m.systemPromptName()
	for {
		defaultName := m.settings().GetSystemPrompt()
		var options []tui.DialogOption
		for _, name := range ListSystemPrompts(m.opts.AgentDir) {
			var tags []string
			if name == m.systemPromptName() {
				tags = append(tags, "this session")
			}
			if name == defaultName {
				tags = append(tags, "default")
			}
			options = append(options, tui.DialogOption{Title: name, Footer: strings.Join(tags, " · "), Value: name})
		}
		options = append(options, tui.DialogOption{Title: "New prompt", Description: "Write a system prompt for other work: writing, video, research", Value: newPrompt})
		d := tui.NewDialogSelect("System prompts", options, focus)
		chosen, ok := m.runDialogSelect(d, dialogMedium)
		if !ok {
			return
		}
		if chosen.Value == newPrompt {
			if name := m.newSystemPrompt(); name != "" {
				focus = name
			}
			continue
		}
		focus = m.systemPromptActions(chosen.Value)
	}
}

// systemPromptActions shows what can be done with prompt name and does the
// chosen one; it returns the prompt to focus in the list afterwards.
func (m *InteractiveMode) systemPromptActions(name string) string {
	agentDir := m.opts.AgentDir
	options := []tui.DialogOption{
		{Title: "Use in this session", Value: "use"},
		{Title: "Make the default", Description: "New sessions start with it", Value: "default"},
		{Title: "Edit", Value: "edit"},
	}
	switch {
	case name != CodingSystemPrompt:
		options = append(options, tui.DialogOption{Title: "Rename", Value: "rename"}, tui.DialogOption{Title: "Delete", Value: "delete"})
	case SystemPromptOverridden(agentDir):
		options = append(options, tui.DialogOption{Title: "Reset to built-in", Value: "reset"})
	}
	chosen, ok := m.runDialogSelect(tui.NewDialogSelect(name, options, ""), dialogMedium)
	if !ok {
		return name
	}
	var err error
	switch chosen.Value {
	case "use":
		if err = m.useSystemPrompt(name); err == nil {
			m.showStatus("System prompt: " + name)
		}
	case "default":
		// The default is for new sessions; this one keeps its prompt.
		if current := m.systemPromptName(); m.sessionSystemPrompt == "" && current != name {
			err = m.useSystemPrompt(current)
		}
		if err == nil {
			err = m.opts.SettingsManager.UpdateGlobal(func(s *Settings) { s.SystemPrompt = name })
		}
		if err == nil {
			m.showStatus("Default system prompt: " + name)
		}
	case "edit":
		text, _ := SystemPromptText(agentDir, name)
		if m.editSystemPrompt(name, text) {
			m.afterSystemPromptChange(name)
		}
	case "rename":
		name, err = m.renameSystemPrompt(name)
	case "delete":
		if err = DeleteSystemPrompt(agentDir, name); err == nil {
			m.showStatus("Deleted system prompt " + name)
			m.afterSystemPromptChange(name)
			return CodingSystemPrompt
		}
	case "reset":
		if err = DeleteSystemPrompt(agentDir, name); err == nil {
			m.showStatus("coding prompt reset to built-in")
			m.afterSystemPromptChange(name)
		}
	}
	if err != nil {
		m.showError(err.Error())
	}
	return name
}

// afterSystemPromptChange rebuilds the system prompt when the prompt whose
// text changed (or was deleted) is the one in use.
func (m *InteractiveMode) afterSystemPromptChange(name string) {
	if name == m.builtSystemPromptName() {
		m.rebuildSystemPromptFromResources()
	}
}

// editSystemPrompt edits name's text in a dialog and saves it; it reports
// whether it saved.
func (m *InteractiveMode) editSystemPrompt(name, text string) bool {
	d := tui.NewTextEditDialog("System prompt · "+name, strings.TrimSpace(text), max(8, m.tuiInst.Height()/2))
	for {
		if !m.runDialog(modalOf(d), dialogLarge) || d.Cancelled() {
			return false
		}
		if err := SaveSystemPrompt(m.opts.AgentDir, name, d.Text()); err != nil {
			d.SetError(err.Error())
			continue
		}
		m.showStatus("Saved system prompt " + name)
		return true
	}
}

// newSystemPrompt asks for a name, then the text; it returns the saved
// prompt's name, or "" when cancelled.
func (m *InteractiveMode) newSystemPrompt() string {
	field := tui.NewTextField("Name", "", "e.g. writing", false)
	field.Hint = "Lowercase letters, digits, - and _. Use it with /prompt <name>."
	f := tui.NewForm("New system prompt", field)
	f.Submit = "Next"
	for {
		if !m.runDialog(modalOf(f), dialogMedium) || f.Cancelled() {
			return ""
		}
		name := field.Value()
		err := ValidSystemPromptName(name)
		if _, exists := SystemPromptText(m.opts.AgentDir, name); err == nil && exists {
			err = errors.New("a prompt named " + name + " already exists")
		}
		if err != nil {
			f.Error = err.Error()
			f.Reopen()
			continue
		}
		start := "You are a capable assistant working inside wopr. You help the user by reading files, running commands, and writing new files."
		if m.editSystemPrompt(name, start) {
			return name
		}
		return ""
	}
}

// renameSystemPrompt asks for a new name and renames; it returns the name
// the prompt ends up with.
func (m *InteractiveMode) renameSystemPrompt(name string) (string, error) {
	field := tui.NewTextField("Name", name, "", false)
	f := tui.NewForm("Rename "+name, field)
	for {
		if !m.runDialog(modalOf(f), dialogMedium) || f.Cancelled() {
			return name, nil
		}
		to := field.Value()
		if to == name {
			return name, nil
		}
		if err := RenameSystemPrompt(m.opts.AgentDir, name, to); err != nil {
			f.Error = err.Error()
			f.Reopen()
			continue
		}
		if m.sessionSystemPrompt == name {
			if err := m.useSystemPrompt(to); err != nil {
				return to, err
			}
		}
		if m.settings().SystemPrompt == name {
			if err := m.opts.SettingsManager.UpdateGlobal(func(s *Settings) { s.SystemPrompt = to }); err != nil {
				return to, err
			}
		}
		m.showStatus("Renamed " + name + " to " + to)
		return to, nil
	}
}
