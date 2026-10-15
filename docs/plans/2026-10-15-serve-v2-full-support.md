# serve-v2-full-support — full contract coverage of opencode V2 `/api/*`

- Date: 2026-10-15
- Status: **Rev-2.** Round 1's claims were re-derived on 2026-10-15 from the pinned spec (§1) and from
  this checkout at `3421839`. Round 2 re-derived every count, required-field list, and `file:line`
  in Round 1 after a four-angle verification fan reported defects; §0 records what that round changed and
  what Round 1 dropped from the superseded plan. Round 3 found the premise wrong: **there are two V2 surfaces,
  not one** (§1.1). Everything downstream of that — `/api/health`, the event names, the SDK, §10 —
  was re-derived against both. Which one is the specification is an **open owner decision** (D9) with
  a recommendation recorded beside it; this document does not decide it. No claim here rests on a file
  in `/tmp`.
- Supersedes: `docs/plans/2026-10-07-serve-v2-api-parity.md` (Rev-1, marked superseded).
  Where the two disagree, this document wins; §0 lists every disagreement.
- Backlog item: `serve-v2-api-parity` in `BACKLOG.md` (filed by this plan; the 2026-10-07 plan
  reserved the id but never filed it).
- Authority: `AGENTS.md` — opencode is the specification for `wopr serve`. A divergence survives
  only as a recorded decision with a stated reason.
- Scope decision (owner, 2026-10-15): **full contract coverage**. Every `/api/*` path answers a
  spec-valid response. Where wopr has the engine, the answer is real; where it does not, the
  answer is a spec-blessed stub carrying a named reason. No route may answer Go's bare 404.
  Literal implementation of all 142 operations is **out of scope** — see §3.3.

## 0. What the true-up found

Rows 1-8 are Round 1. Rows 9-17 are Round 2: defects a verification fan found in Round 1 and
rows 18-24 are Round 2 facts Round 1 dropped from the superseded plan without recording the drop.
Rows 25-31 are Round 3, from a second fan.

| # | Round 1 claim | Verified 2026-10-15 | Evidence |
|---|---|---|---|
| 1 | Model of record `/tmp/openapi-v2.json`, 537.9 KB, sha `17951689…` | **File gone.** The spec moved: 578,272 bytes, sha `3fcd666b597b746b…`, **118 paths / 142 operations / 285 schemas** (was 117 / 141 / 256) | §1 |
| 2 | "`Command.Info` = `{name,description,template}`" | `{name, description}`; required is `[name]` only | spec `Command.Info` |
| 3 | "the opencode V2 spec" pinned at `f77342d` seams | **Claim withdrawn, not re-scoped.** Rev-1 asserted 1.18.34's source has zero `/api` route literals. `packages/protocol/src/groups/` is 18 files defining **58** V2 endpoints at `aec0b9a`, `/api/health` among them. The premise was wrong, not the scope | §1.1, §10 |
| 4 | "no spec `NotImplemented` tag exists" | Correct, but **`ServiceUnavailableErrorEncoded` exists** and 25 operations document 503 — so the stub answer is spec-blessed, not invented | §4.3 |
| 5 | "`Success{location,data}` envelope" as the general shape | Envelope is **per-operation, not universal**: 40 ops use `{location,data}`, 33 return an inline object, 13 a bare `$ref`, 7 a non-object (`4` array, `2` boolean, `1` `anyOf`), and **49** have no 200 JSON at all | §4.1 |
| 6 | D3 auth open; D2 `/api/status` open; D6 open | D3 **decided** by owner 2026-10-07 (ignore `Authorization`, never emit 401 — `BACKLOG.md`, `serve-kkcode-v1-compat` notes). D2 **moot**: `/api/status` is in no spec and is a KKCode fallback probe only. D6 **answered by the spec** (§4.3) | §6 |
| 7 | Citations "re-read at `f77342d`" | All §7 and §10 citations re-derived today at `3421839` | §10 |
| 8 | — | **New:** wopr already serves two `/api/*` routes, and `GET /api/command` violates the V2 schema today (§5). This is the only route where a V1 client and the V2 spec collide | §5 |
| 9 | — | **Rev-0 defect:** the tier lists did not partition the 142. Rev-0 claimed `30 + 112`; T1 actually enumerated 29 (counting `/api/command` twice, once in T1 and once in T3), `GET /api/form` and `GET /api/permission/request` sat in no tier, and `permission/saved (3)` silently absorbed one of them. **Now 28 + 113 + 1 = 142**, each operation in exactly one tier | §2 |
| 10 | — | **Rev-0 defect:** §3.1.5 read "`serve` still imports nothing from `coding/`". `serve` imports `coding/host` in six non-test files today (`internal/codingagent/serve/serve.go:9`, `serve_handlers.go:22`, `serve_prompt.go:13`, `serve_question.go:15`, `serve_read.go:18`, `serve_sse.go:17`). The live invariant is narrower: **the root `coding` package** (`BACKLOG.md:21`, `docs/architecture.md:35`) | §3.1 |
| 11 | — | **Rev-0 defect:** §4.2 counted 35 error schemas and described a universal `{_tag,message}` shape. There are **33** `*ErrorEncoded` (57 `*Encoded`); 7 of the 33 require only `[_tag, message]` and **26 carry further required fields**; `WorktreeErrorEncoded` has no `message` property at all | §4.2 |
| 12 | — | **Rev-0 defect:** §4.5 under-specified required fields — `Model.Info` and `Agent.Info` lists each omitted four, `TokenUsage.Info.cache`'s nested `read`/`write` requirement was unstated, and `Money.USD` (a bare `number`) had lost its Rev-1 recorded decision. Rev-0 also analysed 11 of the 29 T1 response schemas and **no** T1 request body or query parameter | §4.5, §4.6 |
| 13 | — | **Rev-0 defect:** §6 D5 claimed questions flow through `question.v2.asked` "which wopr already emits". wopr emits `question.asked` / `.replied` / `.rejected` (`serve_sse.go:52-54`) — the `v2` rename is a V2-encoder decision that Rev-0 dropped with the event table it belonged to | §4.4, §6 D5 |
| 14 | — | **Rev-0 defect:** S1's gate ("142 classified, no duplicates; re-run the generator and confirm an unchanged file") cannot fail on a wrong classification — a hand-written tier table passes it whatever it says — and the second half is the change-detector category `AGENTS.md:53-56` bans. The V1 conformance plan ruled against exactly this artifact-as-gate in writing (`docs/plans/2026-10-09-serve-v1-conformance-and-parity.md:43-50,71-72`) | §8 S1 |
| 15 | — | **Rev-0 defect:** §2's rule "a T2 answer with no reason in `BACKLOG.md` does not count" invalidated its own list — `serve-7` covers permissions only, and neither "no engine in wopr" (43 operations) nor "new persistence subsystem" appears in any backlog entry. The reason now lives in §2's own group list, and the forms reason is stated here rather than mis-cited to `serve-7` | §2 |
| 16 | — | **Rev-0 defect:** §7's `serve_handlers.go:154-456` over-ran by 28 lines into three project functions; `GET /api/session/active`, `GET /api/agent/{agentID}`, and `GET /api/provider/{providerID}` were cited to seams that do not exist | §7 |
| 17 | — | **Rev-0 defect:** §4.4 characterised `x-effect-stream.causeSchema` as "an error channel carrying the error envelope". It is a three-arm union whose `error` member is literally `{"not":{}}` — i.e. *no schema at all* | §4.4 |
| 18 | Go 1.22+ `ServeMux` prefers the more specific pattern, so a `/api/` catch-all loses to registered routes | **Still true and still load-bearing**, but Rev-0 stated the conclusion ("no route answers Go's default 404, 405") without naming the mechanism any slice must implement. S2 names it | §8 S2 |
| 19 | Error writer emits six tags: `UnauthorizedError`, `InvalidRequestError`, `SessionNotFoundError`, `FileNotFoundError`, `ServiceUnavailableError`, `UnknownError` | Dropped by Rev-0 with no note. Restored; each is a spec `*ErrorEncoded` with a required-field set (§4.2), so "emit `_tag` + `message`" would have violated **two** of the six — `SessionNotFoundError` requires `sessionID`, `FileNotFoundError` requires `path` | §4.2, §8 S2 |
| 20 | Coexistence cost — "two response shapes, two SSE encoders, two DTO sets in one package" — with three mitigations | Dropped by Rev-0. Restated as a cost in §2 and a risk in §9; the mitigations are the single catch-all (§8 S2), the single static V1→V2 event table (§4.4), and V2 DTOs carrying no wopr-only fields (§4.5) | §4.4, §9 |
| 21 | Rejected alternative in Rev-1 §3: teach `eventMapper` to emit both shapes — the bus stores only `(name, payloadBytes)` (`bus.go:98-101`), so a second shape doubles every subscriber's queued bytes and couples two wire vocabularies in one producer | Dropped by Rev-0. This is *why* §7 may say "one producer"; restored | §4.4 |
| 22 | Scope-escalation tripwire: if a live capture shows the client needs `session.next.*` for streaming rather than `message.part.delta`, that is a scope escalation and stops the slice | Dropped by Rev-0. Restored | §9 |
| 23 | Rev-1 §10 cited the seams Rev-0 never re-derived: `serve_sse.go` (V1 SSE mapper), `serve_question.go` (question handlers), and the hermetic `TestMain` harness every gate depends on | Restored and re-derived at `3421839` | §7, §8 |
| 24 | Rev-1's per-slice DoDs asserted outcomes ("curl the handshake routes", "a disk-only session is listed and readable", "the pickers decode every required field", "`make check` docs-drift green") | Rev-0 reduced all six to two-word labels ("Connection test.", "Session picker."). Restored as assertions. **S1 still has no DoD** — it is a generated document plus a review gate (§8 S1), so "all six" was one too many | §8 |
| 25 | Rev-1 treated `https://opencode.ai/v2/openapi.json` as *the* model of record | **Wrong on its own terms.** `packages/protocol/src/groups/` at `aec0b9a` is a second V2 surface: **58** endpoints, **46** shared with the published spec, **12** protocol-only, **96** published-only. Which surface is the specification is now D9 (open) | §1.1 |
| 26 | §5 and §0 row 19: `/api/health` is "not in the V2 spec at all" | True of the **published** spec, false of the **protocol** surface, which defines `health.get` on `GET /api/health` returning `{healthy: Schema.Literal(true)}` — byte-identical to what wopr already serves. Restated per surface | §1.1, §5 |
| 27 | §4.4's V2 event-name column, asserted over 13 rows | The names are **not** in the published JSON (zero literal occurrences of any of them, and `/api/event` declares `event: {type: string}` with no enum) — but they are **sourced**, from `packages/sdk/openapi.json` at `aec0b9a`, whose `V2Event` is an 88-arm `anyOf` naming all 88. Column kept, source cited. The two pinned artifacts declare **different** unions (88 vs `EventManifest.ServerDefinitions`), which is part of D9 | §4.4 |
| 28 | §4.4: `session.idle` is "new", V1 column `—`; `server.connected` and `server.heartbeat` unmentioned | `session.idle` is already a V1 bus event (`serve_sse.go:50`), emitted at `:765`. The other two are the 13th and 14th constants and are written straight to the stream (`:206`, `:225`), never published — so **12** constants are bus-resident, not 14. Three rows added/fixed | §4.4 |
| 29 | §4.5 `SessionActive` = `{type:"running"}`, "a one-property tagged union" | The **200 body** is `{data: {<sessionID matching ^ses>: {type:"running"}}}`, `required:["data"]`, `additionalProperties:false`. The bare object matches no pattern. T1 inside S3 | §4.5 |
| 30 | §4.6: message `type` filter is `agent-switch`/`model-switch`/`location-switch` | The published enum is ten values, each with a trailing `d`: `agent-switched, model-switched, location-switched, user, synthetic, system, skill, shell, assistant, compaction`. `GET /api/project`'s `archived` query was also absent from the table entirely | §4.6 |
| 31 | §4.1: 36 inline objects, 46 with no 200 JSON | Re-derived: **33** inline, **49** with no 200 JSON — 46 that declare no 200 at all, plus 3 that declare a non-JSON 200 (`/api/event` and the experimental log as `text/event-stream`, `GET /api/fs/read/*` as `application/octet-stream`). Rev-1's "all 204, plus the SSE stream" was wrong on both counts: the 46 *is* the 204-only set, and both SSE routes do declare a 200 | §4.1 |

