# Changelog

## [0.2.3] - 2026-10-05

- **Compaction at 200k**: a large window no longer fills to 98% before compacting; the context compacts past 200k tokens, so each turn re-reads far less. `/settings` → Compact at picks 100k, 200k, 400k, or the window (`compaction.maxContextTokens`).
- **Compaction on the conversation's model**: summaries run on the model running the conversation, never a routed side model such as a slow local one; when the conversation is larger than its window, it summarizes the newest messages that fit, so compaction never fails on size.
- **Fixed: sessions that pruned never compacted**: after context pruning the size estimate read about a third low (a 994k-token session read as 666k), so the window filled and replies were cut off. Automatic compaction now says when it fails instead of failing silently.
- **Plans**: the model lists a multi-step task's steps with `update_plan` up front and marks each done, giving online compaction its boundaries.

## [0.2.2] - 2026-10-04

- **Background work**: `bash` with `run_in_background` starts long jobs in their own process group and returns at once; the model is told when a job ends (exit code and last output), so it never sleep-polls. A strip above the prompt lists running subagents and shell jobs; down on an empty prompt focuses it, enter opens a live view with Stop and Close.
- **Subagents can write**: `task` type `general` runs with the full tools under the session's undo and backup hooks, locks the files it changes, and reports them.
- **Longer prompt cache**: the interactive conversation uses Anthropic's 1-hour and OpenAI's 24-hour cache, so a pause over 5 minutes no longer rewrites the whole cached conversation. Context pruning and image pruning move the cached prefix far less often.
- **Images**: sent only to models that accept them, sized by tile budget; requests carry the newest images and older ones become placeholders, with a size guard under the provider's limit.
- **Safety backup**: before a shell command rewrites git history or opens a SQLite database with live `-wal`/`-shm` files, wopr saves what it could destroy and tells the model how to restore it.
- **Final check**: the first time a run that changed something would end, the model verifies each requirement against the request.
- **Tool batches**: read-only calls run together; any other call waits for the ones before it, so a write and a read of the same file in one message see the finished file.
- **Edits**: `edit` matches looser when the text isn't found as written (indentation, whitespace, doubled backslashes) and lists each failing edit with its closest lines; hashline anchors turn off per model when they fail more than exact-text edits.
- **Models**: model lists come from providers and models.dev, not a hand-written catalog; a provider's default is its current flagship; an unpriced model shows "unknown", never $0.
- **Providers and auth**: OpenCode Zen under Add an API key (thanks @krishnaglick), with a real key check; `/logout` and `wopr logout` list every stored credential, API keys included.
- **Print mode**: `--deadline` / `WOPR_DEADLINE` gives a run a time budget; piped stdin no longer hangs on a pipe that never closes; provider errors exit nonzero in JSON mode; `--gtw` runs Global Thermonuclear War.
- **Fixes**: large tool output and oversized session lines no longer freeze scrolling or resume; bare URLs in output are clickable; faster rendering per keystroke; Anthropic web-search replies are replayed correctly and no longer inflate context size; war council time budgets scale with each member's speed; `web_fetch` retries HTTP 406; a DuckDuckGo bot check pauses searches instead of retrying.

## [0.2.1] - 2026-09-30

- **`/undo` covers shell edits**: when a command (`sed -i`, a Python script, `cat >`) changes files, they become normal undo changes, and the command's card shows their diff. The "before" comes from wopr's copies of files the model read, wrote, or named, or from git for clean tracked files; files a command created are removed on undo.
- **`/gtw`** toggles Global Thermonuclear War (`/gtw on|off` sets it). War council members now all run at once instead of four at a time.
- **Stall nudge** stops interrupting good work: new file reads, searches, and read-only shell commands (`rg`, `sed -n`, `cat`, …) count as progress; the clock starts at the model's first reply, and one long command can't fill the idle time.
- **A warning when you send something that looks like a secret** (API keys, OAuth codes, bearer tokens, …): it still goes to the model, but you're told it's sent and saved.
- **Long writes show progress**: the card names the file as soon as it arrives and counts lines as they stream in.
- **Scrolling**: the mouse wheel moves 3 lines per step (Scroll speed in `/settings`, 1–10).

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
