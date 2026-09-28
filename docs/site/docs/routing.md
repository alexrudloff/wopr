# Routing and subagents

The orchestrator is the model that holds your conversation; subagents are
short-lived helpers it starts with the `task` tool.

## Without routing

Model routing is off unless you turn it on. You pick the model with `/model`,
which lists the models you set up in `/setup` (grouped by connection), Tab
on an empty prompt cycles those models, and subagents run on the model you
picked. No modes are offered, and the prompt panel and
sidebar show no routing.

## Turning routing on

In `/setup`, open **Model routing** and set **Routing** (←/→) to:

- **Basic**: fixed rules pick a model from your order and what wopr knows
  about each model (its window, measured speed, and cost class). Nothing
  else to set up.
- **Jev**: Jev classifies each prompt and subagent brief. Enter the Jev
  endpoint, an API key, and the model (for example `jev-1.13`); **Save** asks
  Jev to classify a sample prompt and keeps the choice only if it answers.
  The endpoint can be a base URL or the full `https://…/v1/systemone` URL:

  | Where Jev runs | Endpoint | API key |
  |---|---|---|
  | TypeSafe | `https://api.typesafe.ai` | your TypeSafe key, or `TYPESAFE_API_KEY` |
  | OpenRouter | `https://openrouter.ai/api` | your OpenRouter key: the one you connected in `/setup` or `OPENROUTER_API_KEY` is used when the field is blank |
  | A proxy or self-hosted endpoint | its URL, for example `http://proxy.lan:8001` | blank if the proxy holds the key |

  A typed key is saved in `auth.json`, never in router.json. Use TypeSafe's
  model ids everywhere; on OpenRouter wopr sends `typesafe/jev-1.13` (and
  `~typesafe/jev-latest` for `jev-latest`).
- **Off**: wopr runs the model you pick.

With Basic or Jev, the screen holds **Put your models in order**: your models, strongest first. New models
join the bottom. To move one, press space to pick it up, ↑/↓ to carry it
(the list reorders as you go), and space or enter to drop it; esc puts it
back where it was. alt+↑/alt+↓ move the highlighted model directly. Enter
on a model opens its settings: use for routing, abliterated, and its
strengths, the kinds of work (frontend, backend, data/SQL, infra/build,
tests, docs, systems/perf, general) where Jev routing prefers it among
models equally able. A model's strength number
comes from its place: evenly spaced from 0.95 at the top to 0.4 at the
bottom. **Save** keeps changes (turning routing off keeps the Jev endpoint),
and **Cancel** changes nothing.

### Basic rules

The Basic rules are fixed. A model is eligible when it is reachable, not
resting after errors or a quota limit, allowed by the mode, and the
conversation plus room for its reply fits its window (the one you set, if
you set one). Auto and quality take your highest-ranked eligible model;
speed takes the least expected time to a reply (measured time to first
token plus decode speed; unmeasured counts as slow); cost takes local, then
free remote, then subscription, highest-ranked within each, and never pays
per token; uncensored takes your highest-ranked abliterated model.
Pay-per-token models otherwise come last. The orchestrator keeps its model
while it stays eligible; subagents follow the same rules at the size they
are expected to reach. Thinking is your level (or the model's own effort
setting). Strength tags are for Jev only.

If Jev stops answering mid-session, the Basic rules route until it answers
again, and one notice says so. If no model is eligible, the request fails
with the reason; a mode never falls back to a model it didn't choose.

## States (routing on)

| State | How to enter it | Orchestrator | Subagents |
|---|---|---|---|
| Auto | A mode for the orchestrator (`/model`, Tab), `/router on` | The router picks the model (Jev also the thinking level) | Their own choice |
| Pinned | Pick a model (`/model`, F2, Tab) | Your model and thinking level | Their own choice |
| Off | `/router off` | Your model | Run on your model |

The subagents' choice is separate: see [Orchestrator and subagents](#orchestrator-and-subagents).

In auto, the orchestrator is sticky: wopr keeps the model while its prompt
cache is warm and only moves to a more capable one mid-session. It picks
afresh after 10 idle minutes, after a compaction, or in a new session.

## Modes

A mode is what routing optimizes for the orchestrator or the subagents,
whichever it was chosen for. Modes are
offered only while routing is on. A mode lasts for the session, and a new
start restores the last one you chose. The table is how Jev applies each
mode; see [Basic rules](#basic-rules) for Basic.

| Mode | Picks | Thinking |
|---|---|---|
| auto | The cheapest model likely to be good enough | As decided |
| cost | Free models unless they clearly cannot do the job, are unavailable, or cannot fit the context; then the subscription with the most quota left in its usage window, least capable model first. Never pay-per-use: if nothing free or subscription can serve, the request fails | One step lower |
| speed | The fastest model that is capable enough; a model with no speed measurements counts as slow | One step lower |
| quality | Any available model within 0.05 of the strongest one available, local models included; cost is ignored except pay-per-use, which stays a last resort | One step higher |
| uncensored | Only models marked abliterated (`"uncensored": true` in router.json), for everything. If none is available, the request fails rather than use another model | As decided |

Choose a mode in `/model` (the Modes section comes first in each picker),
or cycle the orchestrator's with Tab on an empty prompt (auto, cost, speed,
quality, uncensored, then your configured models). A mode picked for the orchestrator (Tab, the Orchestrator picker, or
`/model <mode>`) is the subagents' mode too. A model picked for the
orchestrator changes only the orchestrator; the subagents keep their
choice. `/model` → Subagents splits them, and that choice stays until a
mode is next picked for the orchestrator. Uncensored
appears only when a model is flagged.

