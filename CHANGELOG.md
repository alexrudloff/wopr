# Changelog

## [0.1.5] - 2026-09-30

- **Global Thermonuclear War** (`ctrl+x g`) is a toggle on the current session: your top model at max thinking plus a war council. On each prompt, every other model you've set up proposes in parallel (with web search), and the top model synthesizes. For real code changes, council members each build the change in their own git worktree and run the tests; the top model applies or merges the best. War-red prompt and sidebar while it's on; `/setup` → War council picks members and time limits.
- **Private mode**: mark connections "Private endpoint"; private mode keeps the conversation, subagents, and background summaries on them and never falls back to the cloud.
- **Web search works with no setup**: your Brave/Tavily/SearXNG key first, else the model provider's own search (Claude, GPT/Codex), else DuckDuckGo. `web_search`, `web_fetch`, and `task` are always loaded.
- **`/undo`** reverts the last file change (`/undo <path>`, `/undo prompt`); `write` refuses to replace an unrelated existing file unless told to.
- **Context and routing**: compaction sized to each model's window, including your own window setting; compact-to-fit a smaller routed model; subscription quota balancing; per-model efficiency learning; every efficiency mechanism on by default.
- **Routing**: Off / Basic / Jev, with Basic taking over when Jev can't answer; Jev works with TypeSafe, OpenRouter, or a proxy (API key field). Shift+Tab cycles backwards.
- **OpenCode Go** under Add an API key (a subscription plan).
- **Setup**: xhigh thinking offered when a server accepts it; provider names as you set them.
- **Quality of life**: word-level diff highlighting; retries show "Attempt 2 of 4" and one error when they run out; readable network errors; session images stored once outside the session file; temp files a session creates are cleaned up; `/upgrade` with restart into the session, and what's new after an update.

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