## 1. The pin

| Field | Value |
|---|---|
| Source URL | `https://opencode.ai/v2/openapi.json` |
| Fetched | 2026-10-15 |
| sha256 | `3fcd666b597b746bad26f56a92fe782cc86338a0b4498f43b435a2f97e4fa05a` |
| Size | 578,272 bytes |
| Document | OpenAPI 3.1.0 |
| Counts | **118 paths / 142 operations / 285 schemas** (201 of them `additionalProperties:false`) |
| `info` | title `opencode HttpApi`, version `0.0.1`, description *"Experimental HttpApi surface for selected instance routes."* |
| `servers` | **key absent** (not an empty array) — every URL in the spec is a path or absolute |
| `security` | `[]` |
| `components.securitySchemes` | **present and `{}`** — an empty scheme map, not an absent key |
| Consequence | the spec documents `401` on all 142 operations and defines no scheme, so "secured" is not derivable from the document at all (§6 D3) |

**Do not vendor the JSON.** The repo does not vendor specs and a 578 KB blob rots the same way a
`/tmp` path does. **Exception:** S1 emits `testdata/opencode-v2-schemas.json` — a curated subset of
the 142 operations' request/response schemas that S3–S5 decode against, serialized as JSON Schema,
pinned by sha256 in the inventory and `server.md` so decoders are bound to a committed artifact
rather than a network fetch. The pin is this table plus the inventory in §2; re-derive it with:

```bash
curl -sSL https://opencode.ai/v2/openapi.json -o /tmp/openapi-v2.json
sha256sum /tmp/openapi-v2.json
```

When the hash moves, the spec changed: re-read §2 and §4 against the new file before implementing
against it, and treat any operation this document does not mention as **unclassified** (it must
land in T2 with a reason, or the definition of done in §3.1 is not met). The generator described in
`docs/plans/2026-10-09-serve-v1-conformance-and-parity.md` §S1 is the durable fix for this loop
and is not built yet; until it is, this re-derivation is a manual gate on S1.

**This rule does not extend to the second surface.** The published spec is a file on the internet
and needs a hash. The protocol surface (§1.1) is TypeScript in the opencode tree, versioned
together with the code that implements it, cited by commit — it cannot rot the way a `/tmp` path
does and needs no vendoring. It is a candidate authority on that basis alone, which is part of D9.

### 1.1 There are two V2 surfaces, and this document pinned only one

This is the defect that shaped Rev-2. `AGENTS.md` names opencode as the specification for
`wopr serve`; Rev-1 read that as the published JSON and never checked whether the pinned
opencode checkout agreed. It does not, and the difference is not cosmetic.

**Surface A — the published spec.** The table above. 118 paths / 142 operations / 285 schemas.

**Surface B — the protocol source at `aec0b9a` (v1.18.34).**
`packages/protocol/src/groups/` — 18 TypeScript files, **58** endpoints, written as
`HttpApiEndpoint.get("event.subscribe", "/api/event", …)`. The set is `server.*` plus one group per
prefix. Alongside it, `packages/sdk/openapi.json` is the generated OpenAPI the JS SDK is built from
(`packages/sdk/js/src/v2/gen/types.gen.ts:1` — "auto-generated by @hey-api/openapi-ts"): 162 paths /
188 operations / 472 schemas, of which the 58 are Surface B and the other 130 are the V1 routes.

Compared, normalizing the protocol package's Express-style `:param` to `{param}`:

| | count |
|---|---|
| published operations | 142 |
| protocol endpoints | 58 |
| **identical method + path** | **46** |
| protocol-only (absent from the published spec) | **12** |
| published-only (absent from the protocol source) | **96** |

The 12 protocol-only operations:

```
GET    /api/health                                    packages/protocol/src/groups/health.ts
GET    /api/question/request                          question.ts
GET    /api/session/{sessionID}/event                 session.ts
GET    /api/session/{sessionID}/history               session.ts
GET    /api/session/{sessionID}/question              question.ts
POST   /api/session/{sessionID}/question/{requestID}/reply     question.ts
POST   /api/session/{sessionID}/question/{requestID}/reject    question.ts
POST   /api/session/{sessionID}/revert/clear          session.ts
POST   /api/session/{sessionID}/wait                  session.ts
GET    /api/integration/attempt/{attemptID}           integration.ts
POST   /api/integration/attempt/{attemptID}/complete  integration.ts
DELETE /api/integration/attempt/{attemptID}           integration.ts
```

**Consequences carried through the rest of this document:**

- `/api/health` is not absent from V2. It is absent from the **published** spec and **present** in
  Surface B — `health.get`, `success: Schema.Struct({ healthy: Schema.Literal(true) })`, which is
  byte-identical to what wopr already serves. §5 and the backlog item now say which surface each
  statement is about.
- The V2 event names are locally derivable, from Surface B — see §4.4, which now carries the source.
- The generated SDK belongs to Surface B. It names `/api/health` once and `/api/info` **zero**
  times; the published spec is the exact inverse. It is not evidence about the published spec, and
  §9 no longer treats it as a contradiction of it.
- The 46 shared endpoints are the only operations where "the two surfaces agree" is a meaningful
  statement. For the other 108 it is not, and this document does not pretend otherwise.

**What is NOT established here, and must not be read in:** which surface a client speaks, and
therefore which one `wopr serve` is specified against. That is **D9 (§6)** and it is the owner's.
This document's working rule until D9 is decided is the weaker one: where the two surfaces disagree,
the pinned artifact each claim came from is named beside the claim.

```
opencode-src @ aec0b9a — re-derive the comparison with:
  grep -rhoE 'HttpApiEndpoint\.[a-z]+\("[^"]+", *"[^"]+"' packages/protocol/src/groups/
```

## 2. Inventory (142 operations)

Per-prefix operation counts at the pin:

| Prefix | Ops | Prefix | Ops | Prefix | Ops | Prefix | Ops |
|---|---|---|---|---|---|---|---|
| `/api/session` | 39 | `/api/experimental` | 29 | `/api/integration` | 11 | `/api/pty` | 7 |
| `/api/vcs` | 6 | `/api/credential` | 5 | `/api/shell` | 5 | `/api/worktree` | 4 |
| `/api/permission` | 3 | `/api/plugin` | 3 | `/api/fs` | 3 | `/api/agent` | 2 |
| `/api/config` | 2 | `/api/debug` | 2 | `/api/location` | 2 | `/api/mcp` | 2 |
| `/api/model` | 2 | `/api/project` | 2 | `/api/provider` | 2 | `/api/websearch` | 2 |
| `/api/command` | 1 | `/api/event` | 1 | `/api/form` | 1 | `/api/info` | 1 |
| `/api/pair` | 1 | `/api/reference` | 1 | `/api/rpc` | 1 | `/api/skill` | 1 |
| `/auth/connect` | 1 | | | | | | |

Session surface (39), for the record: list, create, active, get, patch, delete, agent, background,
command, compact, context, diff, environment, fork, form (5: list/create/get/delete/reply),
generate, inbox (3: list/delete/patch), interrupt, message (2), model, move, permission (4),
prompt, revert (3), shell, synthetic, view.

**The three tiers partition all 142 operations: T1 = 28, T2 = 113, T3 = 1.** Each of the 142
appears in exactly one tier. These are **published-spec** tiers (§1.1 Surface A). `/api/health` is in
no published spec, so it is in no tier and is not counted in the 142; it *is* one of the 12
protocol-only operations, which D9 governs.

