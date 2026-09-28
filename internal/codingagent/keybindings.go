package codingagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/tui"
)

type KeyID = string

type KeybindingDefinition struct {
	DefaultKeys []KeyID
	Description string
}

type KeybindingsManager struct {
	definitions  map[string]KeybindingDefinition
	ordered      []string
	userBindings map[string][]KeyID
	resolved     map[string][]KeyID
	configPath   string
	platform     tui.KeybindingPlatform
	// merged is the single keybindings manager: the tui.* and app.*
	// table with every user override. syncToTUI installs it for tui components.
	merged *tui.TUIKeybindingsManager
}

// appKeybindingDefinitionsFor returns the app.* keybinding rows for one
// platform column.
func appKeybindingDefinitionsFor(platform tui.KeybindingPlatform) map[string]KeybindingDefinition {
	return map[string]KeybindingDefinition{
		"app.interrupt":                 {DefaultKeys: []KeyID{"escape"}, Description: "Cancel or abort"},
		"app.clear":                     {DefaultKeys: []KeyID{"ctrl+c"}, Description: "Clear editor"},
		"app.exit":                      {DefaultKeys: []KeyID{"ctrl+d"}, Description: "Exit when editor is empty"},
		"app.suspend":                   {DefaultKeys: tui.PlatformKeys{Other: []KeyID{"ctrl+z"}, Win32: []KeyID{}}.For(platform), Description: "Suspend to background"},
		"app.thinking.cycle":            {DefaultKeys: []KeyID{"ctrl+t"}, Description: "Cycle thinking level"},
		"app.thinking.save":             {DefaultKeys: []KeyID{"ctrl+s"}, Description: "Save thinking level"},
		"app.commandPalette":            {DefaultKeys: []KeyID{"ctrl+p"}, Description: "Open the command palette"},
		"app.model.cycleForward":        {DefaultKeys: []KeyID{"f2"}, Description: "Cycle to next model"},
		"app.model.cycleBackward":       {DefaultKeys: []KeyID{"shift+f2"}, Description: "Cycle to previous model"},
		"app.model.select":              {DefaultKeys: []KeyID{"ctrl+l"}, Description: "Open model selector"},
		"app.tools.expand":              {DefaultKeys: []KeyID{"ctrl+o"}, Description: "Toggle tool details"},
		"app.thinking.toggle":           {DefaultKeys: nil, Description: "Toggle thinking blocks"},
		"app.editor.external":           {DefaultKeys: nil, Description: "Open external editor"},
		"app.message.followUp":          {DefaultKeys: tui.PlatformKeys{Other: []KeyID{"alt+enter"}, Windows: []KeyID{"ctrl+q"}}.For(platform), Description: "Queue follow-up message"},
		"app.message.copy":              {DefaultKeys: nil, Description: "Copy selection or last assistant message"},
		"app.message.dequeue":           {DefaultKeys: tui.PlatformKeys{Other: []KeyID{"alt+up"}, Windows: []KeyID{"alt+q"}}.For(platform), Description: "Restore queued messages"},
		"app.clipboard.pasteImage":      {DefaultKeys: tui.PlatformKeys{Other: []KeyID{"ctrl+v"}, Windows: []KeyID{"alt+v"}}.For(platform), Description: "Paste image from clipboard (text fallback)"},
		"app.session.new":               {DefaultKeys: nil, Description: "Start a new session"},
		"app.session.tree":              {DefaultKeys: nil, Description: "Open session tree"},
		"app.session.fork":              {DefaultKeys: nil, Description: "Fork current session"},
		"app.session.resume":            {DefaultKeys: nil, Description: "Resume a session"},
		"app.tree.foldOrUp":             {DefaultKeys: tui.PlatformKeys{Other: []KeyID{"ctrl+left", "alt+left"}, Darwin: []KeyID{"alt+left", "ctrl+left"}}.For(platform), Description: "Fold tree branch or move up"},
		"app.tree.unfoldOrDown":         {DefaultKeys: tui.PlatformKeys{Other: []KeyID{"ctrl+right", "alt+right"}, Darwin: []KeyID{"alt+right", "ctrl+right"}}.For(platform), Description: "Unfold tree branch or move down"},
		"app.tree.editLabel":            {DefaultKeys: []KeyID{"shift+l"}, Description: "Edit tree label"},
		"app.tree.toggleLabelTimestamp": {DefaultKeys: []KeyID{"shift+t"}, Description: "Toggle tree label timestamps"},
		"app.tree.filter.default":       {DefaultKeys: []KeyID{"ctrl+d"}, Description: "Tree filter: default view"},
		"app.tree.filter.noTools":       {DefaultKeys: []KeyID{"ctrl+t"}, Description: "Tree filter: hide tool results"},
		"app.tree.filter.userOnly":      {DefaultKeys: []KeyID{"ctrl+u"}, Description: "Tree filter: user messages only"},
		"app.tree.filter.labeledOnly":   {DefaultKeys: []KeyID{"ctrl+l"}, Description: "Tree filter: labeled entries only"},
		"app.tree.filter.all":           {DefaultKeys: []KeyID{"ctrl+a"}, Description: "Tree filter: show all entries"},
		"app.tree.filter.cycleForward":  {DefaultKeys: []KeyID{"ctrl+o"}, Description: "Tree filter: cycle forward"},
		"app.tree.filter.cycleBackward": {DefaultKeys: []KeyID{"shift+ctrl+o"}, Description: "Tree filter: cycle backward"},
	}
}

