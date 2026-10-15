# serve-v1-conformance-and-parity — match opencode's remote spec

## Authority

**opencode `v1.18.34` (`anomalyco/opencode`, commit `aec0b9a`) is the specification for `wopr serve`.** Where wopr's
behavior, a DTO, a route, or any document in `docs/` disagrees with it, opencode wins and the document is wrong.
That includes `docs/site/docs/server.md` and the captured KKCode shapes sidecar: "captured client behavior" is
evidence about the *client*, not a reason to keep diverging from the server it talks to. A divergence survives only
as a recorded product decision with a stated reason.

Ratified by the owner 2026-10-09 and written into `AGENTS.md`.

## Orchestrator brief

- **Files touched (S1 only):**
  - `automation/gen-opencode-contract.py` — **new**. Extracts the endpoint inventory from a pinned opencode checkout.
    **It must fail loudly when the resolved count is below the raw `HttpApiEndpoint.` count.** That guard is the whole
    point: a hand parser dropped endpoints across two attempts and under-counted silently both times.
  - `internal/codingagent/serve/testdata/opencode-v1-contract.json` — **new**, generated, committed. This is the
    inventory. `docs/site/docs/server.md` keeps its hand-written per-route prose — that table documents **wopr's 34
    routes** with a wopr-authored Behavior column and cannot be generated from an opencode endpoint list — and gains a
    pointer to the inventory instead. No claim is made that the docs are generated.
  - `internal/codingagent/serve/serve.go` — **one** addition: `routeMux` embedding `*http.ServeMux`. The 34 call
    sites stay byte-identical.
  - `BACKLOG.md` — one item per slice below.
- **Gates (every slice):** `go build ./...` · `go vet ./...` · `go test -count=1` on touched packages ·
  `go tool golangci-lint run` on touched packages · `go fix -diff ./...` empty · `bash -lc 'make check'` before merge.
- **Done when:** every route wopr does not serve is classified with a reason in the generated inventory, every
  `PromptInput` field is either honored to match opencode or waived in writing, and `docs/site/docs/server.md` points at
  the inventory rather than restating it.
- **Out of scope:** opencode **V2** (`/api/*`); `/tui/*`, `/pty/*`, `/sync/*`, `/experimental/workspace/*`,
  `/experimental/control-plane/*`; auth/TLS/CORS.
- **Wave batch:** S1, S2. S3–S6 are filed to `BACKLOG.md` as READY for `ocean`.

---

## 1. What is testable, and what is not

| gap class | closed by |
|---|---|
| route missing | the generated inventory, surfaced in a doc a reader trusts |
| field parsed and then ignored | **a behavioral test per field** — the job of S2+ |

The inventory is endpoint-level. It does **not** compare DTO shapes, so a DTO that disagrees with opencode is not
surfaced by it; that is a per-DTO job, not this artifact's.

The second row produced every bug so far and is the one a static artifact cannot catch. `prompt_async` decoded `model`,
never used it, and the route was mounted the entire time. Two verification rounds rejected the proposed ledger *tests*
as the banned label/format and change-detector categories — correctly: a test over a committed JSON file turns red when
you edit the JSON and stays green when you delete a route. So the inventory is **a generated document, not a gate.** It
earns its place by being trustworthy, not by failing.

## 2. Slices

Each is one feature, gates green, independently committable.

### S1 — the inventory, and a pointer added to docs

- `automation/gen-opencode-contract.py`: parse `HttpApiEndpoint.<method>("<name>", <Prefix>Paths.<key>)` across
  `packages/opencode/src/server/routes/instance/httpapi/groups/*.ts`, resolving each `<Prefix>Paths` map. **The maps mix three value forms**
  — plain quoted strings (`file.ts`: `list: "/file"`), template literals, and a bare `root` alias — and a parser that
  handles only one of them silently drops whole group files. The guard: abort if `resolved < raw count` where **raw**
  is the total `HttpApiEndpoint.` occurrences in the same source tree (`packages/opencode/src`, **127** across **19 of 21** group files).
- Emit `internal/codingagent/serve/testdata/opencode-v1-contract.json`: `{pinned:{repo,tag,commit},
  endpoints:[{method,path,name,group,phone,disposition,waiver,backlog}]}`. `disposition` is `mounted` | `stubbed` |
  `absent` — the four 501s (`fork`, `revert`, `diff`, `permissions`) are `stubbed` with a waiver, because a 501 on a
  phone route is a tracked gap and `mounted` would hide it.
- `serve.go`: add `type routeMux struct { *http.ServeMux; routes []routeSpec }` with a recording `HandleFunc`. All 34
  existing call sites are unchanged text; the embedded method still registers, so the bad-pattern panic still fires.
  The `routes` field is exposed for potential future tooling. `type routeSpec struct { Method, Pattern string; Handler http.HandlerFunc }` is added.
- **No new test in S1.** The inventory is generated and committed; `docs/site/docs/server.md` gains a pointer to it.
  Adding a test over this artifact is the banned category and is not proposed again.
- **Known gap this leaves:** nothing in the repo catches a route being *removed* from `serve.go`.
  `TestNewRegistersEveryRouteWithoutPanicking` checks only `/permission` and `/question`; roughly eight routes have no
  behavioral test at all. That is a pre-existing hole, recorded here rather than silently inherited — closing it means
  writing per-route tests, which is the work this whole effort exists to reduce.

### S2 — `agent`, matched to opencode

