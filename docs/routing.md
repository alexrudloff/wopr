# Model routing

Model routing is off unless you turn it on. Off, wopr is a plain harness:
you pick the model with `/model` or Tab (both offer the models you set up in
`/setup`, in setup's order),
and subagents and side tasks run on it. There are no modes, and the prompt
panel and sidebar show no routing.

Turn it on in `/setup` → Model routing, which offers **Off**, **Basic**, and
**Jev**:

- **Basic** needs nothing else: fixed rules pick a model from your order and
  each model's attributes (see [Basic rules](#basic-rules)).
- **Jev** classifies each prompt and subagent brief with a Jev endpoint.
  Save asks Jev to classify a sample prompt and keeps the choice only if it
  answers. Whenever Jev can't answer mid-session, the Basic rules route
  those turns, one notice says so, and Jev routes again once it answers.

While a mode routes, a request no eligible model can take fails with the
reason (for example, which models are too small or unreachable); it never
falls back to a model the mode didn't choose.

With routing on, wopr routes each prompt, and each subagent task, to a model chosen for the
active mode: auto balances capability, speed, and cost; speed, quality, cost,
and uncensored each optimize for one goal. The candidates span local models,
subscriptions, and API providers, with pay-per-token a last resort in every
mode. A failing model falls through to the next candidate
without a retry delay.

## Three states

| State | Orchestrator (the main conversation) | Subagents (`task`) and side tasks |
|---|---|---|
| **Auto** | The router picks the model (and, with Jev, the thinking level); sticky while the cache is warm | Their own choice |
| **Pinned** | Your model and your thinking level | Their own choice |
| **Off** | Your model | Run on the orchestrator's model |

Choosing a mode for the orchestrator in `/model`, or Tab to a mode on an
empty prompt, lets the router pick the orchestrator. Picking a model (`/model`,
F2, Tab to a model, or `wopr --model` at startup) pins the orchestrator.
`/router off` turns both halves off for the session, and `/router on`
returns both to auto. The prompt panel shows the orchestrator's mode or
model, and the subagents' choice after it when that differs; the sidebar's
Routing section shows the same.

## Subagents: their own choice

`/model` (and `ctrl+x m`) opens a small menu with two rows, **Orchestrator**
and **Subagents**, each showing its current choice; each opens its own
picker; the latest choice wins.

- Orchestrator: with routing on, a mode (the router picks the orchestrator
  under it) or one of your configured models; with routing off, a model.
  Tab on an empty prompt cycles this choice; a mode picked here routes the
  subagents too.
- Subagents: with routing on, a mode (every brief is routed under it) or a
  configured model; with routing off, "Same as orchestrator" or a
  configured model. A chosen model runs every subagent and side task; Same
  as orchestrator uses the orchestrator's current model. The default is
  auto with routing on and Same as orchestrator with it off.

Both choices are saved (`routing` and `subagents` in settings) and restored
at the next start, and recorded in the session and restored on resume. The
prompt line and the sidebar name the subagents' choice only when it differs
from the orchestrator's, for example `Claude Opus 5.5 · subagents auto`.
`/model <name>` sets the orchestrator directly.

Side tasks (compaction summaries and the log reducer) follow the subagent
half: routed to the cheapest model that fits unless routing is off.

## Basic rules

The Basic rules are fixed; nothing about them is configurable. They use only
what you set up about each model (its place in your order, its window, its
cost class, the abliterated flag) and what wopr measured (time to first token,
decode speed, and tool calling: a model that can't call tools is never
routed). They never name a model.

A model is **eligible** when it is reachable, not resting after errors or a
quota limit, allowed by the mode, and the conversation plus room for the reply
fits its window (the window you set, if you set one). Among eligible models:

| Mode | Picks |
|---|---|
| **auto**, **quality** | Your highest-ranked model |
| **speed** | The least expected time to a reply: time to first token at the current context size plus a 500-token reply at the measured decode speed. Unmeasured models count as slow. |
| **cost** | Local, then free remote, then subscription; your highest-ranked model within a class. Never pay-per-token. |
| **uncensored** | Your highest-ranked model marked abliterated |
| **private** | Your highest-ranked model on a private connection |

Pay-per-token models come after every other eligible model, and only with
`paidLastResort`. The orchestrator stays on its model while that model is
eligible (its cache is warm); a new session, a compaction, or a mode change
picks afresh, and a failure moves to the next model in the order. Subagents
follow the same rules under their mode, sized by the context the child is
expected to reach, and escalate to the next stronger eligible model; a brief
the secret scanner flags stays on owned hardware. Side tasks take the cheapest
cost class, then your highest-ranked model in it. Thinking is the model's own
effort setting, else your thinking level: no automatic boosts. Strength tags
are not used: telling what a prompt is about is Jev's job.

## Modes

A mode is the objective the router applies to whatever is not pinned: the
orchestrator in auto, and subagents in auto and pinned. It lasts for the
session, `/router off` resets it to auto, and it is recorded in route reasons
and in `task-log.jsonl`. The table is how Jev routing applies each mode; the
Basic rules apply them as in [Basic rules](#basic-rules).

| Mode | Picks | Thinking |
|---|---|---|
| **auto** | The cheapest model likely to suffice (everything below). | as decided |
| **cost** | The fewest marginal dollars, then the least subscription quota, then time. Free-local and free-remote models serve unless none can: all down or resting, the context does not fit, no free subagent slot, or the need clearly exceeds them. The bias is flipped from auto: a brief steps up a difficulty level only when P(harder) ≥ 0.7 and takes a cheaper one at P(level or easier) ≥ 0.3; the orchestrator ignores kind floors and steps up only when its demand exceeds a free model's capability by more than slack + 0.15. Among subscription models, the plan with the most allowance left in its tightest usage window (from the response headers the sidebar's Usage page shows; unreported counts as half) comes first, then the least capability that suffices. Pay-per-token is never used, by the orchestrator, subagents, escalation, failover, or side tasks: if nothing free or subscription can serve, the request fails, and your own model serves only if it is listed in a free or subscription tier. Jev still classifies. | one step lower |
| **speed** | The least expected time to first token among models that meet the need (the prompt's demand, or the brief's level), so a fast model that would fail and escalate is never chosen. Cost is ignored short of pay-per-token. An unmeasured model counts as a second slower than the slowest measured candidate (at least 4 s + 0.5 s per 1K tokens). | one step lower |
| **quality** | Any reachable model within 0.05 capability of the strongest reachable one, fastest first; need is ignored. Reachable means configured and signed in, fitting the context, not resting after a rate limit or failure, and (for subagents) with a free slot. A strong local model qualifies, and the band slides down when a subscription is limited. Pay-per-token only when nothing else is reachable. | one step higher (up to xhigh) |
| **uncensored** | Only models flagged `"uncensored": true` (marked abliterated per model in `/setup` → Model routing), for the orchestrator, every subagent, escalation, failover, and side tasks. Jev classifies as in auto. If no flagged model is reachable the request fails; nothing falls back to an unflagged model. Offered only when a configured model is flagged. | as decided |
| **private** | Only models on private connections (`"privateProviders"` in router.json, set with **Private endpoint** on a connection's screen in `/setup`), for the orchestrator, every subagent, escalation, failover, compaction and branch summaries, and the log reducer; highest-ranked first. Jev is asked only when `jev.privacySafe` is set; otherwise the Basic rules route. If no model on a private connection can take a request it fails with the reason; nothing falls back to another model. `web_search` and `web_fetch` still work, and their results say the request left your machine. MCP servers are not restricted. Offered only when a connection is private. | as decided |

A mode applies to the orchestrator or to the subagents, whichever it was
chosen for in `/model`. Tab cycles the orchestrator's choice: auto, cost,
speed, quality, (uncensored), (private), then your configured models (in routing order
within each connection), then back to auto. A mode picked for the orchestrator (Tab, the Orchestrator picker, or
`/model <mode>`) is the subagents' mode too. A model picked for the
orchestrator changes only the orchestrator; the subagents keep their
choice. `/model` → Subagents splits them, and that choice stays until a
mode is next picked for the orchestrator.

## The orchestrator in auto (Jev)

A switch of the orchestrator's model re-prefills the whole conversation on a
cold cache and a handoff between models recovers only part of the quality
gap, so the orchestrator is sticky:

1. The first turn of a session asks Jev about the prompt and picks freely.
2. While the cache is warm, a turn may only move **up** to a more capable
   model (the ratchet). Thinking may rise, never fall. When no usable model is
   stronger than the current one, the turn keeps it without asking Jev.
3. The next turn decides freely again when the cache is cold: after
   `stickyIdleMinutes` (default 10) without a request, after a compaction,
   after a mode or `/router pin` change, in a new session, or when the model
   is resting or no longer fits the context.
4. If Jev fails, a warm model stays; otherwise the Basic rules pick.

How a free decision is made:

1. Jev answers five questions about the newest prompt: task kind, complexity,
   capability needed, deep reasoning, and sensitive data. Before anything is
   sent, a local secret scanner redacts credentials; a hit marks the prompt
   sensitive.
2. Demand is `0.5·complexity + 0.4·capability + 0.15·deepReasoning`. A task
   kind can raise it with `minCapability` (plan and review default to 0.85).
   A sensitive prompt drops the kind floor so it can stay on owned hardware.
3. A model is **adequate** when its `capability` plus `slack` covers the
   demand. A fast model that is not capable enough never beats a slow one
   that is.
4. Among adequate models the router prefers the cheapest cost class (owned
   hardware, then subscription, then paid; paid only for kinds listed in
   `paidKinds`), then the **fastest expected time to first token**, then the
   strongest.
5. If no model is adequate, the router first drops the kind floor and retries
   with unpaid models. Only then does it take the most capable model that
   fits, paid only when `paidLastResort` is true.
6. Thinking comes from demand (off, low, medium, high), plus the kind's
   `thinkingBoost`, capped by the model, the tier's `maxThinking`, and the
   candidate's own `thinking`.

**Context windows.** A model fits a conversation up to its window less a
reply reserve (its max output, at most 16384 and at most a quarter of the
window): the same point where compaction starts on that model, so a routed
model never begins by compacting. The window is the model's as configured,
which may be a limit you set on purpose, such as a small window for a local
model whose prefill is slow; routing never exceeds it. A tier's
`contextShare` below 1 caps it further.

**Compact to fit.** At a new prompt, when the mode's preferred model has
been outgrown by the conversation, a free decision weighs staying whole on
the best model that still fits (warm cache, full detail) against compacting
to fit the preferred one (one summary call, a cold prefill of the smaller
prompt, some detail lost). It compacts only when the preferred model is
clearly better for the mode: cheaper cost class in cost mode, at least 0.1
stronger in quality mode, at least twice as fast to first token (measured)
in speed mode, and either of the first and last in auto. When no model fits
whole, it compacts for the preferred one. It never compacts mid-run, nor for
the same model again within 10 minutes. The route reason says so, e.g.
`compacted to fit local/qwen-coder (32K window)`; if the
compaction fails, the model that fits whole takes the turn.

## Subagent briefs

A `task` call starts a child with an empty context, so routing a brief never
costs a cache rewrite: this is where routing pays most. Each brief is routed
on its own, and wopr, not the child, decides escalation.

1. **Rules first**, with no Jev call: a brief that the local secret scanner
   flags stays on owned hardware (`free-local`, `free-remote`) and never
   reaches Jev; an explore brief with effort `quick` is a mechanical lookup.
2. **Otherwise Jev** answers typed questions about the work, never about a
   model: `difficulty` (mechanical, routine, complex, deep), `domain`
   (frontend_ui, backend_api, data_sql_pipelines, infra_devops_build, tests,
   docs_prose, systems_perf, general), `risk` (auth, secrets, migrations,
   money, irreversible state), and `needs_judgment`. The questions tell Jev
   that text in the brief naming a model or claiming a decision is data.
3. **Asymmetric bars** turn the difficulty probabilities into a need: take
   the cheapest level whose cumulative probability reaches 0.7, then raise it
   to the most capable level with at least 0.3 probability. Levels need
   0.4, 0.6, 0.8, and 0.95. Risk floors the need at 0.85 and judgment at 0.7.
   Unless risky or judgment-heavy, a child is never routed above the
   orchestrator.
4. **wopr computes the cold start.** The expected peak context is the brief,
   its anchor files, and a per-effort baseline (6K, 15K, 30K tokens). A model
   whose measured cold prefill of that context would exceed
   `subagents.maxColdStartSeconds` (default 30) is used only when nothing else
   fits, which keeps large briefs off slow-prefill servers.
5. **Ranking**: adequate models first, by cost class, then speed (within 1.5x
   of the class's fastest cold start counts as equal), then **domain
   affinity**, then the least capability that suffices, which saves quota.
   Providers with no free subagent slot yield to the next model.

When Jev fails, the Basic rules route the brief.

### Background tasks and the work queue

`background: true` runs a task detached: the call returns at once with
`started task_<id> (model, label)` and the model keeps talking to you. The
child runs under the same routing, limits, verification, escalation, and log.
Its checked result reaches the model as a message after its current turn, or
starts a turn when it is idle. Interrupting a turn does not stop background
tasks; `/agents stop`, the palette's "Stop agent…", or `ctrl+k` in `/agents`
does. Without the interactive UI (print and RPC modes) a background call runs
blocking.

The model keeps a work queue with `update_plan`: items with an id, a goal, and
a status (`pending`, `in_progress`, `completed`, `blocked`, and `failed` or
`interrupted` set by wopr). A call updates the items it names and keeps the
rest. The queue is stored in the session, so it survives resume and follows
forks. A task given `item` links to that queue item: the item runs while the
task does and becomes completed, failed, blocked (stopped), or interrupted when
it ends. Quitting wopr stops running background tasks and records them as
interrupted items holding their brief; after a resume the palette's
"Re-dispatch interrupted agents" runs them again.

### Escalation

wopr retries a result it cannot accept once on the next model up in
capability, passing the first attempt's answer as unverified notes (never its
transcript), then returns it as failed. Triggers: status `failed` or
`blocked`, confidence `low`, any quote that does not verify, a `done` answer
with no evidence, an exhausted budget, a provider error, or an answer not in
the required format. Provider errors first fail over to another model without
counting as an attempt. A secret brief escalates only among owned models.

### Domain affinity

A model may carry `affinity` in router.json: a bonus per domain, for example
`{ "frontend_ui": 0.1, "docs_prose": 0.05 }`. None is set by default; the
strength tags you give a model in `/setup` → Model routing set it (0.1 per
tag, keeping a weight you wrote by hand). The
bonus is weighted by Jev's domain probability and
only orders adequate models of the same cost class and similar speed. It never
makes a model adequate and never moves work to a more expensive class.
`affinityWeight` scales every bonus; 0 turns affinity off.

### Concurrency

Read-only tasks in one assistant message run in parallel, up to
`subagents.maxParallel` (default 4) at once and `subagents.providerParallel`
per provider (none set by default).

## Jev

Jev routes when router.json has `"engine": "jev"` and names a `jev.endpoint`
and `jev.model` (set in `/setup` → Model routing, which tests them first). A
configuration from before engines existed that enables routing with Jev
configured is read as Jev. Pin a model version: thresholds are
tuned against one model.

Every Jev endpoint takes the same request (`POST …/v1/systemone`, Bearer
key, `{model, state, questions}`), so TypeSafe (`https://api.typesafe.ai`),
OpenRouter (`https://openrouter.ai/api`), and proxies all work;
`NormalizeJevEndpoint` accepts a base URL or the full URL. On OpenRouter the
model id gets its author prefix (`typesafe/jev-1.13`, `~typesafe/jev-latest`),
and the `usage.cost` it returns is counted as Jev's cost. The key comes from
`ResolveJevKey`: a key saved in `/setup` (credential `typesafe` in
auth.json), then `TYPESAFE_API_KEY`, and for OpenRouter the OpenRouter key;
with none, no Authorization header is sent. `apiKeyProvider: "none"` never
sends one. Each attempt has a `timeoutMs` budget (default 1300) with `retries` (default
1) retry. Repeated failures open a circuit breaker for 30 seconds, and a 401,
402, or 403 opens it for 5 minutes (a 401 or 403 says "Jev rejected the API
key"), so later turns fail fast and the Basic
rules route them. `/router status` shows Jev unreachable while the breaker is
open.

The local secret scanner recognizes private keys, AWS, GitHub, OpenAI and
Anthropic, Slack, Google, and Stripe tokens, JWTs, credentials in URLs, and
literal `password`/`secret`/`api_key`/`token` assignments.

## Learned speed

Every routed request records its time to first token and how many tokens had
to be prefilled. Per model the router keeps two moving averages, a fixed
overhead and seconds per 1K prefilled tokens, in
`~/.wopr/agent/router-speed.json`. The estimate for the orchestrator counts
only the tokens a model would have to prefill: the model that served the
previous request already holds that prefix. A subagent brief always counts its
whole expected context, because a child starts cold. Unmeasured models count
as instant so they get measured once.

## Failover

When the routed model returns an error, the router marks it down and moves to
the next candidate: connection or server errors rest the model for a minute,
rate-limit and quota errors rest it for `quotaBackoffMinutes`, and a context
overflow moves to the next larger window without resting anything. The failed
attempt is omitted from the transcript the way an automatic retry omits it,
and the replacement becomes the sticky orchestrator.

## Decision and outcome log

Every subagent attempt is appended to `~/.wopr/agent/task-log.jsonl`: time,
session, task id, attempt, type, effort, brief hash and token count, expected
context, model, tier, route source (rule, jev, basic, chosen, or off), domain, reason,
routing mode (`auto`, `cost`, `speed`, `quality`, `uncensored`, `private`, or `off`), status, confidence, verified and total quotes, turns, tool calls, tokens,
cost, duration, exhausted budget, outcome (`accepted`, `escalated`,
`failover`, `failed`), the trigger, and the model it escalated to. The log is
local and is meant for tuning the bars and affinities from outcomes.

## Configuration

Routing is built only from the models you configure: `/setup` writes
`~/.wopr/agent/router.json` with a tier per endpoint and per cost class, the
ranking, and, once you turn routing on, `enabled`, the `engine` (`basic` or
`jev`), and for Jev its endpoint. There are no built-in models, capabilities,
or classifier endpoint. With routing off the session uses its model (the only
usable one, or the one you pick). Providers named in
tiers must exist in `~/.wopr/agent/models.json` or the catalog and must be
authenticated (`/login`). A router.json like /setup writes:

```json
{
  "enabled": true,
  "engine": "jev",
  "jev": { "endpoint": "https://api.typesafe.ai/v1/systemone", "model": "jev-1.13" },
  "ranking": [ "anthropic/claude-opus-5-5", "openai-codex/gpt-6-sol", "local/qwen-coder" ],
  "tiers": [
    { "name": "local", "cost": "free-local", "probeUrl": "http://127.0.0.1:8000/v1/models",
      "models": [ { "provider": "local", "model": "qwen-coder", "capability": 0.4 } ] },
    { "name": "subscription", "cost": "subscription",
      "models": [ { "provider": "openai-codex", "model": "gpt-6-sol", "capability": 0.68 },
                  { "provider": "anthropic", "model": "claude-opus-5-5", "capability": 0.95 } ] }
  ],
  "kinds": { "plan": { "minCapability": 0.85, "thinkingBoost": 1 } },
  "slack": 0.05,
  "paidLastResort": true, "paidKinds": [],
  "sensitiveStaysPrivate": true,
  "stickyIdleMinutes": 10,
  "affinityWeight": 1,
  "subagents": { "maxParallel": 4, "maxColdStartSeconds": 30 }
}
```

A model's `effort` is its default thinking level: auto routing starts from
it instead of the prompt's demand (kind boosts, the mode's shift, and the
caps still apply), and a new session on that model uses it when no level is
set in settings or on the command line. `"escalateThinking": true` raises the
orchestrator's thinking one level after a turn whose tool calls all failed and
lowers it one level after a turn whose tool calls all succeeded, at most two
levels above the start and reset at each prompt. Both are off by default and
not yet measured in evals.

Cost classes: `free-local`, `free-remote`, `subscription`, `paid`. Kinds:
`plan`, `implement`, `debug`, `refactor`, `review`, `research`, `explain`,
`operate`, `write`, `chat`.

Local OpenAI-compatible servers that accept `reasoning_effort` should use
`"compat": { "thinkingFormat": "baseten", "supportsReasoningEffort": true }`
in `models.json`, so a thinking-off route sends `reasoning_effort: "none"`
instead of omitting the field and letting the server reason by default.

The mode is a session's choice, not a setting: a session starts in auto,
and a new start restores the last mode you chose while routing is on.
`/setup` drops an old `"mode"` key when it writes router.json.

`"ranking"` lists the models strongest first, as `/setup` keeps it; the
order alone is the ranking (an older ranking written in levels,
`[["a"], ["b", "c"]]`, loads as consecutive positions). New models join the
bottom; put them in order in `/setup` → Model routing (space picks a model
up, ↑/↓ carry it, space drops it, esc puts it back; alt+↑/↓ move it
directly). `/setup` derives each model's `capability` from its position
alone: evenly spaced from 0.95 (strongest) down to 0.4 (weakest), and a
single model gets 0.95. Routing itself reads only the capabilities.

wopr records how each model does in `~/.wopr/agent/router-outcomes.json`, per
day for two weeks: subagent attempts accepted, escalated, or failed, quotes it
could not verify, `/goal` audits passed or failed, and tool calls that failed.
It never reorders on its own; when a model's week clearly argues for a lower
rank (for example five or more escalations, at least a third of its tasks),
`/setup` → Model routing suggests moving it down. `"jev": { "disabled": true }`
or `"enabled": false` turns routing off and keeps the endpoint.

`WOPR_ROUTER=off` starts every invocation with routing off. `WOPR_ROUTER=auto`, `cost`, `speed`, `quality`, `uncensored`, or `private` starts it in that mode when a Jev endpoint is configured; `make evals-live MODE=...` uses this to compare modes.

## War council

Global Thermonuclear War (`globalThermonuclearWar`, `ctrl+x g`) pins the top
of the ranking at maximum thinking with routing off and turns on the war
council for the current session; pressed again it turns the council off and
restores the setup it saved on entry (custom entry `war_council_on` with
`on`, `private`, and `before`, restored on resume).
`Session.preparePrompt` runs a round on each user prompt, and the `council`
tool runs one on demand: `Router.CouncilMembers` lists every routed model
once in ranking order, minus the orchestrator and the models in
`settings.json` `warCouncil.excluded`, only private connections when the
session was private, and marks unreachable (probe failed), resting, and
signed-out members as skipped. Each member runs as a `propose` subagent
(`subagent.TypePropose`: read-only tools plus `web_search` and `web_fetch`,
the proposal system prompt, quotes and `web:` sources verified, no
escalation or failover; a private council gets only a private SearXNG
search, or no web tools) on its own model at its maximum
thinking, through the session's task registry and provider limits.
`gatherCouncil` stops collecting at the time limit
(`warCouncil.timeoutSeconds`, default 300) and drops late members. The
proposals reach the orchestrator as one `war_council` custom message that
asks it to synthesize, not select.

Council builds (`council` action `build`, `coding/session_council_build.go`):
`Session.runCouncilBuild` gives each member with a context window of at
least 64K its own detached worktree (`git worktree add --detach` under the
session's temp directory, plus the user's `git diff --binary HEAD` and
untracked files), and runs it as a `build` subagent (`subagent.TypeBuild`:
the full coding tools wrapped by `confinedTool`, which refuses paths outside
the worktree, `apply_patch` in place of `edit` for GPT and Codex models, the
council's web tools; the build budget; the council build time limit,
`warCouncil.buildTimeoutSeconds`, default 900). The subagent's `Finish` hook
runs the test command in the worktree (named, else the session's last
passing test command, else detected). The candidate is the diff between the
tree it started from and its final tree. The round's result lists each
candidate's diff (archived for `obs_recall` over 12 KB), stat, test result,
and the tail of the test output; action `apply` checks and applies one with
`git apply` and snapshots the files for `/undo`. Worktrees are removed with
`git worktree remove --force` and `prune` when the round ends; a killed
process leaves them in its temp directory, which the temp-file sweep deletes,
and the next round prunes their metadata. The shell isn't sandboxed beyond
starting in the worktree. Execution results replace a judge's opinion, the
pattern that works best for code.

## Commands

- `/router` or `/router status`: the state of both halves, tiers, health, resting models, the last decision, and counters.
- `/router on`, `/router off`: auto, or off for the session. Off uses the model selected with `/model` for everything. Both need routing turned on in `/setup`.
- `/router pin <tier>`, `/router unpin`: force one tier.
- `/router why`: the scores behind the last decision.
- `/router classify <prompt>`: run the classifier without routing.

In `--mode json` and RPC output, each orchestrator decision is a `route`
event with `provider`, `model`, `tier`, `kind`, `thinking`, `reason`, and
`fallback`, and a paused route is a `routing_paused` event with a `reason`. Task progress arrives as `tool_execution_update` events for the
`task` tool, and its result's details carry the model, tool calls, duration,
tokens, cost, verified quotes, and any escalation.