var appKeybindingDefinitions = appKeybindingDefinitionsFor(tui.HostKeybindingPlatform())

// keybindingDefinitionsFor returns the keybinding table for one platform: the
// tui.* table with its per-platform defaults plus the app.* table. The TUI
// manager receives this whole table so components there resolve app actions.
func keybindingDefinitionsFor(platform tui.KeybindingPlatform) map[string]tui.TUIKeybindingDef {
	defs := tui.TUIKeybindingDefinitionsFor(platform)
	for id, def := range appKeybindingDefinitionsFor(platform) {
		defs[id] = tui.TUIKeybindingDef{DefaultKeys: def.DefaultKeys, Description: def.Description}
	}
	return defs
}

var appKeybindingOrder = []string{
	"app.commandPalette", "app.interrupt", "app.clear", "app.exit", "app.suspend", "app.thinking.cycle", "app.thinking.save",
	"app.model.cycleForward", "app.model.cycleBackward", "app.model.select", "app.tools.expand",
	"app.thinking.toggle", "app.editor.external", "app.message.copy",
	"app.message.followUp", "app.message.dequeue", "app.clipboard.pasteImage", "app.session.new", "app.session.tree",
	"app.session.fork", "app.session.resume", "app.tree.foldOrUp", "app.tree.unfoldOrDown",
	"app.tree.editLabel", "app.tree.toggleLabelTimestamp",
	"app.tree.filter.default", "app.tree.filter.noTools",
	"app.tree.filter.userOnly", "app.tree.filter.labeledOnly", "app.tree.filter.all",
	"app.tree.filter.cycleForward", "app.tree.filter.cycleBackward",
}

// keyIDDisplay returns human-readable display text for a key ID.
func keyIDDisplay(id KeyID) string {
	// Capitalize each segment: "ctrl+o" → "Ctrl+O", "alt+up" → "Alt+Up".
	parts := strings.Split(string(id), "+")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "+")
}

func KeybindingsFile(agentDir string) string {
	return filepath.Join(agentDir, "keybindings.json")
}

func NewKeybindingsManager(agentDir string) *KeybindingsManager {
	km := &KeybindingsManager{
		definitions:  appKeybindingDefinitions,
		ordered:      slices.Clone(appKeybindingOrder),
		userBindings: map[string][]KeyID{},
		resolved:     map[string][]KeyID{},
		configPath:   KeybindingsFile(agentDir),
		platform:     tui.HostKeybindingPlatform(),
	}
	km.rebuild()
	km.syncToTUI()
	tui.SetAppKeyTextResolver(km.KeyText)
	if agentDir != "" {
		_ = km.Reload()
	}
	return km
}

func DefaultKeybindingsManager() *KeybindingsManager {
	return NewKeybindingsManager("")
}

