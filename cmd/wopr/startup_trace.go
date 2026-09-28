// Startup timing instrumentation for wopr.
//
// Activated by setting WOPR_STARTUP_TRACE=1. Prints structured timing
// to stderr showing wall-clock elapsed from process start for each
// startup. Uses Go's monotonic clock via time.Since.
//
// Usage:
//
//	WOPR_STARTUP_TRACE=1 wopr --model github-copilot/gpt-5-mini
//
// Output:
//
//	[startup] flags-parsed          0ms
//	[startup] services-init         2ms
//	[startup] model-resolved        3ms
//	[startup] session-created      28ms
//	[startup] interactive-ready   130ms
//
// This is the official profiling mechanism. Do NOT add ad-hoc
// fmt.Fprintf timing to main.go or interactive.go: use this.
package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// startupTrace records from process start.
// Zero-cost when disabled (all methods are no-ops).
type startupTrace struct {
	enabled bool
	t0      time.Time
	mu      sync.Mutex
}

var trace = startupTrace{
	enabled: os.Getenv("WOPR_STARTUP_TRACE") != "",
	t0:      time.Now(), // captures process init time
}

// Mark records a named checkpoint. No-op when tracing is disabled.
func (s *startupTrace) Mark(label string) {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(os.Stderr, "[startup] %-28s %dms\n", label, time.Since(s.t0).Milliseconds())
}