opencode resolves the agent per prompt and persists it on the user message (`prompt.ts:657-667`), then writes it to
the session when it differs (`sessions.setAgentModel`, `prompt.ts:672-690`). Matching that means **wopr's serve grows
per-session agent state**: `sessionDTO.Agent` exists (`serve_dto.go:24`) and is emitted as `""` because
`buildSessionDTO` never assigns it, and `agent.UserMessage` has no agent field (`agent/agent.go:33-41`). So the slice
populates an existing field rather than inventing one.

- `promptBody` gains `Agent *string` (optional) alongside `Model *modelRef`. The agent is **validated before the 204**
  (exactly like `body.Model`), resolved against the **hardcoded agent list** (`explore`, `general`, `propose`, `build` —
  the names served by `GET /agent`, `serve_handlers.go:533`). This list is **not** from `AvailableModels` (which
  returns models, not agents).
- unknown agent → `400 {"error":"agent not found: …"}`, no fallback (owner decision 2026-10 — a deliberate divergence
  from opencode, which falls back to the default; recorded as such);
- known agent → applied in the same run-slot `before` hook the model uses, and `GET /session/{id}` reports it.

**Gate:** `TestPromptAsyncAppliesTheClientAgent` — a prompt naming `general` leaves `GET /session/{id}` reporting
`agent: "general"`, and an unknown name is `400`. **The test waits for the run to complete** (`waitForIdle`)
before asserting `GET /session/{id}` reports the agent, because the model swap happens inside the run slot.
**Mutation that must turn it red:** make the run-slot `before` hook ignore `body.Agent`, or delete the `Agent`
assignment in `buildSessionDTO` — either leaves `"agent":""` while the test asserts the requested name. If the gate
cannot be made red that way, the slice is not done.

### S3 — `system`, `noReply`, `parts: agent|subtask` — DEFERRED to backlog

Not in this wave: `system` has **no identified observable** — the override reaches the model, not any DTO, and there is
no test seam for it yet — and `parts: agent|subtask` has none named either. Naming those seams is research, not
implementation, and a slice whose gate cannot be written should not be dispatched. Filed as three separate READY items
(`serve-prompt-system`, `serve-prompt-no-reply`, `serve-prompt-parts-agent-subtask`); `noReply` is ready now (user
message count +1, assistant count +0), the other two are not.

### S4 — `variant` (backlog, `serve-variant-wire-alias`)

opencode derives variants from models.dev `reasoning_options` (`provider/transform.ts:1717`): one per effort value, or
a thinking toggle, or a token budget. `variant: "high"` is the *same model at high reasoning effort* — wopr already has
that as `ai.ThinkingLevel` (`coding/session.go:897`, clamped per model). Missing: the wire alias. `sessionModel.Variant`
is a hardcoded `"default"` and `providerModelDTO.Variants` an empty map.

**Under the authority rule the hardcoded `"default"` is now wrong**, not merely cautious: opencode emits a real variant.
Deferred only because no captured request sends one. When built, it emits the real variant — the shapes sidecar's
"captured `default`" no longer justifies it.

### S5 — `tools` (backlog, `serve-prompt-tools-permissions`) and `format`

opencode converts `tools` into session permission rules (`prompt.ts:1060-1067`):
`{permission: t, action: enabled ? "allow" : "deny", pattern: "*"}`, written to the session. Matching that needs a
permission subsystem wopr does not have — `Permission` appears in three non-test files, all in the serve package, all
stubs, and `listPermissions` returns a hardcoded `[]any{}`. A subsystem item, not a field item.

`format: json_schema` needs a structured-output tool; wopr's system prompt already promises it.

### S6 — absent endpoints (backlog)

The phone-relevant endpoints wopr does not serve are filed as READY backlog items for `ocean`, not built here. The
inventory is what makes them tracked rather than forgotten: each carries the backlog item that will close it. **No
count is quoted here** — the generator produces it.

## 3. Risks

- **The inventory rots.** opencode moves past `v1.18.34`. The pin and a regeneration command ship in the generated file.
  Staleness is not auto-detected — deliberately, since a network check would break hermeticity. Revisit on upgrade.
- **The generator is the new single point of failure.** If it under-counts again, the docs are wrong. Hence the loud
  guard, and hence *never quote a count in prose that the generator cannot reproduce*.
- **S1 touches the mux construction.** Every serve test must pass unedited.

## 4. Adjacent: the parked V2 plan is rotting

`docs/plans/2026-10-07-serve-v2-api-parity.md` is parked (BACKLOG: "NOT needed for KKCode"). Its model of record is
`/tmp/openapi-v2.json` — ephemeral — and it has no inventory, so nothing would notice. One backlog line: check it in,
re-download with a recorded digest, or accept the loss in writing.

## 5. Decisions, ratified 2026-10-09

| # | question | decision |
|---|---|---|
| 1 | unknown `agent` → 400 or fall back? | **400**, no fallback. Divergence from opencode, recorded. |
| 2 | `tools` — honor or waive? | **Honor**, as a backlog item. Needs a permission subsystem first. |
| 3 | `variant` — implement or waive? | **Implement**, as a backlog item. The hardcoded `"default"` is now a divergence, not a caution. |
| 4 | absent endpoints — this wave or later? | **Later.** Filed READY for `ocean`. |
| 5 | what wins when the docs disagree with opencode? | **opencode.** Recorded in `AGENTS.md`. |