**The 12 protocol-only operations (Surface B only) are unclassified until D9 decides.** They are
listed in §1.1 and carry no tier assignment in this document. If D9 picks Surface B, they enter the
tier system; if D9 picks Surface A, they remain out of scope.

**T1 — implement for real (28 operations; wopr has the engine; §7 names the seam):**
`GET /api/info` · `GET /api/event` · `GET /api/session` · `POST /api/session` ·
`GET /api/session/{sessionID}` · `PATCH /api/session/{sessionID}` ·
`DELETE /api/session/{sessionID}` · `GET /api/session/active` ·
`GET /api/session/{sessionID}/message` · `GET /api/session/{sessionID}/message/{messageID}` ·
`POST /api/session/{sessionID}/prompt` · `POST /api/session/{sessionID}/interrupt` ·
`POST /api/session/{sessionID}/model` · `POST /api/session/{sessionID}/agent` ·
`GET /api/fs/list` · `GET /api/fs/read/*` · `GET /api/fs/find` · `GET /api/location` ·
`GET /api/project` · `GET /api/agent` · `GET /api/agent/{agentID}` · `GET /api/model` ·
`GET /api/model/default` · `GET /api/provider` · `GET /api/provider/{providerID}` ·
`GET /api/config` · `GET /api/reference` · `GET /api/skill`.
(`GET /api/command` was in Rev-0's T1 list and is now **T3** — see §5.)

**T2 — stub in the V2 envelope, with a reason (113 operations),** by group:

| Reason | Operations | Ops |
|---|---|---|
| No engine in wopr | `/api/pty*` | 7 |
| No engine in wopr | `/api/worktree*` | 4 |
| No engine in wopr | `/api/vcs*` (incl. `POST /api/vcs/init`, the one 501) | 6 |
| No engine in wopr | `/api/mcp*` | 2 |
| No engine in wopr | `/api/credential*` | 5 |
| No engine in wopr | `/api/integration*` | 11 |
| No engine in wopr | `/api/plugin*` | 3 |
| No engine in wopr | `/api/shell*` | 5 |
| No engine in wopr | `/api/websearch*` | 2 |
| No engine in wopr | `/api/rpc/*/{method}` | 1 |
| No engine in wopr | `/api/debug/location*` | 2 |
| Deliberate — pairing is out of scope: wopr has no device-pairing flow and D3 drops the auth it would need | `POST /api/pair`, `GET /auth/connect/{code}` | 2 |
| Typed-field engine wopr does not have (§6 D5) | `GET /api/form` | 1 |
| Typed-field engine wopr does not have (§6 D5) | `/api/session/{id}/form*` (5) | 5 |
| New persistence subsystem — a durable inbox | `/api/session/{id}/inbox*` | 3 |
| No engine in wopr — no session revert/stash | `/api/session/{id}/revert*` (stage, commit, clear) | 3 |
| Permissions all-open by owner decision (`BACKLOG.md` `serve-7`, which covers **permissions only** — no form or saved-permission mention) | `/api/session/{id}/permission*` | 4 |
| New persistence subsystem — saved permission grants | `/api/permission/saved` (2), `/api/permission/request` (1) | 3 |
| New persistence subsystem — session records on disk | `/api/project/{projectID}` PATCH, `/api/config/shell`, `/api/location/reload` | 3 |
| Experimental line or off the chat path | all `/api/experimental/*` | 29 |
| Off the chat path, no wopr equivalent | `session/{id}/` `fork`, `diff`, `compact`, `context`, `environment`, `generate`, `move`, `shell`, `synthetic`, `view`, `background`, `command` | 12 |
| | **T2 total** | **113** |

`permission/saved` is **2** operations (`GET /api/permission/saved`, `DELETE
/api/permission/saved/{id}`); the prefix `/api/permission` is 3 because `GET
/api/permission/request` is a third and is named here rather than folded into the saved pair.

The reason is the left-hand column of this table, not a `BACKLOG.md` entry: the groups above are
the only filing, and an operation not in a row here is unclassified. Rev-0 claimed the reasons were
already in `BACKLOG.md`; only the permissions row is.

**T3 — namespace conflict (1 operation):** `GET /api/command`. wopr serves it today with a body
`Location.PublicRef` (`additionalProperties:false`) rejects (§5). It is not T1 because the right
shape is an open decision (D7), and it is not a stub because wopr must keep answering it for KKCode.

**Coexistence cost.** `/api/*` carries two response shapes, two SSE encoders, and two DTO sets in
one package. Three mitigations, each carried by a named slice: the single `/api/` catch-all (S2),
the single static V1→V2 event table (§4.4, S4), and V2 DTOs that carry no wopr-only fields (§4.5).

## 3. Scope

### 3.1 Definition of done

**These DoDs are defined against Surface A (the published spec). If D9 chooses Surface B, the T1/T2/T3
partition, the tier counts, and the specific operations in each tier change — see §1.1 and §2.**

1. Every path in §2 is classified T1/T2/T3 in `docs/site/docs/server.md`, and the doc names the
   spec sha256 it was classified against.
2. Every T1 operation answers a spec-valid body **and consumes a spec-valid request**: exact field
   names, every `required` field present (§4.5, §4.6), and no extra fields where the schema sets
   `additionalProperties:false` (201 of 285 schemas do).
3. Every T2 **operation** answers a V2 error envelope — `503` `ServiceUnavailableError` where the
   spec documents 503, the spec's documented status otherwise — never Go's default 404/405.
   Classification is per operation, not per path: `/api/location` hosts one T1 operation
   (`location.get`) and one T2 operation (`location.reload`), and both must behave differently.
   No route answers Go's default 404, 405, or an HTML error page.
4. V1 routes are byte-unchanged; the V1 suite passes unedited.
5. **No non-test file in `internal/codingagent/serve` imports the root `coding` package**, and
   `coding/`, `agent/`, `ai/`, `tui/` are untouched. The gate is greppable and excludes tests
   because three **internal** test files (they declare `package serve`, not `serve_test`) already
   import `coding` today and that is the baseline, not a regression:

   ```bash
   grep -l '"github.com/alexrudloff/wopr/coding"' internal/codingagent/serve/*.go | grep -v _test
   # must print nothing; today it prints nothing across the six non-test files
   ```

   The package *does* import `coding/host` today, in exactly those six non-test files (`serve.go:9`,
   `serve_handlers.go:22`, `serve_prompt.go:13`, `serve_question.go:15`, `serve_read.go:18`,
   `serve_sse.go:17`), and that is the intended seam (`docs/architecture.md:35`); `coding/host`
   re-exports `coding.Services` precisely to keep it. The three baseline test imports are
   `serve_test.go:29`, `serve_read_test.go:13`, `serve_dto_test.go:15`. If a slice needs the root
   package in a non-test file, the seam is wrong.
6. **T3 (`GET /api/command`) answers a spec-valid body**, or D7 has recorded which way it diverges.
   It is one operation and it is live today, so §3.1.2 cannot leave it unbounded; until D7 lands,
   the shape wopr emits is the documented conflict (§5), not a silent extra key.

### 3.2 Why "full" stops here

Rev-1 rejected literal parity because parity here means implementing PTY, worktrees, VCS, MCP,
credential and integration management, plugin RPC, shell-as-a-service, pairing, a durable inbox,
and a form engine — subsystems wopr has no engine for, each of which breaks invariant 3.1.5 above.
Coverage is the part that decides whether a client works against wopr: a client gets parseable
answers everywhere, and real behavior everywhere wopr can honestly provide it.

### 3.3 Explicitly out of scope

Replacing or removing V1 (`/global/*`, `/session*`, `/file*`, `/find/*`, `/project*`, `/config/*`,
`/provider`, `/agent`, `/question*`, `/permission`) — V2 is added alongside. Auth, TLS, CORS,
rate limiting, audit. `POST /api/vcs/init`, the one operation the spec documents as 501. The
`session.next.*` durable event log. Replacing `/api/health`'s existing V1-era meaning (§5).

## 4. Wire facts (verified at the pin)

### 4.1 The success envelope is per-operation

| 200 body shape | Operations |
|---|---|
| `{location, data}` envelope | 40 |
| inline object | 33 |
| bare `$ref` | 13 |
| non-object (`4` array, `2` boolean, `1` `anyOf`) | 7 |
| no 200 JSON body | 49 |
| **total** | **142** |

The 49 are the 46 operations that declare **no 200 at all** — the same set as §4.3's 46 204-only
operations, not an addition to it — plus 3 that declare a 200 whose content is not JSON:

| Operation | 200 content | Note |
|---|---|---|
| `GET /api/event` | `text/event-stream` | §4.4 |
| `GET /api/experimental/session/{sessionID}/log` | `text/event-stream` | the `session.next.*` log |
| `GET /api/fs/read/*` | `application/octet-stream` | T1. Schema is `{"type":"string","format":"binary"}` — **bytes, not a DTO**. No slice may decode it as JSON |

The 13 bare `$ref`s are `ServerInfo`, `PairingCode`, `PairingSession`, `Location.PublicInfo`,
`SessionsResponse`, `SessionGenerateResponse`, `SessionInterruptResponse`,
`SessionMessagesResponse`, `GenerateTextResponse`, `Project`, `Rpc.Output`, `Worktree.List`,
`Worktree.Info`. The 7 non-objects are the four arrays (`GET /api/project`, `/api/debug/location`,
`/api/config`, `/api/config/shell`), the two booleans (`GET /api/pty/{ptyID}/connect`, `GET
/api/experimental/persistent-pty/{ptyID}/connect`), and one `anyOf` (`GET
/api/experimental/migration/v1`). **Read the envelope off the operation, not off a house style.**
A DTO shared across operations cannot assume `{location,data}` — `GET /api/project` and `GET
/api/config` are bare arrays in the same package as enveloped reads.

### 4.2 The error envelope

The spec defines **57** `*Encoded` schemas, of which **33** are `*ErrorEncoded`. All 33 set
`additionalProperties:false`, so no extra field is ever legal. They are **not** a uniform
`{_tag, message}` shape:

- **7 require exactly `[_tag, message]`** and are safe to emit as a bare pair:
  `ConflictError`, `ForbiddenError`, `InvalidCursorError`, `InvalidRequestError`,
  `ServiceUnavailableError`, `UnauthorizedError`, `UnknownError`.
- **26 carry further required fields**, so the pair alone is an invalid response. Required set per
  tag: `AgentNotFound` `+agentID` · `CommandExecution`/`CommandNotFound` `+command` ·
  `FileNotFound` `+path` · `FormAlreadySettled`/`FormInvalidAnswer`/`FormNotFound` `+id` ·
  `InstructionEntryValueTooLarge` `+actualBytes, maxBytes` · `IntegrationAttemptNotFound`
  `+integrationID, attemptID` · `IntegrationMethodNotFound` `+integrationID, methodID` ·
  `IntegrationNotFound` `+integrationID` · `LocationNotFound` `+location` ·
  `McpServerNotFound` `+server` · `MessageNotFound` `+sessionID, messageID` ·
  `PermissionNotFound` `+requestID` · `ProjectNotFound` `+projectID` ·
  `ProviderNotFound` `+providerID` · `PtyNotFound` `+ptyID` · `RpcError`/`RpcInternalError`
  `+type` · `SessionBusy`/`SessionNotFound` `+sessionID` · `ShellNotFound` `+id` ·
  `SkillNotFound` `+skill` · `VcsInitNotSupported` `+providerID`.
- **`WorktreeErrorEncoded` is the exception that breaks the pattern**: it has **no `message`
  property at all** and requires `[_tag, name, data]`, where `data` is an object requiring
  `message` (and an optional `forceRequired`). A helper that emits `{_tag, message}` would violate
  both `required` and `additionalProperties:false`.

Optional fields, all nullable: `resource` (`ConflictError`), `ref` (`UnknownError`), `data`
(`RpcError`, `RpcInternalError`), `kind` and `field` (`InvalidRequestError`), `service`
(`ServiceUnavailableError`).

**The T2 stub shape is exact.** `ServiceUnavailableErrorEncoded` is
`{_tag: enum["ServiceUnavailableError"], message: string, service?: string|null}`, required
`[_tag, message]`, `additionalProperties:false` — so `{"_tag":"ServiceUnavailableError","message":
"<reason>"}` validates. The 503 stub in §3.1.3 and D6 are correct as written.

The six tags wopr's own T1 and catch-all handlers need are `UnauthorizedError`,
`InvalidRequestError`, `SessionNotFoundError`, `FileNotFoundError`, `ServiceUnavailableError`, and
`UnknownError`. **Four** of the six are in the bare-pair set (`UnauthorizedError`,
`InvalidRequestError`, `ServiceUnavailableError`, `UnknownError`); `SessionNotFoundErrorEncoded`
requires `sessionID` and `FileNotFoundErrorEncoded` requires `path`, so for those two the writer
must take the extra field as an argument rather than building a pair.

### 4.3 Status codes, as documented by the spec (Surface A)

| Code | Operations | Note |
|---|---|---|
| 400 | 142 (all) | the only universally documented error |
| 401 | 142 (all) | **with no security scheme anywhere** — see D3 |
| 404 | 121 | |
| 503 | 25 | `ServiceUnavailableErrorEncoded` exists → **the T2 stub is spec-blessed** |
| 200 / 204 | 96 / 46 | |
| 409, 500, 403, 413 | 13, 8, 4, 1 | |
| 501 | 1 (`POST /api/vcs/init`) | |

**Surface B differs:** the protocol surface documents 200 on `GET /api/health` (not in Surface A), and its 58 endpoints have their own status-code sets not fully captured here. See D9.

### 4.4 Events

`GET /api/event` is SSE, and **the two surfaces frame the frame differently**, so the frame's own
shape is itself part of D9:

| | Surface A (published) | Surface B (protocol) |
|---|---|---|
| frame object | `{id?, event, data}`, `required: [event, data]`, `additionalProperties:false` | `{id?, metadata?, durable?, location?, type, data}`, `required: [id, type, data]`, `additionalProperties:false` — `packages/protocol/src/groups/event.ts:8-27` |
| name field | `event: {type: string}` — **no enum, no payload schema** | `type`, a literal from the event manifest |
| `data` | a JSON **string** — `V2EventEncoded` is `{"type":"string","contentMediaType":"application/json"}` | a **structured object**, one arm per event |

On Surface A the spec's own shape is double-encoded: serialize the event object and emit the
resulting JSON as the frame's `data` string. Rev-1's D4 recommendation (a) is correct **for Surface
A** and is now verified rather than assumed; it is wrong for Surface B, where `data` is the object
itself and there is no `event` field at all.

**The stream's failure channel is not the error envelope.** `x-effect-stream` (nested under
`responses.200.content["text/event-stream"]`, not at the operation root) declares
`causeSchema` as an array whose items are a three-arm union:

