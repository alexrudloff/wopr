# Spec: docs-drift-docs-json

Backlog item: `docs-drift-docs-json`. Worktree: `.worktrees/wave-docs-drift-docs-json`.

## Decision

**Take option A: correct the claim in `docs/site/docs/development.md:76`.** Do
not add a `docs.json` check to `tests/docs-drift`.

Why (changed from the first draft after the leanness spec-fan):
- The repo's own test rule, stated on this very page
  (`development.md:68`, mirrored in `AGENTS.md`), is "Do not add tests by
  default. Add one small behavioral test only when a regression would cost
  money, lose data, or weaken safety, or when you fix a real bug that is likely
  to recur." A stale `docs.json` entry costs none of those.
- `docs.json` is not shipped: `docs/site/docs/embed.go:11` is `//go:embed *.md`,
  and no Go/JS/TS/Makefile/workflow in the repo reads `docs.json`. Its consumer
  is an external site build, so a hand-maintained table with no in-repo reader
  is not a gate target.
- The real user-visible harm — a page nobody can find — is already covered by
  `TestEveryPageIsReachableFromTheIndex` (`docs_drift_test.go:133`): every page
  must be reachable from `index.md` by links.
- Adding the check would also stale the `Makefile:158-160` comment that
  enumerates what `docs-drift` checks, creating a second doc-drift to fix.

So: leave the gate as-is and make the sentence match what the gate actually
does.

## Current state (verified)

- `docs/site/docs/development.md:76` reads (one line):
  "User documentation is authored as Markdown under `docs/site/docs/` and read
  on GitHub; pages link each other with relative `.md` links.
  `docs/site/docs/docs.json` classifies and orders every page, and
  `make docs-drift` checks it. The installer is `install.sh` ..."
- `Makefile:161-162` — `docs-drift: ## Fail when generated documentation no
  longer matches its source` / `@go test ./tests/docs-drift/ -count=1`.
- The gate's four checks (docs_drift_test.go): slash commands documented exist;
  listable commands are documented; every page reachable from index; internal
  links resolve; retired names absent. None opens `docs.json`.
- `docs.json` does classify (`provenance`) and order (`navigation`) every page;
  that clause is true and stays.

## Files

- `docs/site/docs/development.md` — only file touched.

## Change

Delete the false clause "`, and `make docs-drift` checks it`" from line 76.
Resulting sentence: "`docs/site/docs/docs.json` classifies and orders every page."
Nothing else on the page changes.

## Definition of done (gates)

- `go build ./...` passes.
- `go vet ./...` passes.
- `go test ./tests/docs-drift/ -count=1` passes (unchanged tests still green).
- `go tool golangci-lint run` passes.
- `go fix -diff ./...` clean.
- `make check` stops at `test-prereqs` (tmux absent, `Makefile:110-111`) — run
  the rest directly.
- No red-proof needed: no gate behavior changes (no check added).

## Out of scope

- Adding any `docs.json` check to `tests/docs-drift`.
- Editing `docs.json`, `embed.go`, or the `Makefile` gate comment.
