package codingagent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/internal/codingagent/prompts"
)

// Saved system prompts: named texts in <agentDir>/system-prompts/<name>.md
// that replace the persona part of the system prompt (prompts.Options.Persona).
// The built-in "coding" prompt is the default persona; a coding.md file
// overrides it.

// CodingSystemPrompt names the built-in prompt.
const CodingSystemPrompt = "coding"

// SystemPromptMessageType is the session entry that records a session's
// chosen prompt, so a resumed session keeps it.
const SystemPromptMessageType = "system_prompt"

var systemPromptName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func systemPromptsDir(agentDir string) string { return filepath.Join(agentDir, "system-prompts") }

func systemPromptPath(agentDir, name string) string {
	return filepath.Join(systemPromptsDir(agentDir), name+".md")
}

// ValidSystemPromptName reports why name can't name a prompt, or nil.
func ValidSystemPromptName(name string) error {
	if !systemPromptName.MatchString(name) {
		return errors.New("a prompt name is lowercase letters, digits, - and _ (e.g. writing)")
	}
	return nil
}

// ListSystemPrompts returns the saved prompt names, coding first.
func ListSystemPrompts(agentDir string) []string {
	names := []string{CodingSystemPrompt}
	entries, _ := os.ReadDir(systemPromptsDir(agentDir))
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if ok && !e.IsDir() && name != CodingSystemPrompt && ValidSystemPromptName(name) == nil {
			names = append(names, name)
		}
	}
	slices.Sort(names[1:])
	return names
}

// SystemPromptText returns a prompt's text for editing: the file, or the
// built-in persona for coding without one. ok is false for an unknown name.
func SystemPromptText(agentDir, name string) (text string, ok bool) {
	data, err := os.ReadFile(systemPromptPath(agentDir, name))
	if err == nil {
		return string(data), true
	}
	if name == CodingSystemPrompt {
		return prompts.DefaultPersona(), true
	}
	return "", false
}

// SystemPromptPersona returns the persona to build the system prompt with:
// "" for the built-in coding prompt or an unknown name, else the prompt's
// text.
func SystemPromptPersona(agentDir, name string) string {
	data, err := os.ReadFile(systemPromptPath(agentDir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SystemPromptOverridden reports whether coding.md overrides the built-in.
func SystemPromptOverridden(agentDir string) bool {
	_, err := os.Stat(systemPromptPath(agentDir, CodingSystemPrompt))
	return err == nil
}

// SaveSystemPrompt writes a prompt's text.
func SaveSystemPrompt(agentDir, name, text string) error {
	if err := ValidSystemPromptName(name); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("a prompt can't be empty")
	}
	if err := os.MkdirAll(systemPromptsDir(agentDir), 0o700); err != nil {
		return err
	}
	return os.WriteFile(systemPromptPath(agentDir, name), []byte(strings.TrimSpace(text)+"\n"), 0o600)
}

// DeleteSystemPrompt removes a prompt; for coding it restores the built-in.
func DeleteSystemPrompt(agentDir, name string) error {
	err := os.Remove(systemPromptPath(agentDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// RenameSystemPrompt renames a saved prompt. coding can't be renamed.
func RenameSystemPrompt(agentDir, from, to string) error {
	if from == CodingSystemPrompt {
		return errors.New("the coding prompt can't be renamed")
	}
	if err := ValidSystemPromptName(to); err != nil {
		return err
	}
	if _, err := os.Stat(systemPromptPath(agentDir, to)); err == nil || to == CodingSystemPrompt {
		return fmt.Errorf("a prompt named %s already exists", to)
	}
	return os.Rename(systemPromptPath(agentDir, from), systemPromptPath(agentDir, to))
}
