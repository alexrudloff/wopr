//go:build integration

package integration

import (
	"time"
)

// pollInterval is the default sub-second cadence for poll loops in this
// package. 200ms is fast enough that LLM-driven assertions don't waste
// >0.2s of slack and slow enough that we don't hammer tmux.
const pollInterval = 200 * time.Millisecond

// pollUntil runs cond every pollInterval until it returns true or
// timeout elapses. Returns true if cond ever returned true.
//
// Use this in place of fixed time.Sleep("worst case") waits. The whole
// suite gets faster on the happy path while keeping the slow-LLM
// pessimistic ceiling intact.
func pollUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(pollInterval)
	}
	return cond()
}
