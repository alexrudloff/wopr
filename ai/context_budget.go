package ai

// Context budget: one rule for how full a model's window may get, shared by
// routing (which models a conversation fits) and compaction (when a
// conversation must shrink), so a routed model never starts out needing a
// compaction.

// DefaultContextWindow is the window assumed for a model that states none.
// Guessing low costs an early compaction; guessing high costs failed requests.
const DefaultContextWindow = 32768

// maxContextReserve caps the reply headroom on large windows.
const maxContextReserve = 16384

// ContextReserve is the headroom a model's window keeps free for its reply:
// the model's max output (16384 when unknown), at most 16384 and at most a
// quarter of the window. A window of 0 counts as DefaultContextWindow.
func ContextReserve(window, maxOutput int) int {
	if window <= 0 {
		window = DefaultContextWindow
	}
	reserve := maxContextReserve
	if maxOutput > 0 {
		reserve = min(reserve, maxOutput)
	}
	return min(reserve, window/4)
}

// UsableContext is how many conversation tokens a model takes before it
// must compact: its window less ContextReserve.
func UsableContext(window, maxOutput int) int {
	if window <= 0 {
		window = DefaultContextWindow
	}
	return window - ContextReserve(window, maxOutput)
}
