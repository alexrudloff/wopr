# Changelog

All notable changes to wopr are recorded in this file.

## [Unreleased]

- CLI: unknown `--options` are errors instead of being silently swallowed with the next argument, and `--` ends option parsing as the help describes. `wopr diagnose` is removed; `wopr status` now also lists the providers with stored credentials and the default model, and `wopr version` prints the build. `auth print-bearer-token --min-expiry` takes a Go duration (`30m`, `1h30m`).
- Go SDK: `coding.Runtime` and `coding.NewRuntime` are replaced by `coding.StartSession(services, options)`; set `SessionStartOptions.ResumePath` to resume. `SessionStartOptions.ThinkingLevel` sets the starting thinking level. `coding.BuildModelWithWarning` reports an unknown model id.
- Fixed: `--model amazon-bedrock/...` at startup built an OpenAI-compatible client; startup dropped the unknown-model warning; RPC mode started with 4 default tools instead of 8 and ignored `--exclude-tools`; print mode ignored `--thinking`; print and RPC modes ignored the provider timeout and retry settings.
- The in-process extension API is removed: `coding/extension`, extension events and hooks, extension commands, shortcuts, and UI, the RPC extension UI dialogs and the `extension_error` event, and the extension surface of the Go SDK `Runtime` and `Session`. The Go SDK keeps `ExtraTools` and `BeforeToolCall` hooks. RPC mode writes `/llama` notices as `ui_notify` records, and `get_commands` reports `/llama` with source `builtin`.
- Distribution moved to GitHub. Releases are built by `.github/workflows/release.yml` on a `v*` tag and published on GitHub Releases with a `SHA256SUMS` signed by the release Ed25519 key. `install.sh` now lives at the repository root (`curl -fsSL https://raw.githubusercontent.com/alexrudloff/wopr/main/install.sh | sh`). `wopr update` finds the latest release through the `releases/latest` redirect, verifies the signature and the archive SHA-256, and replaces a writable standalone binary; it no longer needs an installer receipt. Removed: the npm packages and Node launcher, the npm/pnpm/yarn/bun update tier and the `npmCommand` setting, the update manifest and `WOPR_UPDATE_URL`, `WOPR_UPDATE_TRUST_ROOT`, and `WOPR_UPDATE_ALLOW_LOOPBACK_HTTP`.
- Extensions are in-process Go only. Removed: the TypeScript/JavaScript extension runtime, the Go (out-of-process), Python, Rust, and TypeScript extension SDKs, the subprocess extension host and runtime cells, Cartridges and Cartridge Binaries, and the package manager. Removed commands and flags: `wopr install`, `remove`, `uninstall`, `list`, `package`, `extension`, `extensions`, `build`, `setup`, and `cartridge`; `--cartridge`, `-e`/`--extension`, and `-ne`/`--no-extensions`. `wopr update` updates only wopr itself. The `packages` and `extensions` settings keys are ignored.
- The minimal-code discipline is now always on in the system prompt; `/lean` and its levels are removed. The report commands are renamed `/review` (now bugs first, then simplifications), `/audit`, and `/debt`, and the comment marker is `simplify:`.
- Routing has three states: auto (Jev picks the orchestrator once and keeps it while its cache is warm, moving only up mid-session), pinned (picking a model; subagents stay routed), and off (`/router off`).
- New `task` tool: read-only `explore` subagents with fresh context, budgets, and parallel runs. Each brief is routed on its own (rules, then Jev's typed questions with asymmetric thresholds, cold-start aware, domain affinity as a tie-breaker); wopr verifies every quoted line, escalates once on an untrustworthy answer, and logs each attempt to `~/.wopr/agent/task-log.jsonl`.
- Jev is pinned to `jev-1.13`, retries once within 1.3 s per attempt, and trips a circuit breaker; a local secret scanner redacts credentials before anything reaches Jev and keeps secret briefs on owned hardware.

## [0.2.0] - 2026-09-26

First wopr release.

- **Jev model routing.** Every prompt goes to the cheapest model that can do the job: local hardware first, then subscriptions, then pay-per-token providers only when nothing else fits. `/router` shows and steers the decision.
- **Efficiency mechanisms.** Action Fusion, ObservationPack, the Evidence-Preserving Reducer, and Online Context Compact cut the tokens each request carries.
- **Lean build discipline.** `/lean` keeps generated code small: reuse what exists, prefer the standard library, and add no abstraction or dependency that was not needed.
- **Fullscreen TUI.** A new interface with a WOPR home screen, sidebar, and status line.
- **opencode theme compatibility.** opencode theme files load as wopr themes.
- `/share` publishes the session as a secret GitHub gist through the GitHub CLI.