func normalizeKeys(keys []KeyID) []KeyID {
	seen := map[KeyID]struct{}{}
	out := make([]KeyID, 0, len(keys))
	for _, key := range keys {
		key = KeyID(strings.TrimSpace(strings.ToLower(string(key))))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func (km *KeybindingsManager) rebuild() {
	km.resolved = make(map[string][]KeyID, len(km.definitions))
	for _, action := range km.ordered {
		def := km.definitions[action]
		keys, ok := km.userBindings[action]
		if !ok {
			keys = def.DefaultKeys
		}
		km.resolved[action] = normalizeKeys(keys)
	}
	userBindings := make(map[string][]string, len(km.userBindings))
	for action, keys := range km.userBindings {
		userBindings[action] = slices.Clone(keys)
	}
	km.merged = tui.NewKeybindingsManager(keybindingDefinitionsFor(km.platform), userBindings)
}

func decodeKeybindingsConfig(raw map[string]any) map[string][]KeyID {
	config := map[string][]KeyID{}
	for key, value := range raw {
		switch v := value.(type) {
		case string:
			config[key] = normalizeKeys([]KeyID{KeyID(v)})
		case []any:
			var keys []KeyID
			for _, item := range v {
				if s, ok := item.(string); ok {
					keys = append(keys, KeyID(s))
				}
			}
			config[key] = normalizeKeys(keys)
		}
	}
	return config
}

func (km *KeybindingsManager) Reload() error {
	km.userBindings = map[string][]KeyID{}
	if km.configPath != "" {
		data, err := os.ReadFile(km.configPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			var raw map[string]any
			if err := json.Unmarshal(data, &raw); err != nil {
				return err
			}
			km.userBindings = decodeKeybindingsConfig(raw)
		}
	}
	km.rebuild()
	km.syncToTUI()
	return nil
}

// syncToTUI installs the merged keybinding table and the user overrides as
// the TUI registry, so components in the tui package
// resolve tui.* and app.* actions through one manager.
func (km *KeybindingsManager) syncToTUI() {
	tui.SetKeybindings(km.merged)
}

// MatchesEditorHistory reports whether input matches an explicit
// tui.editor.historyPrevious or historyNext binding. The focused editor gives
// those precedence over app actions, so a user
// can bind ctrl+p to history although it cycles models by default.
func (km *KeybindingsManager) MatchesEditorHistory(input string) bool {
	return km.merged.Matches(input, tui.KBEditorHistoryPrevious) || km.merged.Matches(input, tui.KBEditorHistoryNext)
}

func (km *KeybindingsManager) Get(action string) []KeyID {
	keys := km.resolved[action]
	out := make([]KeyID, len(keys))
	copy(out, keys)
	return out
}

// DisplayFor returns a human-readable key hint for the first binding of
// the given action (e.g. "Alt+Up"). Returns the raw KeyID if no
// display mapping exists.
func (km *KeybindingsManager) DisplayFor(action string) string {
	keys := km.resolved[action]
	if len(keys) == 0 {
		return action
	}
	return keyIDDisplay(keys[0])
}

// KeyText returns the un-capitalized display text for every key bound to action,
// joined by "/". Used for the startup keybinding hints.
func (km *KeybindingsManager) KeyText(action string) string {
	return tui.FormatKeyText(strings.Join(km.resolved[action], "/"), false)
}

func (km *KeybindingsManager) Matches(input, action string) bool {
	for _, key := range km.resolved[action] {
		// Every resolved KeyID goes through one matcher, which preserves
		// Kitty-mode ambiguity, alternate-layout, remapped-layout, and
		// terminal-protocol semantics for both defaults and user bindings.
		if tui.MatchesKeyID(input, string(key)) {
			return true
		}
	}
	return false
}

func (km *KeybindingsManager) Resolve(input string) string {
	for _, action := range km.ordered {
		if _, ok := km.userBindings[action]; !ok {
			continue
		}
		if km.Matches(input, action) {
			return action
		}
	}
	for _, action := range km.ordered {
		if _, ok := km.userBindings[action]; ok {
			continue
		}
		if km.Matches(input, action) {
			return action
		}
	}
	return ""
}

func (km *KeybindingsManager) HotkeyLines() []string {
	lines := make([]string, 0, len(km.ordered)+12)
	for _, action := range km.ordered {
		def := km.definitions[action]
		keys := km.Get(action)
		if len(keys) == 0 {
			continue
		}
		lines = append(lines, fmt.Sprintf("%-18s: %s", prettyKeys(keys), def.Description))
	}
	lines = append(lines,
		"Enter             : submit",
		"Shift+Enter       : insert newline",
		"Up/Down           : history navigation (empty editor)",
		"Ctrl+A            : move to start of line",
		"Ctrl+E            : move to end of line",
		"Alt+B             : word backward",
		"Alt+F             : word forward",
		"Alt+Bksp, Ctrl+W  : delete word backward",
		"Alt+D, Alt+Del    : delete word forward",
		"Ctrl+U            : kill to line start",
		"Ctrl+K            : kill to line end",
		"Ctrl+Y / Alt+Y    : yank / yank-pop",
		"Ctrl+/            : undo",
	)
	return lines
}

func prettyKeys(keys []KeyID) string {
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, prettyKey(key))
	}
	return strings.Join(parts, ", ")
}

var prettyKeyParts = map[string]string{
	"ctrl": "Ctrl", "shift": "Shift", "alt": "Alt", "escape": "Esc", "enter": "Enter",
	"backspace": "Bksp", "pageup": "PgUp", "pagedown": "PgDn",
}

func prettyKey(key KeyID) string {
	parts := strings.Split(string(key), "+")
	for i, part := range parts {
		if pretty, ok := prettyKeyParts[strings.ToLower(part)]; ok {
			parts[i] = pretty
		} else {
			parts[i] = strings.ToUpper(part[:1]) + part[1:]
		}
	}
	return strings.Join(parts, "+")
}
