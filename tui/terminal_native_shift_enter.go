package tui

import (
	"os"
	"runtime"
)

// nativeShiftEnterSequence is the enhanced (CSI u) Shift+Enter sequence.
const nativeShiftEnterSequence = "\x1b[13;2u"

func isAppleTerminalSessionFor(goos, termProgram string) bool {
	return goos == "darwin" && termProgram == "Apple_Terminal"
}

// NormalizeNativeShiftEnterInput turns a plain Return read while the native
// Shift key is held into the enhanced Shift+Enter sequence.
func NormalizeNativeShiftEnterInput(data string, shouldDetectNativeShiftEnter, isShiftPressed bool) string {
	if shouldDetectNativeShiftEnter && data == "\r" && isShiftPressed {
		return nativeShiftEnterSequence
	}
	return data
}

// NormalizeProcessInputSequence normalizes one complete StdinBuffer sequence.
// Apple Terminal (and the Windows console) can send plain Return for
// Shift+Enter even with enhanced key reporting requested, so it asks the local
// OS whether Shift is held. Each input loop calls this before dispatch.
func NormalizeProcessInputSequence(sequence string) string {
	return normalizeProcessInputSequenceFor(sequence, runtime.GOOS, os.Getenv("TERM_PROGRAM"), IsNativeModifierPressed)
}

func normalizeProcessInputSequenceFor(sequence, goos, termProgram string, isModifierPressed func(ModifierKey) bool) string {
	shouldDetectNativeShiftEnter := sequence == "\r" && (isAppleTerminalSessionFor(goos, termProgram) || goos == "windows")
	return NormalizeNativeShiftEnterInput(
		sequence,
		shouldDetectNativeShiftEnter,
		shouldDetectNativeShiftEnter && isModifierPressed(ModifierShift),
	)
}