| Arm | Required | Note |
|---|---|---|
| `{_tag: "Fail", error: {}}` | `[_tag, error]` | `error`'s schema is literally `{"not":{}}` — **no schema**, so the arm deliberately carries nothing a decoder can validate |
| `{_tag: "Die", defect: {}}` | `[_tag, defect]` | `defect` is untyped |
| `{_tag: "Interrupt", fiberId: number\|null}` | `[_tag, fiberId]` | the third arm; a stream interrupted rather than failed |

alongside `errorSchema: {"not":{}}` and `failureEvent: "effect/http-api/stream/failure"`. **Do not
route the T2 stub through this channel**: the spec gives it no shape to conform to, and reusing
`{_tag, message}` there would be an invented contract, not a divergent one.

**V1 → V2 event mapping.** The bus carries `Event{Type, Session, Data}` and marshals `Data` once
(`coding/host/bus.go:16-21`, `:52`); wopr emits **14** V1 event-name constants at
`serve_sse.go:41-54`, of which **12 are bus-resident** — `server.connected` and `server.heartbeat`
are written straight to the response stream (`serve_sse.go:206`, `:225`) and never published, so no
bus event exists for the V2 encoder to map and they get their own rows. The V2 encoder subscribes
exactly like `globalEvent` and looks up `(v1Name, v1Payload)` in one static table:

| V1 bus event (wopr source) | V2 event name | Work |
|---|---|---|
| `message.updated` | `message.updated` | reshape `properties.info` into the V2 `Session.Message.Info` union (§4.5) |
| `message.part.updated` | `message.part.updated` | **the highest-probability decode failure — see §9** |
| `message.part.delta` | `message.part.delta` | properties identical today. **Not in `ServerDefinitions`** — see the note under the table |
| `session.updated` | `session.updated` | `info` becomes V2 `Session.Info` |
| `session.status` | `session.status` | V2 wraps as `status:{type}`; V1 is flat `type`. **Not in `ServerDefinitions`** |
| `session.error` | `session.error` | V2 error object is a tagged union; reshape. **Not in `ServerDefinitions`** |
| `todo.updated` | `todo.updated` | the arm's payload requires `content` |
| `session.diff` | `session.diff` | emitted today; **not in `ServerDefinitions`** |
| `question.asked` / `.replied` / `.rejected` | **D5 open** — Rev-1 proposed `question.v2.asked` / `.replied` / `.rejected` | rename + reshape, **in the V2 encoder only** (§6 D5). The protocol source declares **both** vocabularies, so the rename is optional rather than required — that weakens D5's stated reason |
| `session.idle` (`serve_sse.go:50`, emitted `:765`) | `session.idle` | **already a V1 bus event**, not new: `sseSessionIdle{SessionID}` at `:151-153`. Rev-1's "derivable from `session.status`" was wrong. **Not in `ServerDefinitions`** |
| *no bus event* — `sseWrite` only (`serve_sse.go:206`) | `server.connected` | already the stream's first frame; the V2 encoder emits its own because the V2 frame shape differs (D9) |
| *no bus event* — `sseWrite` only (`serve_sse.go:225`) | *(no sourced name — see below)* | emitted when the subscription goes idle. `server.heartbeat` is wopr's own V1 keep-alive name and appears in **neither** surface's event union — it is not one of the 88 below, and no pinned artifact declares a heartbeat event — so the V2 frame has **no sourced `type`** for it. Flagged unverified; this is part of D9, not a name wopr may assert |
| — | `session.created`, `session.deleted`, `permission.v2.asked` / `.replied` | **new**, only if a trigger exists |
| — | `session.next.*` (32 names), `file.edited`, `pty.created` / `.updated` / `.exited` / `.deleted`, `worktree.ready` / `.failed` | **new, not emitted** — `session.next.*` is the `/api/experimental/session/{id}/log` stream, a different subsystem |

**Where the V2 event names come from, and the limit of that source.** Not from the published JSON:
that file contains **zero** literal occurrences of any name in the table, and its `/api/event`
declares `event: {type: string}` with no enum. They come from **Surface B** —
`packages/sdk/openapi.json` at `aec0b9a`, whose `V2Event` schema is an **88-arm `anyOf`**, one arm
per event, every arm `required: ["id","type","data"]` with `additionalProperties:false` and a
literal `type` enum. Every name in the table above is one of those 88 — **except `server.heartbeat`,
which is in neither union and is flagged in its row** — and `session.next.*` is 32 of them.

**The two pinned artifacts declare different unions, and this document does not resolve it.**
`packages/protocol/src/groups/event.ts:52` builds the server's `/api/event` group from
`EventManifest.ServerDefinitions`, which `packages/schema/src/event-manifest.ts:57-61` composes from
the foundation, feature and `SessionTodo` groups only. Read against that composition:

- `SessionStatusEvent`, which defines `session.status` and `session.idle`
  (`packages/schema/src/session-status-event.ts:45`), appears in the full `Definitions` at `:75` and
  **not** in `ServerDefinitions`.
