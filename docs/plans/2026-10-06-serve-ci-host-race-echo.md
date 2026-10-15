# serve-ci-host-race-echo — correct the `host` race-gate echo

## Decision
**Option A — correct the prose.** The `host` echo overstates a supervisor↔bus
cross-race the tests never create; fix the wording, don't add a test: AGENTS.md bars
new tests absent a money/data/safety cost, and `docs-drift-docs-json` (BACKLOG.md:850-877)
took A on the same rule.

## Files touched
- `automation/ci/test-race.sh` (echo @37; comment @23-24)

## Exact change
- `:37` old: `echo "== race regressions (host: multi-session supervisor vs typed event bus) =="`
- `:37` new: `echo "== race regressions (host: concurrent session prompts + racing subscriber close) =="`
- `:23-24` old: `#   4. host: ./coding/host races the multi-session supervisor and its typed event` / `#      bus.`
- `:23-24` new: `#   4. host: ./coding/host races concurrent prompts against the per-session run` / `#      slot, plus the event bus's racing subscriber close.`

## Definition of done / Gates
`bash -n automation/ci/test-race.sh`; `bash automation/ci/test-race.sh` (or `make test-race`);
`go test ./tests/docs-drift/ -count=1`. Expected: syntax exits 0; script prints the new
`host:` echo and `test-race: race + leak regressions green.`; docs-drift passes. `make check`
may still stop at `test-prereqs` (tmux absent) — pre-existing, not this slice (BACKLOG.md:847-848, :876-877).

## Out of scope
Adding a Translator-installing host test (option B); any other line/echo in `test-race.sh`;
BACKLOG.md status flip (wave reconciles the tracker); `internal/codingagent/serve/serve.go`.

## In-scope flag: the :23-24 comment
The item's **Files** line names only "the `host` echo line, ~37". The :23-24 comment makes
the *same* overstated claim. Treat the comment edit as **IN SCOPE** on its merits: it is in
the same file, and correcting only the echo would leave the file self-contradictory. It is
not line-adjacent, though — :23-24 sits 13 lines above the echo at :37, in the block header
at :18-24. If strict line-scope is demanded, this comment is the only defensible extension.

## Open questions
None.

---

## Evidence (why the new echo is true)

`go test -race -count=1 ./coding/host` runs four tests (`coding/host/host_test.go`;
`BenchmarkServeFanout` runs only under `-bench`, so this gate does not race it):

- `TestHostRejectsASecondConcurrentPromptPerSession` (:64) — goroutines call
  `Session.Prompt` at :138, :152, :166. The session-A retry at :152 returns `ErrSessionBusy`
  before the session-B prompt at :166 launches, so those two never overlap; only the first
  (A, blocking) and third (B) run concurrently. The contended pair is the per-session run
  slot: `s.run.TryLock()` / `Unlock` in `host.go:217-220`. Prompts on sessions A and B also
  run `Send` and their `forwardEvents` goroutines (`host.go:122`, :153) concurrently. The
  registry mutex (`host.go:45`) is only touched sequentially here (`h.Start` ×2).
- `TestSubscriptionCloseIdempotent` (:191) — the first `Close()` (:196) already ran
  `closeOnce.Do`'s body, so the ten concurrent `Close()` goroutines (:201-206) contend only
  on `sync.Once`'s internal state (`bus.go:117`) and `doneCh`; `Bus.mu` (`bus.go:27`) is
  locked once, in the main goroutine at `bus.go:257`. The `Publish` after is a post-close
  panic check, not concurrent.
- `TestDrainedBurstDoesNotFakeADeadline` (:220) and `TestServeFanoutDropsStalledSubscribers`
  (:264) — single-goroutine; no concurrency.

**No `coding/host` test installs a `Translator`.** `Host.Translate` is assigned only once
in the tree, at `internal/codingagent/serve/serve.go:44` (grep for `Translate\s*=` /
`Translate:` matches that line only; `host.go:51` is the field declaration `Translate
TranslatorFactory`, which matches neither pattern). So the HOST gate (`test-race.sh:38`)
runs with `Translate == nil`: `forwardEvents` (`host.go:154-164`) takes the `translate ==
nil` branch and `continue`s on every event, never calling `h.Publish` / `Bus.Publish`. The
promised supervisor↔bus cross-race therefore does not exist in this package's tests. The
bus/fan-out path is not untested overall, though: `internal/codingagent/serve/serve_test.go:168`
calls `serve.New`, which executes the `serve.go:44` assignment, then drives prompts, and that
package runs under the serve gate (`test-race.sh:35`). The serve gate therefore already
exercises `forwardEvents` with a non-nil translator publishing to the Bus, making Option B
still weaker than BACKLOG.md:819 suggests. Option A stands: the corrected echo is about the
host gate specifically, and the item is cosmetic. The old echo's "vs typed event bus" is the
overstatement; the run slot and the racing `Close` are what the host tests actually race.
