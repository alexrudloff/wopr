#!/usr/bin/env bash
# Race regression gate for the interactive TUI, serve, and host concurrency models, plus a TUI-only goroutine-leak profile.
#
# The interactive TUI runs one main loop that owns all component-tree mutation
# and rendering. Background goroutines must route UI work onto that loop
# (runOnMain for must-not-lose state, postUITask for cosmetic updates) instead
# of touching the tree from their own goroutine. These tests lock that
# invariant in so a future off-loop UI touch fails the build rather than
# shipping a latent data race.
#
#   1. -race regressions: the runOnMain delivery/shutdown proofs (testing/synctest)
#      and the StatusLine + SIGWINCH no-race regressions. TestConcurrent_SIGWINCHVsTyping
#      is intentionally excluded: it is the red oracle that documents the raw
#      off-loop pattern still races, gated behind WOPR_PROBE_RENDER_RACE=1.
#   2. goroutineleak: Go 1.27's profile asserts runOnMain leaves zero
#      goroutines blocked on uiTaskCh after its workload.
#
# The serve and host models are raced as whole packages rather than through one
# main loop:
#
#   3. serve: ./internal/codingagent/serve races HTTP handlers against the tool
#      goroutines those handlers dispatch.
#   4. host: ./coding/host races concurrent prompts against the per-session run
#      slot, plus the event bus's racing subscriber close.
set -euo pipefail

pkg=./internal/codingagent

echo "== race regressions (runOnMain + off-loop UI routing) =="
go test -race -count=1 -run \
  'TestRunOnMain_DeterministicNoLeak|TestRunOnMain_DropsOnShutdown|TestConcurrent_StatusLineInvalidate_NoRace|TestConcurrent_SIGWINCHViaPostUITask_NoRace' \
  "$pkg"

echo "== race regressions (serve: HTTP handlers vs tool goroutines) =="
go test -race -count=1 ./internal/codingagent/serve

echo "== race regressions (host: concurrent session prompts + racing subscriber close) =="
go test -race -count=1 ./coding/host

echo "== goroutineleak profile =="
leak_output="$(go test -count=1 -v -run '^TestRunOnMain_NoGoroutineLeakProfile$' "$pkg")"
printf '%s\n' "$leak_output"
grep -F -- '--- PASS: TestRunOnMain_NoGoroutineLeakProfile' <<<"$leak_output" >/dev/null

echo "test-race: race + leak regressions green."
