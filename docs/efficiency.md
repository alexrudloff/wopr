# Efficiency mechanisms

The harness implements four mechanisms that cut the tokens each request
carries, in `internal/codingagent/efficiency`, plus the harness additions
below. Every mechanism is on by default; `~/.wopr/agent/efficiency.json` can
turn any of them off. The four:

```json
{
  "version": 1,
  "actionFusion": true,
  "observationPack": true,
  "evidencePreservingReducer": true,
  "onlineContextCompact": true,
  "cacheWriteReadRatio": 12.5
}
```

Unknown keys are an error, so a typo never silently disables a mechanism.
`/router status` lists which mechanisms are on and where archives live.
Each saving appears as a `⚡` line in the TUI and a `savings` event in JSON and
RPC output.

## Action Fusion

`edit` and `write` accept an optional `then_run: {command, timeout}`. After a
successful mutation the tool hashes the file, runs the command through the
bash tool, and returns one combined result marked `[then_run:succeeded]`,
`[then_run:failed]`, or `[then_run:skipped]`. A failed mutation skips the
command. A failed command keeps the mutation. This removes one model round
trip per edit-then-test pair.

## ObservationPack

A non-error text tool result over 10 KB is sent in full for its first two
provider requests, then replaced in the request (never in the stored session)
by a stable placeholder with its first and last lines and an id. The
`obs_recall` tool pages the archived original back exactly. Archives live at
`<session dir>/harness/<session id>/observation-pack/`.

## Evidence-Preserving Reducer

When a build or test command's output is over 4 KB, the log is archived and a
cheap model is asked for a JSON receipt with at most 12 quotes. The receipt
replaces the log only if every quote is a byte-exact substring of the archive,
the status matches the exit code, and a failing log carries failure evidence.
Otherwise the original output passes through untouched, and the journal at
`evidence-preserving-reducer/journal.jsonl` records why.

The reducer model is the router's cheapest tier whose window fits, with
thinking off, unless `evidencePreservingReducerProvider` and
`evidencePreservingReducerModel` pin one. With routing off (`/router off`) and
no pin, the reducer is inactive rather than billing the main model.

A bare `FAIL` counts as a failure signal (Go test output), and prose around
the receipt object is tolerated. Neither weakens verification.

## Online Context Compact

The work queue the model keeps with `update_plan` is the plan. Completing an item is a boundary.
At a boundary the harness prices a compaction: it compacts when the context is
within 16K tokens of the window, or when the prompt-cache write cost
(`cacheWriteReadRatio`) pays back over the expected remaining requests. When it
compacts, the run ends at the boundary, the branch is summarized by the
cheapest model that fits, and a hidden reminder starts a new turn that brings
the queue up to date. The request and compaction history is persisted as
`harness-online-context-state-v1` custom session entries. A steer or a prompt
starting with `CORRECTION:` resets that history.

For local models the real cost of a cache write is prefill time rather than
money, so `cacheWriteReadRatio` is best treated as a tuning knob there.

## Subagents

The `task` tool applies the reducer's contract to delegation: a cheap model
does the reading, and the orchestrator never has to trust a fluent summary.
Delegation pays only where a cheaper or faster model or parallelism does;
ObservationPack and Online Context Compact already keep the orchestrator's
context small, and Action Fusion keeps small edits cheaper inline.

- **Explore type.** A brief goes to a child with a fresh context, a short
  byte-stable system prompt (about 260 tokens, so sibling tasks share a
  provider prefix cache), and read-only tools: `read`, `grep`, `find`, `ls`,
  `obs_recall`, and `bash` limited to read-only pipelines (`rg`, `grep`, `git
  log/show/diff/blame/status/ls-files/grep`, `go doc/list`, `cat`, `head`,
  `tail`, `wc`, `ls`, `find`, `sed -n 'N,Mp'`; chaining, redirection,
  substitution, and write flags are rejected). Children never get `task`,
  `edit`, `write`, or `update_plan`, and their requests pass through
  ObservationPack.
- **Budgets.** Effort `quick`, `medium`, `thorough` allows 8, 20, or 40
  turns; 60, 180, or 400 seconds; and 150K, 400K, or 1M cumulative input
  tokens. At 80% of any budget the child is told to wrap up; when one runs out
  every tool call is refused and it gets two grace turns to answer.
- **Result contract.** The child ends with `STATUS`, `CONFIDENCE`, `ANSWER`,
  `EVIDENCE` (at most 12 quotes, `path:line "exact line"` or `cmd: <command>
  "exact output line"`), and `NOT_CHECKED`. wopr verifies each quote as a
  byte-exact substring of the file within 50 lines of the cited range, or of
  one of the child's own tool outputs, and marks it ✓ or ✗. The result is
  capped near 1.5K tokens; a longer one is archived whole for `obs_recall`.
  The child's full transcript is archived too.
