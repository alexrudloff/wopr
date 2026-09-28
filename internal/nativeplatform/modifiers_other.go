//go:build !darwin && !windows

package nativeplatform

// isModifierPressed always reports false on platforms other than macOS and
// Windows.
func isModifierPressed(string) bool { return false }