- `SessionV1.Event.Definitions` splits its 10 events into 7 durable and 3 live
  (`v1/session.ts:502-507`, `:664-675`); only the durable half feeds `ServerDefinitions`, so
  `message.part.delta`, `session.diff` and `session.error` are in `Definitions` and **not** in
  `ServerDefinitions`.
- `WorktreeEvent` and `ServerEvent` are likewise excluded. `server.connected` is nonetheless in the
  protocol union, synthesized as an empty-data arm by `event.ts:18-26`.

The manifest is TypeScript and **was not executed** — `opencode-src` has no `node_modules`, and
installing a monorepo to evaluate it is not a cost this plan pays. The per-name membership of
`ServerDefinitions` above is therefore *read off the composition, not observed*, and it is the
strongest statement available without running the tree. What is not established at all: which union
a client expects. That is part of **D9**; the table above is sourced from the generated artifact and
says so.

**One bus, one new encoder** (`serve_v2_sse.go`). Rejected alternative: teach `eventMapper` to emit
both shapes. The bus stores only `(name, payloadBytes)` (`bus.go:98-101`), so a second shape
doubles every subscriber's queued bytes and couples two wire vocabularies in one producer — which
is what lets §7 say "one producer" without qualification. The `session.next.*` log is not emitted.

**The `question.v2.*` rename belongs in the V2 encoder only.** Renaming the bus constants at
`serve_sse.go:52-54` would break KKCode, which consumes the V1 names on `/global/event`.

### 4.5 DTO burden — every T1 response schema, with its required set (Surface A)

201 of 285 schemas set `additionalProperties:false`, so every placeholder below is a client-visible
failure if guessed wrong. "Placeholder" means a value wopr cannot source and must synthesize.

| Schema | Required | Where wopr's value comes from |
|---|---|---|
| `Session.Info` (16 props) | `id, projectID, cost, tokens, time, location` | `buildSessionDTO`; `location`→`Location.PublicRef{directory}` from `Services.CWD()` |
| `Session.Info.time` (nested) | `created, updated` | from the session record's timestamps; `idle`, `viewed`, `archived` are optional |
| `Session.Info.tokens` → `TokenUsage.Info` | `input, output, reasoning, cache` | `input`/`output` from the transcript; **wopr has no reasoning-token count → placeholder `0`** |
| `TokenUsage.Info.cache` (nested) | `read, write` | **placeholder `0`/`0`** — a required object inside a required object; omitting it fails `Session.Info` |
| `Session.Info.cost` → `Money.USD` | (`{"type":"number"}` — **a bare number, not an object**) | the transcript's USD total, else `0`. Emitting `{amount, currency}` fails `Session.Info` outright |

**Surface B note:** the protocol surface's SDK (`packages/sdk/openapi.json`) has a different schema set (472 schemas). The T1 set under Surface B would differ — notably `/api/health` becomes T1 and `/api/info` is absent. See D9.
| `Model.Info` (18 props) | `id, modelID, providerID, name, capabilities, variants, time, cost, status, enabled, limit` | `AvailableModels`. **`variants` → `[]`**, **`status` → `"active"`** (no wopr source). Largest DTO in the slice |
| `Model.Capabilities` (nested) | `tools, input, output` | `tools` from the tool set; `input`/`output` are string arrays → `[]` if unknown |
| `Model.Info.time` / `.limit` (nested) | `released` / `context, output` | `released` → `0`; `limit.context`/`limit.output` from the model database |
| `Agent.Info` | `id, name, request, mode, hidden, permissions` | hardcoded agent list. `id`/`name` from the list; **`mode` → `"primary"`**, **`hidden` → `false`**, `permissions` → `[]`; `request` → `Provider.Request{settings: {}, headers: {}, body: {}}` — all three required fields present as empty objects to satisfy `additionalProperties:false` |
| `SessionsResponse` | `data, cursor` | `cursor` is required and is `{previous, next}`, each `SessionsCursor` (an opaque string) **or `null`** — emit `{"previous":null,"next":null}` when unpaginated, never omit it |
| `SessionMessagesResponse` | `data, cursor` | same shape; `cursor` here is `{previous, next}` of plain opaque strings or `null` |
| `SessionActive` | `type` | **the 200 body is not this schema.** `GET /api/session/active` declares `{data: {patternProperties: {"^ses": {$ref: SessionActive}}, additionalProperties: false}}`, `required: ["data"]` — a **map keyed by session id**, one `SessionActive` per live session. `SessionActive` itself is the `{type:"running"}` value; emitting it bare matches no pattern *and* violates `additionalProperties:false`. T1, in S3 |
| `Session.Message.Info` | **no single required set** — an 11-arm `anyOf` over `AgentSelected`, `ModelSelected`, `LocationSwitched`, `User`, `Synthetic`, `System`, `Skill`, `Shell`, `Assistant`, `Compaction`, `Idle` | each arm carries its own required set; a message must match exactly one arm. S4's decode gate (§8) is what catches a mis-typed arm |
| `SessionInterruptResponse` | `interrupted` | from `Session.Abort`; `false` for a known-but-idle session |
| `Session.Inbox.User` | `id, sessionID, time, type, payload, delivery` | **this is the prompt route's 200 body** — `POST /api/session/{id}/prompt` declares `{data: Session.Inbox.User}`, `required:["data"]` — so S4 must emit it unconditionally, not behind a decision. Two nested required sets: `time` requires `created`, and `payload` (`Session.Inbox.UserPayload`) requires `text`. The **required** `delivery` member is `"steer" \| "queue"`. Wopr has no durable inbox, so this is reachable only through the prompt path (S4); it is **not** gated on D5, which is about forms, not the inbox |
| `Provider.Info` | `id, name, activation, package` | `listProviders`; **`activation` and `package` have no wopr source → placeholders** (`activation` from `AuthenticatedProviders`, `package` a fixed literal) |
| `Reference.Info` | `name, path, source` | context files under the agent dir; `source` → `"agent"` (the only value wopr produces; the enum includes `"skill"`, `"mcp"`, `"user"` which are D9-dependent) |
| `Skill.Info` | `id, name, path, content` | the skills loader; `content` is the file body |
| `Config.Entry` | **no single required set** — a 2-arm `anyOf` over `Config.DocumentEncoded` and `Config.DirectoryEncoded` | `GET /api/config` returns a bare **array** of these (§4.1) |
| `Location.PublicInfo` | `directory, project` | `Services.CWD()` + `projectID`. **This is `GET /api/location`'s response** (a bare `$ref`), not `/api/command`'s — that one is `Location.PublicRef`, required `[directory]` only, `additionalProperties:false`. wopr's `apiCommandDTO` emits `commandLocationDTO{Directory, Project}` (`serve_dto.go:322-330`), i.e. a `PublicInfo` shape where the spec wants a `PublicRef`; **that extra key is the T3 conflict (§5)**, and it belongs to §5, not here |
| `Project` | `id, canonical, time, sandboxes` | `GET /api/project` returns a bare array of these; `sandboxes` is required and wopr has none → `[]` |
| `FileSystem.Entry` | `path, type` | thin — `{path, type}` |
| `ServerInfo` | `version, pid, urls, paths` | `coding/version.Version`, `os.Getpid`, listener address. **`paths` requires `tmp`** (required `["tmp"]`, `additionalProperties:false`) — S2 emits it or `ServerInfo` fails. `capabilities.persistentPty=false` |

### 4.6 T1 request bodies and query parameters (Surface A)

§3.1.2 requires T1 operations to *consume* spec-valid requests as well as emit them. No parameter is
`required` except where noted. **Most** optional parameters are nullable, but not all — `fs.find`'s
`type` is a plain `enum ["file","directory"]` with no null arm, and in the prompt body `files`,
`agents`, `skills` and `metadata` are non-nullable. A client may send `null` for the ones that say
`anyOf: [{…}, {type: "null"}]` and only for those.

**Surface B note:** the protocol surface's 58 endpoints have different request shapes (e.g., `GET /api/health` has no params, `GET /api/question/request` has different query params). The T1 set under Surface B would differ. See D9.

| Operation | Body (`required: true` where marked) | Query |
|---|---|---|
| `GET /api/session` (`session.list`) | — | `limit`, `order` (`asc`/`desc`), `search`, `parentID`, `directory`, `project`, `subpath`, `cursor` — **all 8 must be honored or explicitly defaulted (D10)**; `SessionsResponse.cursor` is required in the response, so an ignored `cursor` still needs a well-formed `{"previous":null,"next":null}` |
| `POST /api/session` (`session.create`) | **`required`**; inline object, `additionalProperties:false`, **no `required` members**: `id`, `parentID`, `title`, `agent`, `model`, `location`, `metadata`, `permissions`, each nullable. An empty `{}` is valid; `Host.Start` requires a model, so a body with no `model` must take the server default | none |
| `PATCH /api/session/{sessionID}` | **`required`**; inline, `additionalProperties:false`, no required members: `title`, `metadata`, `permissions`. Every field is nullable, so `null` means "clear", which must not be dropped as a no-op | `sessionID` (required) |
| `POST /api/session/{sessionID}/prompt` | **`required`**; inline, `additionalProperties:false`, **`required: ["text"]`**: `text`, `id`, `files`, `agents`, `skills`, `metadata`, `delivery` (`"steer"\|"queue"`), `resume`. Only `text` is mandatory — the other seven are the optional surface Rev-0 left unspecified | `sessionID` (required) |
| `POST .../model` (`session.switchModel`) | **`required`**; `{"model": Model.Ref}`, `required: ["model"]`, `additionalProperties:false` | `sessionID` (required) |
| `POST .../agent` (`session.switchAgent`) | **`required`**; `{"agent": Agent.ID}`, `required: ["agent"]`, `additionalProperties:false` | `sessionID` (required) |
| `POST .../interrupt` | — | `sessionID` (required), `resume` (`"true"`/`"false"` string) |
| `GET .../message` | — | `sessionID` (required), `limit`, `order`, `cursor`, `type` — **the enum is ten values, each with a trailing `d`**: `agent-switched`, `model-switched`, `location-switched`, `user`, `synthetic`, `system`, `skill`, `shell`, `assistant`, `compaction` (not `agent-switch`/`model-switch`/`location-switch`). **A filter wopr has no equivalent for; D10** |
| `GET .../message/{messageID}` | — | `sessionID`, `messageID` (both required) |
| `GET /api/project` | — | `archived` (`enum ["true","false"]`, not required, nullable). T1, and §4.6 must cover it — see §4.5 for the array body |
| `GET /api/fs/find` | — | **`query` (required)**, `location`, `type` (`file`/`directory`, **not nullable**), `limit` |
| `GET /api/fs/list`, `GET /api/fs/read/*` | — | `location`, plus `path` on `fs.list` |
| `GET /api/agent`, `/api/agent/{agentID}`, `/api/model`, `/api/model/default`, `/api/provider`, `/api/provider/{providerID}`, `/api/config`, `/api/reference`, `/api/skill`, `/api/location` | — | `location` on all ten; `agentID`/`providerID` required on the two by-id reads. **`location` is the scope resolver wopr must honor on every one of them, not just `fs`** |

