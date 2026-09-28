// Package nativeplatform provides operating-system queries. wopr builds with
// CGO_ENABLED=0, so each query calls the system function directly.
package nativeplatform

// IsModifierPressed reports
// whether the named modifier ("shift", "command", "control", or "option") is
// held right now. It reports false when the platform has no helper, the system
// function cannot be loaded, or the name is unknown.
func IsModifierPressed(name string) bool {
	return isModifierPressed(name)
}