## Orchestrator and subagents

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

## The task tool

The model can delegate a read-only investigation with `task`: a short
description, the type `explore`, a brief, optional anchor `paths`, and an
effort (`quick`, `medium`, `thorough`). The subagent starts with a fresh
context, reads and searches with read-only tools, and answers in a fixed
format. wopr checks every quote it cites against the file or command output,
retries once on a stronger model when the answer cannot be trusted, and adds
a footer with the model, tool calls, duration, tokens, cost, and how many
quotes verified. Several tasks in one reply run in parallel. Subagents cannot
start subagents.

In the transcript a task is one row: `│ Explore — description`, then
`↳ current tool` while it runs and `↳ n toolcalls · duration · model` when it
is done. Ctrl+O shows the checked result. The sidebar counts
tasks by where they ran, and its Agents section shows each task live.

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

### Goals

`/goal <objective>` sets a session goal and starts work on it. After every
turn that ends without the goal met, wopr sends the next turn itself
(`◎ Goal · continuing (turn n/20)` in the transcript), routed like any turn.
The model claims completion by ending its reply with a `GOAL MET` line and
the evidence. wopr then runs an independent audit: a read-only subagent,
routed as a `routine` brief without a Jev call and, when an equally cheap
adequate model exists, on a different model family than the one that did
the work. The auditor checks the files itself and must answer `PASS` with
quotes wopr verifies; anything else goes back to the model as feedback and
the loop continues. A pass marks the goal done with a toast. With subagent
routing off, the audit runs on your model.

Caps pause the goal: 20 automatic turns, $1.00 of non-free spend (turns plus
subagents), one hour of active time, or three automatic turns in a row
without a tool call. Set them per goal with
`/goal --turns 30 --cost 2 --time 45m <objective>`. A reply ending in
`GOAL BLOCKED: <why>`, an interrupted or failed turn, and reopening the
session also pause it. `/goal` shows the status, `/goal resume` continues
with a fresh window of caps, and `/goal stop` ends it. Anything you type
goes first: no automatic turn starts while the editor holds text or a
question waits, and none starts while background tasks started during the
goal still run; their results arrive first.

The goal is stored in the session and shows at the top of the sidebar's Queue as a
pinned `◎` row with its turns, spend, and time. `wopr -p "/goal <objective>"`
runs the loop headless and exits when the audit passes (status 0) or a cap
pauses it (status 1).

## What stays private

Before anything is sent to Jev, a local scanner looks for credentials (private
keys, API tokens, JWTs, passwords in URLs or assignments). Hits are redacted,
and a task brief with a hit runs only on your own hardware.

## router.json keys

| Key | Default | Meaning |
|---|---|---|
| `enabled` | `false` | Routing on. Sessions start in auto, or in the last mode you chose. |
| `engine` | `jev` when a Jev endpoint is set, else `basic` | What routes: `basic` or `jev`. |
| `ranking` | unset | Models strongest first, as `/setup` keeps them; their `capability` values are derived from it. |
| `jev.disabled` | `false` | Stop using Jev and keep the endpoint: the Basic rules route. |
| `jev.endpoint`, `jev.model` | unset | The Jev classifier; without both, the Basic rules route. The endpoint may be a base URL (`https://api.typesafe.ai`, `https://openrouter.ai/api`) or be the full `https://…/v1/systemone` URL. |
| `jev.apiKeyProvider` | unset | Unset: the key saved in `/setup`, then `TYPESAFE_API_KEY` (OpenRouter endpoints: then your OpenRouter key or `OPENROUTER_API_KEY`); no key found sends none. `none`: never send a key. Another provider id: that provider's key. |
| `jev.timeoutMs`, `jev.retries` | `1300`, `1` | Per-attempt timeout and retries. |
| `stickyIdleMinutes` | `10` | Idle time after which the orchestrator may change model. |
| `tiers[].models[].uncensored` | `false` | Model allowed in uncensored mode. |
| `tiers[].models[].hashline` | unset: on for free-local and free-remote tiers (with the `hashline` setting at `auto`) | Hashline anchors on `read` and `edit` for this model. See [settings](settings.md#hashline-anchors). |
| `tiers[].models[].effort` | unset | Default thinking level for this model: auto routing starts from it instead of the prompt's demand, and a session that starts on the model without a level set in settings or on the command line uses it. |
| `escalateThinking` | `false` | Raise the orchestrator's thinking one level after a turn whose tool calls all failed, and lower it one level after a turn whose tool calls all succeeded (at most two levels up, reset at each prompt). Unmeasured. |
| `tiers[].models[].affinity` | unset | Per-domain tie-breaker among equally cheap, adequate models; the strengths picked in `/setup` set it. |
| `affinityWeight` | `1` | Scales affinity; `0` turns it off. |
| `subagents.maxParallel` | `4` | Concurrent subagents. |
| `subagents.providerParallel` | unset | Concurrent subagents per provider. |
| `subagents.maxColdStartSeconds` | `30` | Keep a task off a model whose measured cold start would take longer. |

Every subagent attempt is logged locally to `~/.wopr/agent/task-log.jsonl`.
`/router status` shows both halves of routing, the tiers, and the counters.
