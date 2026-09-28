package tui

import "github.com/alexrudloff/wopr/internal/nativeplatform"

// ModifierKey names a keyboard modifier for IsNativeModifierPressed.
type ModifierKey string

const (
	ModifierShift   ModifierKey = "shift"
	ModifierCommand ModifierKey = "command"
	ModifierControl ModifierKey = "control"
	ModifierOption  ModifierKey = "option"
)

// IsNativeModifierPressed asks the operating system whether a modifier key
// is held right now, and reports false when no helper is available.
func IsNativeModifierPressed(key ModifierKey) bool {
	return nativeplatform.IsModifierPressed(string(key))
}