## 5. Namespace conflicts (T3)

V2 owns `/api/*`. wopr already serves two routes there, both added for KKCode during V1 work. One
is in the published spec and is T3's single operation; the other is in no **published** spec — which
is not the same as "in no spec" (§1.1).

- **`GET /api/command`** (`internal/codingagent/serve/serve.go:96`, handler `serve_handlers.go:125`, DTO `serve_dto.go:322-330`)
  is the one live schema violation and the **T3 operation**. V2 declares its 200 as
  `{location: Location.PublicRef, data: Command.Info[]}` where `Location.PublicRef` is
  `{directory}` with `additionalProperties:false`. wopr emits `commandLocationDTO{Directory,
  Project}` — an extra key in a closed schema — and an empty `data` array. A strict V2 decoder
  rejects the response.
- **`GET /api/health`** (`internal/codingagent/serve/serve.go:65`, handler `serve_handlers.go:95`) returns `{"healthy":true}`.
  Per surface: it is **absent from the published spec** (so it is not one of the 142 and no tier
  counts it), and it is **present in the protocol surface** — `health.get` on `GET /api/health`
  with `success: Schema.Struct({ healthy: Schema.Literal(true) })`
  (`packages/protocol/src/groups/health.ts`), a body byte-identical to what wopr already serves.
  It is one of the 12 protocol-only operations (§1.1), so under the protocol surface it is
  **already conformant today** and needs no change at all. Recorded here so nobody "fixes" it into
  a 404 later, so S2 does not fold it into the V2 catch-all and strand it, and so D9 is not
  answered by accident: if D9 picks Surface B, this route is conformant, and §3.1.6 does not apply
  to it.

  **D7 (open):** options — (a) drop `location.project` and accept that KKCode reads a smaller
  object (its use of the route is a connect-time probe, `BACKLOG.md` `serve-kkcode-v1-compat`);
  (b) keep `project` and record a divergence; (c) serve `/api/command` from a separate V1
  handler and register the V2 shape last. **Recommend (b) over (a), for now.** Rev-1 recommended (a)
  on the grounds that it "deletes a key no client has been shown to need", and that ground does not
  hold: `BACKLOG.md` `serve-kkcode-v1-compat` records a capture from a real opencode 1.18.34 server
  returning `/api/command` as `{location:{directory,project},data:[{name,…}]}` — the reference
  server KKCode talks to **does** carry `project`. Dropping the key would be a deliberate
  divergence from the thing the client was built against, on the strength of a route its use of
  which is recorded as "a connect-time probe". If the owner still prefers (a), it needs a KKCode
  capture showing the client does not read `project`, not the absence of one showing it does.

## 6. Decisions

| # | Question | Status |
|---|---|---|
| D1 | Capture KKCode's real V2 traffic | **Open, lower priority — does not block any slice.** Options: (a) defer until a V2 client appears; (b) set up a logging proxy now against a real opencode 1.18.34 server. **Recommend (a)** — the spec is pinned, S1 classifies all 142 operations from it, and no slice guesses the route set. The residual risk is client-side (framing, event expectations) and no gate in this plan reduces it. S4's live-curl gate proves wopr's output bytes, not a client's decoder. The V1 capture (`serve-kkcode-v1-compat`) answered the V1 handshake completely. Revisit if a V2 client appears. |
| D2 | `/api/status` | **Closed — do not implement.** Absent from the published spec at the pin; KKCode only probes it as a fallback when `/api/health` fails, and opencode answers it with web-UI HTML. |
| D3 | Auth | **Decided 2026-10-07 by owner:** ignore `Authorization`, never emit 401, keep every tool open. The spec's universal 401 with no security scheme is a recorded divergence (`BACKLOG.md`, `serve-kkcode-v1-compat` notes). The optional `server.authToken` idea is dropped from this plan. |
| D4 | Event `data` shape | **Closed for Surface A, reopened by D9 for Surface B.** On the published spec: emit literally — `data` is a JSON string, and the frame is `{id?, event, data}` (§4.4). The protocol surface declares a different frame with no `event` field and an *object* `data` (`packages/protocol/src/groups/event.ts:8-27`), so "the spec's shape" is no longer a single thing. Follows D9; the encoder is one file either way. |
| D5 | Forms ↔ `askuser` | **Open, deferred.** Options: (a) T2 stub for all 6 form operations and let questions flow over the existing `question.asked` wire name; (b) T2 stub for forms **and** rename to `question.v2.asked/replied/rejected` in the V2 encoder, so a V2 client sees a V2 vocabulary; (c) build the typed-field engine now. **Recommend (b)** — but **weaker than Rev-1 argued it**, because the premise it rested on is gone: Rev-1 claimed a V2 client "cannot correlate a question" without the rename. The protocol source declares **both** `question.v2.asked/replied/rejected` and `question.asked/replied/rejected` (§4.4), so under that surface a V1-vocabulary frame is a name the contract knows. (a) is defensible on that basis; it is not defensible under Surface A, whose `/api/event` names no events at all. (c) remains the §3.2 subsystem. Under either (a) or (b) the rename is **encoder-local**: the bus constants at `serve_sse.go:52-54` keep the V1 names KKCode consumes. Revisit if a client drives questions as forms, or if D9 picks Surface A, where nothing constrains the name. |
| D6 | Stub status | **Closed by the spec: every T2 answers a V2 error envelope — `503` `ServiceUnavailableError` where the spec documents 503, the spec's documented status otherwise** (§4.3, §3.1.3). `POST /api/vcs/init` is **not** an exception and Rev-1's clause "which wopr does not serve anyway" was wrong on two counts: it *is* classified — §2's `vcs` row, T2 — and every T2 operation answers the envelope per §3.1.3 and S6's gate. The spec documents 501 for that one operation; wopr answers 503 with the `ServiceUnavailableError` reason "no engine in wopr". The recorded divergence is *one status code on one operation*, stated here rather than implied. **Whether any T2 resource must answer a 200-empty body instead of `503` because a client polls it is not established:** no client poll of a T2 route is recorded in this plan, so §3.1.3's rule stands for all 113. A client shown to poll one would be a new finding against §3.1.3, not an exception to D6. |
| D7 | `/api/command` shape | **Open** — §5. |
| D8 | V2 identifier format | **Open, low priority.** Options: (a) keep wopr's UUIDs (server is conformant — wopr never validates its own ids); (b) mint `^ses`/`^msg_` pattern ids for V2 routes only. **Recommend (a)** — the spec declares `^ses` and `^msg_` patterns but wopr mints UUIDs; since wopr never validates its own ids, a server is conformant either way. Revisit only if a strict client rejects an id. |
| D9 | **Which V2 surface is the specification for `wopr serve`?** | **OPEN — owner decision. This plan does not decide it.** §1.1 establishes two: the published `opencode.ai/v2/openapi.json` (142 operations) and `packages/protocol/src/groups/` at `aec0b9a` (58 endpoints, 46 shared, 12 protocol-only, 96 published-only). The answer is not cosmetic — it changes what T1 *is* (`/api/health` is already conformant under Surface B and unclassified under Surface A; 96 operations exist only under A and 12 only under B), it changes the SSE frame shape (§4.4, D4), and it decides whether D5's rename is required or optional. **Recommend: specify against both.** Treat the 46 shared endpoints as the floor every T1 operation must satisfy; classify each of the 12 protocol-only and 96 published-only operations explicitly with the surface named in the reason column; and record which client is expected to speak which. Until it is decided, this document names the surface beside every claim that differs between them, and §2's tiers are published-spec (Surface A) tiers. |
| D10 | T1 request members wopr cannot honor: ignore or reject? | **Open, low priority.** Two instances, one question. (a) `GET /api/session`'s 8 optional query parameters — wopr has no filter for most; "honored or explicitly defaulted" is satisfiable by ignoring, which is silent. (b) `GET /api/session/{id}/message`'s `type` filter — the ten-value enum (§4.6) has no wopr equivalent; rejecting is spec-valid but breaks a client that filters. **Recommend: ignore, and log.** Rejecting is a louder failure than a wrong-but-decodable result, and §3.2's full-coverage stance is about answering, not about refusing. Both instances land in S3/S5, so neither blocks S1 or S2. |

## 7. Capability mapping (citations re-derived 2026-10-15 at `3421839`)