- **Footer.** wopr appends what the model cannot write: model and tier, route
  source, tool calls, turns, duration, tokens, cost, verified quotes, the
  transcript's `obs_recall` id, and any escalation.
- **Escalation** on objective signals, once, one step up; see
  [routing](routing.md#escalation).

Orchestrator cost: the tool definition and its two "when to delegate"
guidelines add about 340 tokens to every request (a cached prefix), and only
while the tool is active.

## Harness additions

More keys in `efficiency.json`, each on by default:

- `toolOutputHalfLife`: for a model whose context window is 128K or less,
  text tool results older than the newest 4 (window up to 64K) or 6 are cut
  to a 1 KB head-and-tail excerpt in the request; `obs_recall` pages the
  archived original back. The cut advances in steps of that count, so the
  cached prefix changes only every few requests. Needs `observationPack`.
- `stallNudge`: after the same tool call three times with no file change in
  between, or ten turns and three minutes without progress, the model gets
  one message telling it to change approach or finish (at most three per
  prompt). Progress is a file change, including one a shell command made to a
  file the model read or wrote, or a web search or fetch it hasn't made
  before in the run.
- `testRerunCap`: a test command (`go test`, `pytest`, `npm test`, `cargo
  test`, `make test`, and the like) that already passed is not run again
  while no file has changed since; the call is answered with a note instead.
  An `edit`, `write`, or any shell command that is not read-only counts as a
  change.
- `applyPatch`: requests served by an OpenAI GPT or Codex model declare
  `apply_patch` (the Codex patch format: `*** Begin Patch`, Add/Delete/Update
  File sections, `@@` hunks) in place of `edit`; other models keep `edit`,
  with hashline anchors where those are on. Context lines match exactly,
  then ignoring trailing whitespace, then ignoring surrounding whitespace, and
  every file is checked before any is written.
- `quotaBalance`: with routing on, when the plan behind the router's top
  pick is getting tight, a model on another subscription plan within one
  rank step of it takes the work instead, if its plan has at least 10 points
  more allowance left. A plan is tight when a usage window has under 30%
  left, or its use runs more than 15 points ahead of the time elapsed in the
  window. The router reads the windows Claude and ChatGPT subscriptions
  report on every response; a plan that hasn't reported yet is not tight.
  Balancing happens when the router picks afresh (a new session, a
  compaction, a mode change) and for every subagent, except that a plan
  under 10% moves a warm orchestrator at the next turn. The route reason
  says so: `quota: Claude 5h 82% used → ChatGPT wk 41% used`. A pinned tier
  is left alone.
- `learn`: every model starts from the numbers above and is tuned from real
  sessions, in small bounded steps, once a knob has enough events since it
  last moved (8, or 5 compactions). Values are per provider/model in
  `~/.wopr/agent/efficiency-learned.json`; delete the file to reset every
  model. `/harness` lists the values that differ from the defaults.

  | Signal | Knob | Step | Range |
  |---|---|---|---|
  | Placeholders recalled with `obs_recall` within 6 requests: over 40% / under 10% | ObservationPack threshold and excerpt | ×1.25 / ×0.8 | 4–32 KB, 512 B–4 KB |
  | Half-life cuts recalled: over 30% / under 5% | results kept whole | +1 / −1 | 2–12 |
  | Receipts whose raw log the model reads back: over 30% / under 5% | smallest log reduced | ×1.5 / ×0.75 | 2–64 KB |
  | A reducer model's receipts failing verification over 50% | that model is skipped as reducer, tried every 10th time | | |
  | Files read before a compaction read again within 6 requests: over 50% / under 20% of compactions | window share (compaction point and routing fit) | +5 / −5 points | 50–100% of the window |
  | Stall nudges at least twice as frequent from a tenth of the window up (20+ requests, 3+ stalls there) | window share | down to that tenth | 50–100% |

  The window share never exceeds the window you set; routing checks fit
  and compaction triggers against the learned share of it.

## Minimal-code discipline

Always on. The default system prompt carries a short `discipline` section (in `internal/codingagent/prompts`): a ladder the model climbs before writing code (does it need to exist, is it already in the codebase, stdlib, platform feature, installed dependency, one line, then the minimum), root-cause fixes where all callers route through, no unrequested abstractions or scaffolding, deletion over addition, and a floor it never cuts (input validation, error handling, security, accessibility, and the one test that proves non-trivial logic). Deliberate simplifications are marked with `simplify:` comments.

- `/review` reviews the current changes: correctness bugs first, then over-engineering and simplification opportunities.
- `/audit` audits the whole repository for over-engineering.
- `/debt` lists every `simplify:` comment as a debt ledger.

All three are report-only prompts. A user or project prompt template with the same name overrides them.
