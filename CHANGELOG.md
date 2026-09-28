# Changelog

## [0.1.0] - 2026-09-28

First release.

- Model routing: Off, Basic (built-in rules from your model order, speed, cost class, and windows), or Jev (TypeSafe Jev scores each prompt). Modes: auto, speed, quality, cost, uncensored. Jev unreachable falls back to Basic.
- `/setup`: connect subscriptions, API keys, and endpoints; choose models from each provider's live list; put them in order. Speed is measured automatically.
- `/model`: pick the orchestrator and subagents by mode or model; Tab cycles.
- Subagents: routed read-only `explore` tasks with quote verification and escalation.
- Efficiency, all on by default: action fusion, observation pack, evidence reducer, online compaction, tool-output half-life, stall nudge, test-rerun cap, lazy tools, apply_patch for GPT/Codex, and subscription quota balancing.
- Compaction sized to each model's window, including compacting to fit a smaller routed model.
- Fullscreen terminal UI with sidebar, command palette, and opencode-compatible themes.
- Signed releases and `wopr update`.