| V2 operation(s) | wopr seam | Note |
|---|---|---|
| `GET /api/info` | `coding/version.Version`, `os.Getpid`, listener address | needs `version`, `pid`, `urls`, `paths`; `capabilities.persistentPty=false` |
| `GET /api/event` | `Host.Subscribe` `coding/host/host.go:139`, `forwardEvents` `coding/host/host.go:159`, `Bus.Publish` `coding/host/bus.go:52` | one producer; the V2 encoder re-encodes V1 bus events (§4.4). Note only **12** of wopr's 14 event constants are bus-resident; `server.connected`/`server.heartbeat` are written straight to the stream |
| `GET /api/session` | `listSessionsFiltered` `serve_handlers.go:154`, `buildSessionDTO` `:987` | new `Session.Info` DTO (§4.5) |
| `GET /api/session/active` | **no seam exists.** `Host.Sessions()` (`coding/host/host.go:78`) returns *every* live session and `listSessionsFiltered` returns every session on disk; neither yields "the active session". S5 must define the rule (most-recently-updated live session is the defensible choice) before this route can be implemented — and the body is the keyed map of §4.5, not one `SessionActive` | gap, not a mapping |
| `POST /api/session` | `Host.Start` `coding/host/host.go:98`; `getSessionRecord` `serve_read.go:322` | |
| `GET`/`PATCH`/`DELETE /api/session/{sessionID}` | `getSession` `:297`, `patchSession` `:318`, `deleteSession` `:407`; record load via `getSessionRecord` `serve_read.go:322`; DTO via `buildSessionDTO` `:987` | delete is **already decided** — `BACKLOG.md` `serve-delete-hard-delete` (READY) supersedes the 2026-10-08 archive-on-delete decision and requires hard delete; S3 depends on that item, so it is a named dependency, not an open choice |
| `GET .../message`, `.../message/{messageID}` | `sessionMessages` `serve_handlers.go:919`, `sessionEntries` `:955`, `entriesToMessages` `:1142` | cursor semantics are V2-only (§4.6) |
| `POST .../prompt` | `Session.PromptContent` `coding/host/host.go:314`, `PromptContentWait` `coding/host/host.go:379` | per-prompt `system` staging already exists: `serve_prompt.go:104,161`. The 200 is `{data: Session.Inbox.User}` (§4.5), required unconditionally |
| `POST .../interrupt` | `Session.Abort` `coding/host/host.go:450` | |
| `POST .../model`, `.../agent` | `applyModel` / `SetModel` in `serve_prompt.go` | 204 in V2 |
| `GET /api/fs/list`, `/api/fs/read/*`, `/api/fs/find` | `resolveInRoot` `serve_dto.go:380`, `os.OpenInRoot` `serve_handlers.go:656,787`, walk `findFiles` `:838` | `FileSystem.Entry` is `{path,type}` — thin |
| `GET /api/location`, `GET /api/project` | `Services.CWD()`, `projectID`, `EvalSymlinks`; `project()` `serve_handlers.go:429` | `Location.PublicRef` is `{directory}` |
| `GET /api/agent` | `listAgents` `serve_handlers.go:532` | V2 requires `id, name, request, mode, hidden, permissions` |
| `GET /api/agent/{agentID}` | **no seam exists** — wopr serves only the list handler. S5 must add the by-id read | gap, not a mapping |
| `GET /api/model`, `/api/model/default` | `AvailableModels` | heaviest DTO in the slice (§4.5) |
| `GET /api/provider` | `listProviders` `serve_handlers.go:458` | **hazard, not a mapping — see below** |
| `GET /api/provider/{providerID}` | **no seam exists** — wopr serves only the list handler. S5 must add the by-id read | gap, not a mapping |
| `GET /api/config` | `getConfig` `serve_handlers.go:100` | bare array of `Config.Entry` |
| `GET /api/reference`, `/api/skill` | `internal/codingagent` skills + context files | |
| `GET /api/command` (T3) | `apiCommand` `serve_handlers.go:125` | see D7 |

**Hazard — `listProviders` is non-deterministic in its default.** `serve_handlers.go:459-463` sets
`current` from `liveSessions[0].Inner().Model()`, and `Host.Sessions()` (`coding/host/host.go:78-86`)
ranges `h.registry` — a map — with **no sort**, so with two or more live sessions the "default" model
is arbitrary and can differ between two identical requests. This is a **live V1 bug today**, not a
V2 one: `listProviders` is mounted at **`GET /config/providers`** (`serve.go:93`) and answers it
today. Not at `GET /provider` — that is `listProviderNames` (`serve.go:99`, handler
`serve_handlers.go:519`), a different handler that sorts its names and has no session dependency;
and `/global/*` holds only `/global/health` and `/global/event`. S5 must not carry this into
`GET /api/model/default`; either sort the snapshot or derive the default from the requested
`location`.

Shared wiring: V1 mux `internal/codingagent/serve/serve.go:61-116` (routes registered `63-116`);
`serve.New` `internal/codingagent/serve/serve.go:41` (not `cmd/wopr/serve.go:41`, which is a flag
table); the call site `cmd/wopr/serve.go:192`; `writeJSON` `serve_handlers.go:39`; `notImplemented`
`serve_handlers.go:80`; `ServerSettings` `internal/codingagent/settings.go:180`.
V1 SSE and question seams the V2 encoder sits beside: `serve_sse.go:41-54` (the 14 event-name
constants, 12 of them bus-resident), `serve_sse.go:52-54` (question constants), `serve_question.go`
(`questionDTO`, ask, reply).

## 8. Slices

Each is one feature, gates green, independently committable. Run in `.worktrees/wave-serve-v2-*`.
**Common gates, every slice:** `go build ./...`, `go vet ./...`, `go test -count=1` on touched
packages, `go tool golangci-lint run` on touched packages, `go fix -diff ./...` empty, and before
merge `make check`. Any test added must be **hermetic** (`AGENTS.md`): point `WOPR_HOME` at a temp
directory and clear every provider credential variable — this package's existing harness is
`TestMain` at `internal/codingagent/serve/serve_test.go:39`, which already does both, and new tests must live under
it.

### S1 — inventory classifier

- **Files:** a generator script, new at `automation/gen/opencode-v2-inventory` (the V1 plan's
  `automation/gen-opencode-contract.py` is **named there but does not exist anywhere on the system**
  — `automation/` here holds `ci/ dev/ gen/ internal/ make/ release/`. S1 builds it; do not cite it as
  existing), `internal/codingagent/serve/testdata/opencode-v2-inventory.json`
  (`path`, `method`, `tier`, `reason`), `docs/site/docs/server.md`.
- **Behavior:** generate the inventory from the fetched spec; abort if the resolved operation count
  is below the raw `operationId` count; record the spec sha256 in `server.md`.
- **Also produces what S3-S5's decode gates need, which nothing else does.** Those gates say
  "round-trip decode against the pinned schemas", and today that is impossible: §1 forbids
  vendoring the JSON, `internal/codingagent/serve/testdata/` does not exist, S1 produced only the
  inventory, and `AGENTS.md` forbids network in tests. So S1 emits, beside the inventory,
  **`testdata/opencode-v2-schemas.json` — the subset of the 142 operations' request and response
  schemas that S3, S4 and S5 decode against, serialized JSON Schema, expected well under 200 KB**
  because it is only the schemas those DTOs touch, not all 285. **The schemas file's sha256 is not
  pinned** — JSON serialization is not byte-deterministic without a fixed draft/$ref/ordering/indent/inclusion
  rule, and this plan does not impose one. Only the inventory's sha256 is pinned. If S1's measured
  subset exceeds 200 KB,
  S1 stops and says so — the alternative is that S3-S5 ship with an unmeetable gate.
- **Gate:** no test over the committed JSON. The V1 plan ruled against exactly this artifact in
  writing — "a test over a committed JSON file turns red when you edit the JSON and stays green
  when you delete a route … the inventory is **a generated document, not a gate**" (`2026-10-09`
  §1, and S1's "No new test in S1") — and `AGENTS.md:53-56` bans the change-detector category
  outright. A count-and-uniqueness assertion is no better: a hand-written tier table passes it
  whatever it says, which is how Rev-0's `30 + 112` survived. The gate that can fail is a
  **review gate, and it is the §2 arithmetic itself**: S1 is done when T1 ∪ T2 ∪ T3 equals the
  spec's operation set, in both directions, with the three counts summing to 142 — and §1's
  `curl | sha256sum` re-derivation is what invalidates it when the spec moves.
- **Relationship to S2:** S1 produces the artifact S2 registers routes against; S2 is the first
  slice whose behavior depends on it. S1 alone merges green with no route change. The V1 plan
  established this ordering deliberately (S1 = generated document + pointer, S2 = behavior).

### S2 — handshake and envelope

- **Files:** `serve.go`, `serve_v2.go`, `serve_v2_dto.go`, `serve_v2_test.go`.
- **Behavior:** `/api/*` catch-all returning the 503 envelope, the error writer covering the six
  tags of §4.2, and `GET /api/info`. `/api/health` stays registered and keeps answering
  `{"healthy":true}` (§5). **The mechanism is Go 1.22+ `ServeMux` pattern specificity** — a
  `mux.HandleFunc("/api/", …)` catch-all loses to every registered `GET /api/…` pattern, so
  registered routes win and only unhandled paths *and* methods fall through to 503. This is what
  makes "no route answers Go's default 404 or 405" achievable with one handler instead of 142
  registrations.
- **Gate:** loopback tests assert the two handshake bodies with the spec field names, that
  `GET /api/nope` and `POST /api/pty` return the envelope rather than Go's 404/405, that
  `GET /api/health` still answers 200 with `{"healthy":true}`, and that
  `ServiceUnavailableErrorEncoded` round-trips. **Plus a round-trip decode of `ServerInfo`**
  (including `paths.tmp`) against `testdata/opencode-v2-schemas.json`, since `GET /api/info` is
  registered in S2. `GET /api/fs/read/*` is registered in S5; at S2 the catch-all returns 503, so it
  is not tested here.
- **DoD:** a `curl` to `/api/info` and to `/api/health` shows 200 with the spec field names, and a
  curl to an unknown `/api/*` path shows the 503 envelope. **Two** handshake routes, not three:
  `/api/command` is T3 and keeps its V1 body until D7 decides, which S5 owns — a "three routes"
  DoD here asserts a shape a later slice is going to change.

