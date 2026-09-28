# Evals

`wopr-eval` measures wopr: how much time and memory wopr itself costs, and how often it finishes real coding tasks for how many tokens and dollars under each routing mode. It is Go (`internal/evals`, command line in `cmd/wopr-eval`), is not part of the shipped binary, and runs through `make`:

| Target | What it does |
|---|---|
| `make evals` | Overhead against a mock model: startup, one prompt round trip over each wire protocol, and a resumed 2,000-exchange Session. `RUNS=10`. Writes `tmp/evals/overhead.json` and `.md`. |
| `make perf-check` | Applies `budgets.toml` to the last overhead run (`EVAL_RESULTS`). |
| `make evals-live` | Runs `tasks/` with real models and judges each result. `MODE=auto,speed,quality` and/or `MODEL=provider/model,...` give one configuration each; `LIVE_RUNS=3`, `LIVE_PARALLEL=1` (runs at once), `TASKS=all`. Writes `tmp/evals/live.json` and `.md`. |
| `make evals-mutate` | Generates seeded bug-fix tasks from wopr's own Go source (`MUTATIONS=30 SEED=1`) into `tmp/evals/mutation-tasks`; run them with `make evals-live EVAL_TASKS_DIR=tmp/evals/mutation-tasks`. |
| `make evals-publish` | Copies a reviewed overhead run (and `LIVE_RESULTS`, if set) to `results/` and regenerates `docs/site/docs/evals.md`. |
| `make profile`, `make pgo` | Profiles round trips against the mock with `WOPR_PROFILE`; `pgo` merges CPU profiles for a profile-guided build. |
| `make bench`, `make bench-compare` | Go benchmarks and a median comparison against a saved base (`make bench-base`). |
| `make evals-test` | Unit tests for the eval harness. |

## Method

**Overhead.** The mock model answers every request with one fixed reply over OpenAI Chat Completions, OpenAI Responses, or Anthropic Messages. `wopr-eval` writes a `models.json` with one mock provider per protocol, so each round trip exercises wopr's own client for that protocol. Replies take no model time, so what remains is wopr: process startup, request construction, streaming, Session I/O, and teardown. Each run has an isolated `HOME` and `WOPR_HOME`, no credentials in its environment, and routing off. The report shows the median, p90, CPU time, peak RSS, and the size of the first request, its system prompt, and its tool count. A prompt run counts only if it exits 0, prints the reply, and spoke only the scenario's protocol.

**Budgets.** `budgets.toml` holds ceilings per scenario (`[overhead.<scenario>]`, any numeric field of the result). They catch regressions, not noise.

**Live tasks.** Each task in `tasks/` is a small repository, a prompt, a check, and a list of protected files. A run passes when the check succeeds and no protected file changed. A run cut off at its time limit is still judged on what it left in the repository, and the report counts those passes separately. Every task's check fails before the fix; the unit tests assert that. Each run starts `wopr --mode json` in a fresh copy of the task with your own wopr configuration (credentials, `router.json`, `efficiency.json`), with its session stored beside the copy so the mechanisms that need a persisted session run as they do interactively. A routing mode sets `WOPR_ROUTER=<mode>`; a pinned model passes `--model` with routing off.

**Live metrics.** From wopr's JSON events, each run records the models that answered (turns per model), input tokens (including cache reads and writes), output tokens, provider-reported cost, turns, tool calls, polling calls, and wall time. The report gives one row per configuration: pass rate with its 95% Wilson interval, passes at the time limit, total cost, cost per passing task, input, cached, and output tokens per task, turns and tool calls per task, median and total wall time, and the models used, so routing modes and pinned models compare on accuracy per dollar. Free and subscription models report $0. `slow-build` makes waiting behavior visible: a polling call is a shell command that only waits on or inspects a running job.

**Mutation tasks.** `make evals-mutate` takes a real Go file from wopr (`agent`, `ai`, `tui`, `coding`), applies one mechanical bug with a known inverse (inverted comparison, flipped boolean, off-by-one bound, swapped logical operator, removed guard clause), and describes it in plain English. A run passes when the file's gofmt output hashes to the original's (`gofmt_sha256` in `task.toml`), so any fix that restores the code counts. Generation is seeded and reproducible, and a task is kept only after it fails as mutated and passes restored.

## Adding a task

Create `tasks/<id>/task.toml` with `prompt`, `check` (a shell command run in the repository; exit 0 passes) or `gofmt_sha256`, `protected`, and optionally `timeout` in seconds (default 600). Put the starting repository in `tasks/<id>/files/`. Keep tasks small, deterministic to check, and failing before the fix. Unknown keys are rejected.

## Reading results

Compare numbers from one run on one machine only. Overhead numbers say nothing about model quality, and a live pass rate from a few runs has a wide error; raise `LIVE_RUNS` before drawing conclusions.
