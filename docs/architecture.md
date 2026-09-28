# WOPR architecture

This page maps WOPR's core, its routing and efficiency layer, the embedding SDK, and local state. Return to the [maintainer docs router](README.md) for focused references.

## Layer map

```text
wopr binary
├── core
│   ├── CLI/TUI/session/agent loop
│   ├── providers and message conversion
│   └── builtin tools
├── routing and efficiency
│   ├── model routing (internal/codingagent/router)
│   └── token-efficiency mechanisms (internal/codingagent/efficiency)
├── SDK and docs
│   ├── embedding API (coding)
│   └── embedded reference documentation
└── resources
    └── skills, prompt templates, themes, and context files
```

## Package map

| Package | Purpose |
|---|---|
| `agent` | Agent loop, messages, tools, and Session primitives |
| `ai` | Models, providers, authentication, and stream types |
| `coding` | Embedding API: Services, Runtime, Sessions, and resource discovery |
| `cmd/wopr` | Command-line application and RPC surface |
| `tui` | Terminal UI components and renderer |
| `internal/codingagent` | Private command-line implementation, including the router and the system prompt |

`cmd/wopr-eval` and `internal/evals` are the development-only eval harness (`evals/README.md`); they are not part of the shipped binary.

## Embedding

A Go program embeds WOPR through the `coding` package: it builds `coding.Services` and starts Sessions with `coding.StartSession`. It adds its own tools with `ExtraTools` and gates tool calls with `BeforeToolCall` hooks on `coding.SessionStartOptions`. WOPR has no extension or plugin runtime.

## Local roots

```text
$WOPR_HOME/                         # default ~/.wopr; writable
├── state/<namespace>/              # namespaced additive capability state
└── docs/                           # managed embedded user manual

$WOPR_CODING_AGENT_DIR/             # default $WOPR_HOME/agent
├── settings.json
├── sessions/
├── catalog/                        # downloaded catalog bundle roots
├── skills/
├── prompts/
├── themes/
├── models.json
├── router.json
└── SYSTEM.md
```

Project-local resources mirror the agent root under the workspace:

```text
<workspace>/.wopr/
├── settings.json
├── skills/
├── prompts/
├── themes/
└── state/<namespace>/
```

## Boundary invariants

- Core command paths win.
- WOPR-only mechanics that are not part of the public API live under `internal/`.
- Contract changes land atomically across code, clients, tests, and docs.