### S3 — session read and lifecycle

- **Files:** `serve_v2.go`, `serve_v2_dto.go`, tests.
- **Behavior:** list/create/get/patch/delete/active plus the `Session.Info` DTO and the
  `SessionsResponse` cursor (§4.5, §4.6). Depends on `BACKLOG.md` `serve-delete-hard-delete`
  (READY, unmerged) for hard delete.
- **Gate:** round-trip decode tests for `Session.Info` and `SessionsResponse` against
  `testdata/opencode-v2-schemas.json` (S1's committed subset, §8 S1), including the nested
  `tokens.cache.read`/`write` placeholder and a bare-number `cost`. `GET /api/session/active`'s
  keyed-map body (§4.5) decodes in this slice too — it is T1 and nothing else covers it.
- **DoD:** a disk-only session (server restarted) is listed and readable exactly as in V1, and
  `cursor` is present on every list response.

### S4 — prompt loop

- **Files:** `serve_v2_sse.go`, `serve_v2.go`, `serve_v2_dto.go`, tests.
- **Behavior:** the `/api/event` V2 encoder over the §4.4 table, plus prompt/interrupt and message
  read. Adds the **manual live-curl gate**: this package's suite is historically blind to wire
  defects (five escapes recorded in `BACKLOG.md` serve-3/serve-4), so curl against a real listener
  is load-bearing.
- **Gate:** a `go test` that **decodes a representative payload against
  `testdata/opencode-v2-schemas.json` for every V2 DTO this slice emits** — `Session.Message.Info`
  (all 11 arms), `Session.Inbox.User` (including `time.created` and `payload.text`), the
  `{data: Session.Inbox.User}` prompt 200, `SessionInterruptResponse` — plus the live-curl gate.
  The `message.part.*` frame decode is **not required here** — Surface A's `/api/event` declares no
  payload schema (`event: {type: string}`, `data` a JSON string), while Surface B's `V2Event` is an
  88-arm structured union. Which surface governs is D9; the event shape is Surface-B when D9 chooses
  Surface B, and TBD when D9 chooses Surface A.
- **DoD:** `prompt` then a streamed turn whose `message.part.delta` payload a client can decode,
  then `interrupt`. **Do not merge on the unit suite alone.**

### S5 — read surface

- **Files:** `serve_v2.go`, `serve_v2_dto.go`, tests.
- **Behavior:** fs, location, project, agent, model, provider, config, reference, skill. This is
  where the three gaps in §7 get written: `/api/session/active`'s selection rule,
  `GET /api/agent/{agentID}`, and `GET /api/provider/{providerID}` — none of which has a wopr
  handler today. D7 lands here. `listProviders`' non-deterministic default must not be inherited
  (§7 hazard).
- **Gate:** round-trip decode for every V2 read DTO against `testdata/opencode-v2-schemas.json` —
  `Model.Info` and its nested `capabilities`/`time`/`limit`, `Agent.Info` and its nested
  `Provider.Request`, `Provider.Info`, `Reference.Info`, `Skill.Info`, `Config.Entry`,
  `Location.PublicInfo`, `Project`, `FileSystem.Entry`. `ServerInfo` is tested in S2 (its route
  is registered there).
- **DoD:** the model picker and agent picker decode every required field, with each synthesized
  placeholder named in §4.5 present.

### S6 — forms, permission, and docs

- **Files:** `serve_v2.go`, `docs/site/docs/server.md`, `docs/threat-model.md`, and
  `docs/site/docs/settings.md` **only if a V2 setting ships** — D3 dropped `server.authToken`, so
  under the plan as it stands there is no V2 content for `settings.md` and that file is not
  touched. Stated conditionally because Rev-1 made it conditional and the reversal of D3 is what
  emptied it.
- **Behavior:** the T2 stubs, the threat-model update for the `/api/*` surface, and `server.md`'s
  endpoint table with the pinned sha256.
- **Gate:** `make check` docs-drift green, plus a **table-driven test over the T2 catch-all** — the test
  exercises the single `/api/` catch-all handler (not all 113 operations individually, which would
  require concrete path-param values the inventory doesn't supply) with a matrix of: one unknown path
  (`/api/nope`), one known T2 path with each HTTP method (`GET /api/pty`, `POST /api/pty`, etc.),
  and one T2 path with path params (`/api/session/ses_test/permission/req_test`). Each asserts
  `503` + a `ServiceUnavailableErrorEncoded` body whose `message` is non-empty and matches the
  reason from the inventory. If the catch-all is not a single handler, the matrix fails — that is the
  finding.
- **DoD:** `server.md` gains a V2 section (endpoint table, the pinned spec sha256, "V2 is
  additive; V1 unchanged"), `threat-model.md` covers the V2 surface, and `make check` is green.

## 9. Risks

- **The spec is experimental and moving.** `info.version` is `0.0.1`, the description says
  "Experimental". S1's classifier is the only defence; treat an unexpected operation count as a
  blocker, not a merge conflict.
- **`message.part.updated` is the most likely source of stream decode failures** (restored from
  Rev-1 §8; Rev-0 dropped it). Rev-1's stated cause named a schema that does not exist — there is
  no `Part` among the 285, and parts are not an arm of `Session.Message.Info` (its 11 arms are in
  §4.5). The real shape: the assistant arm's `content` is an **array of a 3-arm `anyOf`** —
  `Session.Message.Assistant.Text` / `.Reasoning` / `.Tool` — and V1's `partDTO`
  (`serve_dto.go:184`) is a flat struct that models none of the three, so every streamed frame needs
  one of them chosen by inspection rather than by a field. Reshaping is mechanical but touches every
  stream frame, and the suite is historically blind to exactly this class — which is why S4 carries a
  decode gate *and* a live-curl gate. Compounding it: `message.part.delta` is **not** in
  `ServerDefinitions` (§4.4), so the streaming event wopr already emits is one the protocol surface's
  own union does not name.
- **The two surfaces disagree, and "spec, SDK, and server disagree" undersells it** (revised from
  Rev-1's claim). It is not that three artifacts disagree on a detail — it is that Surface A and
  Surface B share only 46 of 142 operations (§1.1), and the JS SDK is generated from **Surface B**,
  not from the published spec: `packages/sdk/js/src/v2/gen/types.gen.ts` names `/api/health` once and
  `/api/info` **zero** times, the exact inverse of the published spec. So the SDK is evidence about
  Surface B and is not evidence about Surface A; Rev-1 read it as a contradiction of the spec and
  that reading was wrong. The two real inconsistencies worth carrying are both on wopr's side:
  `/api/command`'s body is rejected by Surface A (§5), and the SSE frame shape differs between the
  surfaces (§4.4, D4). Build against D9's answer, tolerate added fields on input, emit no extras on
  output.
- **`+1 path, +1 operation, +29 schemas in eight days`** is **inferred, not re-derived**: it comes
  from Rev-1's recorded counts (117 / 141 / 256), whose pin file is gone. The direction is not in
  doubt — the spec moved — but the rate is one data point from a document that no longer exists.
- **`additionalProperties:false` on 201 of 285 schemas** turns every placeholder decision into a
  client-visible failure. Every slice that emits a V2 DTO carries a decode gate for it (§8).
- **The suite is not evidence here.** Green tests have coexisted with live bugs in this package
  five times. The S4 curl gate and the §2 arithmetic review are the two things that catch what the
  tests miss.
- **Scope-escalation tripwire** (restored from Rev-1 §8): if a live capture shows the client needs
  `session.next.*` for streaming rather than `message.part.delta`, that is a **scope escalation and
  stops the slice** — it is not a silent workaround, and it reopens §3.2.
- **Namespace crowding.** `/api/*` now holds V1-era routes and V2 routes, and `/api/health` is
  absent from the published spec though present in the protocol surface (§5, §1.1). New V2 work must
  not assume the namespace is clean.
- **A third of the published surface has no counterpart in the pinned source.** 96 of 142
  operations exist only under Surface A, so "opencode does this" is unverifiable for most of what
  this plan covers without fetching the JSON again (§1.1). If D9 picks Surface B, those 96 leave the
  scope entirely and the plan shrinks by two thirds. That is the decision, and it is not made here.

## 10. Verification

- The pin: sha256 and counts in §1, re-derived by the curl in §1. Record the same hash in
  `docs/site/docs/server.md` when S1 classifies the inventory, so the number a reader checks is on
  the page that claims conformance and not only in a plan.
- Every `file:line` in §7 and the wiring paragraph was re-read on 2026-10-15 at `3421839`.
- **The V2 spec is readable in the local opencode checkout, and this document's earlier claim that it
  was not is withdrawn.** `opencode-src` is pinned at v1.18.34 (`aec0b9a`), and
  `packages/protocol/src/groups/` there is a **second V2 surface**: 18 files, 58 endpoints,
  `HttpApiEndpoint.<method>("<name>", "/api/path")` (§1.1). Rev-1 said "no `/api` literals" and
  pointed at `server/routes/instance/httpapi/public.ts:155,181`,
  `middleware/schema-error.ts:27` and `middleware/workspace-routing.ts:167` as where they *do* occur;
  those are real but they are not where the V2 contract lives. Rev-2 re-derived the 46 / 12 / 96
  comparison directly from `packages/protocol/src/groups/` and
  `packages/sdk/openapi.json`, both at `aec0b9a`. That checkout is the authority for V1 *and* for
  Surface B; the fetched JSON is the authority for Surface A. **Which one governs is D9, and it is
  open.**
- The comparison is re-derivable without this document:
  `grep -rhoE 'HttpApiEndpoint\.[a-z]+\("[^"]+", *"[^"]+"' packages/protocol/src/groups/` against the
  fetched JSON's operation set, normalizing `:param` to `{param}`.
- Next: `verify-fan` over this document before S1 dispatch, with **both** surfaces pinned for the
  run — the JSON file by sha256 and `opencode-src` by commit. A fan given only the JSON cannot see
  the 96, the 12, or D9.