# Settings

WOPR reads settings from `~/.wopr/agent/settings.json`. The file is optional. Every
key is optional, and a key you leave out keeps its default.

Set `WOPR_HOME` to move the whole configuration root. Set `WOPR_CODING_AGENT_DIR`
to move only this directory.

```json
{
  "defaultModel": "claude-sonnet-4",
  "theme": "dark",
  "compaction": { "enabled": true }
}
```

Use `/settings` to change a setting from inside a session. WOPR writes the file
for you and applies the change without a restart.

## Where WOPR reads settings

WOPR merges two files. A key set in the project file overrides the same key in the
global file. `defaultProjectTrust` is the exception: WOPR reads it from the global
file only.

| Scope | Path |
|---|---|
| global | `~/.wopr/agent/settings.json` |
| project | `<project>/.wopr/settings.json` |

WOPR reads the project file only after you trust the project. An untrusted project
contributes nothing, so cloning a repository cannot change your shell, your model
or your packages before you agree to it. See [Security](security.md).

An empty default column means WOPR has no value for the key until you set one, or
the value depends on what it detects.

## Models and providers

| Key | Type | Default | Meaning |
|---|---|---|---|
| `defaultProvider` | string |  | Provider used when none is chosen. |
| `defaultModel` | string |  | Model used when none is chosen. |
| `routing` | string | unset | The last routing choice while model routing is on (it is a per-session choice, not a default), saved when you pick a mode or a model, Tab, run `/router on` or `off`, or save `/setup`, and restored at the next start only while routing is on: the orchestrator's mode name (`auto`, `cost`, `speed`, `quality`, `uncensored`) or `pinned` (the default model orchestrates). `--model` and `WOPR_ROUTER` override it. |
| `subagents` | string | unset | The subagents' last choice from `/model`, restored at the next start: a mode name (routing on), a `provider/model`, or `same` (the orchestrator's model). Unset means auto with routing on and `same` with it off. |
| `defaultThinkingLevel` | string |  | Thinking level applied to a new session. |
| `modelThinkingLevels` | object |  | Thinking level for a new session per model, keyed by `provider/modelId`. It overrides `defaultThinkingLevel`. |
| `thinkingBudgets` | object |  | Token budget per thinking level: `minimal`, `low`, `medium`, `high`. |
| `transport` | string | `auto` | Transport for providers that support more than one: `auto`, `sse`, `websocket`, or `websocket-cached`. |
| `retry` | object | see meaning | Retry policy: `enabled` (default `true`), `maxRetries` (default `3`), `baseDelayMs` (default `2000`), `maxAgentDelayMs` (the cap on each retry delay, default `60000`), and a per-`provider` override. An explicit `0` is kept. |
| `httpIdleTimeoutMs` | number | `300000` | Idle timeout for provider requests, in milliseconds. `0` or `"disabled"` turns it off. |
| `cacheWarming` | string | `streaming` | `off`, `streaming`, or `idle`. Keeps the provider's prompt cache warm with periodic requests. Global setting only: WOPR ignores it in a project file. See [cache warming](#cache-warming). |

### Cache warming

Cache warming is on by default. Each refresh is a real provider
request that you pay for. WOPR replays the last request with a one-token output
limit shortly before the prompt cache expires, so the next request reads the
cache instead of writing it again at full price.

- `off`: WOPR never sends refreshes.
- `streaming`: only while the agent is running. This is the default.
- `idle`: while the agent runs, and between runs for up to 30 minutes after
  the last request.

A refresh is sent only when the model declares a prompt cache lifetime and WOPR
expects it to save at least $0.05 in avoided cache-miss cost. Warming stops when
the conversation or model changes, after one hour (30 minutes when idle), and
when the session ends. Refresh usage counts toward the session's token and cost
totals, but it never enters the model's context. `/session` shows the mode, the
next decision, and its estimated cost.

To turn cache warming off, choose `off` for **Cache warming** in `/settings`, or
set it in `~/.wopr/agent/settings.json`:

```json
{
  "cacheWarming": "off"
}
```

## Session behavior

| Key | Type | Default | Meaning |
|---|---|---|---|
| `compaction` | object | see meaning | Automatic compaction: `enabled` (default `true`), `reserveTokens` and `keepRecentTokens` (default sized to the model's window; see compaction), and `modelOverrides` (per-model `reserveTokens` and `keepRecentTokens` keyed by exact `provider/modelId`). See [compaction](compaction.md). |
| `contextPruning` | object | see meaning | Context pruning: `enabled`, `dedupe`, `purgeErrors`, `supersedeReads` and `compress` (each default `true`), and `compressThreshold` (default `0.4`). See [context pruning](compaction.md#context-pruning). |
| `branchSummary` | object | see meaning | Summary written when you leave a branch: `reserveTokens` (default `16384`) and `skipPrompt` (default `false`, skip the prompt and write no summary). |
| `steeringMode` | string | `one-at-a-time` | How queued steering messages are dispatched. |
| `followUpMode` | string | `one-at-a-time` | How queued follow-up messages are dispatched. |
| `sessionDir` | string |  | Directory that holds session files. |
| `defaultProjectTrust` | string | `ask` | Trust decision applied to a project that has none: `ask`, `always`, or `never`. WOPR reads this key from the global file only. |

## Interface

| Key | Type | Default | Meaning |
|---|---|---|---|
| `theme` | string |  | Active theme name. |
| `fullscreenScrollbar` | string | `auto` | Transcript scrollbar: `auto`, `always`, or `hidden`. |
| `fullscreenSidebar` | string | `auto` | Sidebar: `auto` (shown when the terminal is at least 121 columns wide), `show`, or `hide`. `ctrl+x b` toggles it and saves the choice. |
| `fullscreenExitOutput` | string | `clear` | `clear` clears the screen when WOPR exits and prints only the resume hint. `transcript` prints the final transcript. `resume-hint` restores the previous screen and prints only the resume hint. |
| `fullscreenCopyOnSelect` | boolean | `true` | Copy selected text automatically. When disabled, `ctrl+x` `y` copies the active selection. |
| `hideThinkingBlock` | boolean | `false` | Hide thinking blocks in the transcript. |
| `doubleEscapeAction` | string | `tree` | Action bound to pressing escape twice. |
| `treeFilterMode` | string | `default` | Filter `/tree` opens with. |
| `editorPaddingX` | number | `0` | Horizontal padding inside the editor. |
| `outputPad` | number | `1` | Horizontal padding for messages and thinking blocks: `0` or `1`. |
| `autocompleteMaxVisible` | number | `5` | Rows shown in the autocomplete list. |
| `showHardwareCursor` | boolean | `false` | Show the terminal's own cursor. |
| `markdown` | object |  | Markdown rendering: `mermaid`. |
| `quietStartup` | boolean | `false` | Suppress the startup banner. |
| `collapseChangelog` | boolean | `false` | Show a condensed changelog. |
| `lastChangelogVersion` | string |  | Last changelog version shown. WOPR writes this. |

## Images and terminal

| Key | Type | Default | Meaning |
|---|---|---|---|
| `terminal` | object | see meaning | Terminal image, width, and progress settings described below. |
| `images` | object | see meaning | Image resize and transcript-blocking settings described below. |
| `terminal.showImages` | boolean | `true` | Render images when the terminal accepts them. |
| `terminal.imageWidthCells` | number | `60` | Width of a rendered image, in terminal cells. |
| `terminal.showTerminalProgress` | boolean | `false` | Show OSC 9;4 progress in the terminal tab. |
| `images.autoResize` | boolean | `true` | Resize an image to fit the width. |
| `images.blockImages` | boolean | `false` | Hide images in the transcript. Images are still sent to the provider. |

## Resources

| Key | Type | Default | Meaning |
|---|---|---|---|
| `skills` | string[] |  | Skill paths to load. See [skills](skills.md). |
| `prompts` | string[] |  | Prompt paths to load. |
| `themes` | string[] |  | Theme paths to load. |
| `enableSkillCommands` | boolean | `true` | Offer skills as slash commands. |

## Terminal capability overrides

| Key | Type | Default | Meaning |
|---|---|---|---|
| `terminal.hyperlinks` | `boolean \| "auto"` | `"auto"` | Override OSC 8 hyperlink detection. |
| `terminal.images` | `"kitty" \| "iterm2" \| "auto" \| false` | `"auto"` | Override inline-image protocol detection. `false` turns images off. |
| `terminal.trueColor` | `boolean \| "auto"` | `"auto"` | Override true-color detection. |

A setting wins over the matching `WOPR_HYPERLINKS`, `WOPR_IMAGE_PROTOCOL` or
`WOPR_TRUE_COLOR` variable, and the variable wins over detection. `"auto"` and any
other value leave detection in charge. Without true color, WOPR draws the theme
in the 256-color palette. See [terminal setup](terminal-setup.md).

## Shell and tools

| Key | Type | Default | Meaning |
|---|---|---|---|
| `shellPath` | string |  | Shell used by the bash tool. |
| `shellCommandPrefix` | string |  | Prefix applied to every shell command. |
| `externalEditor` | string |  | Editor opened by `app.editor.external`. |
| `cleanupTempFiles` | boolean | `true` | Delete the temp files sessions create once nothing needs them. See [Temp file cleanup](#temp-file-cleanup). |

## Tools

| Key | Type | Default | Meaning |
|---|---|---|---|
| `defaultTools` | string[] | `read`, `bash`, `edit`, `write`, `task`, `web_fetch`, `web_search`, `mcp` | Built-in tools active at startup. An empty array turns off every built-in tool but keeps SDK tools. `web_search` and `mcp` are installed only when a search backend or an MCP server is configured. |
| `mcpServers` | object |  | MCP servers by name: `command`, `args`, `env` for stdio, or `url`, `headers` for streamable HTTP. See [MCP servers](mcp.md). |
| `web` | object |  | `web_fetch` limits and the `web_search` backend: `allowPrivateNetwork`, `fetchTimeoutMs`, `maxBytes`, `search`. See [Web tools](web.md). |
| `hashline` | string | `auto` | Hashline anchors on `read` and `edit`: `auto`, `on`, or `off`. `auto` turns them on for models on free-local and free-remote router tiers. A router model's own `hashline` flag wins. |
| `bashCompaction` | boolean | `true` | Drop known noise from `bash` output before the model sees it. See [bash output compaction](#bash-output-compaction). |
| `diagnostics` | object | see meaning | Checker run after `edit` and `write`: `enabled` (default `true`), `timeoutMs` (default `4000`), and `commands` (per-language overrides). See [post-edit diagnostics](#post-edit-diagnostics). |

The built-in tools are `read`, `bash`, `powershell`, `edit`, `write`, `grep`,
`find`, and `ls`. The CLI tool options override this setting for one run. See
[Tools](cli.md#tools).

### Hashline anchors

With hashline on, `read` prefixes each line with an anchor, the line number plus two letters hashed from the line and the one before it: `12ab|  return x`. `edit` then takes `{"anchor": "12ab", "end": "15cd", "newText": "..."}` in place of `oldText`; the range is inclusive and an empty `newText` deletes it. When a line changed since the read, the edit is rejected and the error shows fresh anchors around it. A successful anchored edit returns fresh anchors for the lines it changed. Exact `oldText` edits keep working in both modes.

The mode is decided per model, for each request: the model serving it sees the matching `read` and `edit` descriptions, so a routed local model and a frontier orchestrator can differ within one session. Weak local models gain the most, because they no longer have to reproduce text exactly. Anchors cost four to five tokens per line read, so `auto` leaves them off for subscription and paid models. Subagents are read-only and never get anchors.

### Post-edit diagnostics

After `edit` or `write` succeeds, WOPR runs a fast checker on the changed file and appends the problems that are new since its last check of that file, at most ten lines. Positions are ignored when comparing, so problems an edit only moved are not repeated. A missing checker, a timeout, or a checker crash adds nothing.

| Language | Checker, first one installed |
|---|---|
| Go | `gopls check <file>`, else `go build` on the package (`go vet` for a `_test.go` file) |
| TypeScript | the project's own `node_modules/.bin/tsc --noEmit` with the nearest `tsconfig.json`; nothing otherwise |
| JavaScript | `node --check <file>` |
| Python | `ruff check <file>`, else a `python3` syntax check that writes no bytecode |
| Shell | `bash -n <file>` |

`diagnostics.commands` replaces the checker for a language with a command line; `{file}` and `{dir}` are substituted, the words are split on spaces without shell quoting, and `off` turns the language off:

```json
{
  "diagnostics": { "timeoutMs": 3000, "commands": { "go": "go vet {dir}", "shell": "off" } }
}
```
### Temp file cleanup

Models often leave scripts, screenshots and downloads in the system temp directory. WOPR tracks what a session creates there and deletes it once nothing needs it.

What is tracked:

- Each shell command runs with `TMPDIR` set to a folder for the session, so `mktemp` and language temp APIs write there.
- Files the `write` and `edit` tools put in /tmp or the system temp directory.
- Top-level entries in those directories that appear during a shell command, belong to you, and are named in the command or its output. An entry nothing names may belong to another program and is left alone; anything that existed when WOPR started is never tracked.
- The shell tool's own full-output logs (`wopr-bash-*.log`).

The list lives in `~/.wopr/agent/temp-files.json`, with the session and the WOPR process that created each entry.

When tracked files are deleted:

- After a compaction, if neither the summary nor the kept messages mention the file by path or name, and it hasn't been used in the last 5 minutes.
- When you leave a session (`/new`, resume, switch), for files not used in the last 5 minutes. The rest wait for a later trigger.
- When WOPR exits, including a closed terminal or an external interrupt.
- When WOPR starts, for files whose creating process is no longer running (a crash or `kill -9`).

Deletion stays inside the temp directories, touches only tracked entries you own, and never follows a symlink. Running processes are left alone. Set `cleanupTempFiles` to `false`, or turn off **Clean up temp files** in `/settings`, to keep everything.

### Bash output compaction

WOPR drops lines that carry no signal from the output of common commands before the model sees them. Filters only remove lines they recognize and never reword the rest; failure lines are always kept.

| Command | Dropped |
|---|---|
| `git status` | `(use "git ...")` hint lines |
| `git diff`, `git show` | the `index` blob-hash line under each file header |
| `git log` (default format, no count) | commits after the first 50, with a count |
| `go test` | `ok` and `[no test files]` package lines, `=== RUN`/`--- PASS` framing; a one-line package summary is added |
| `pytest`, `python -m pytest` | progress rows and `PASSED` rows without failures |
| `cargo test` | `... ok` test rows and `Compiling` lines |
| `npm`/`yarn`/`pnpm`/`bun` test, `jest`, `vitest`, `mocha` | passing (`✓`, `PASS`) rows |
| `ls -l`, `ls -la` | the `total` line, `.` and `..`, and the link-count, owner, and group columns when one owner and group covers every entry |
| anything else except file and search output | runs of six or more lines that differ only in numbers (progress), keeping the first two and the last; lines that mention a failure never collapse |

A command with a pipe or redirect is left alone, so `| cat` returns raw output. When compaction removed more than 1 KB, one line says so and, with ObservationPack on, gives the `obs_recall` id of the raw output.

## Notices

| Key | Type | Default | Meaning |
|---|---|---|---|
| `showCacheMissNotices` | boolean |  | Report a prompt cache miss and each successful cache-warming refresh. |
| `enableAttributionHeaders` | boolean | `true` | Send wopr's app-attribution headers (`HTTP-Referer`, `X-OpenRouter-Title`, `X-BILLING-INVOKE-ORIGIN`, `User-Agent`) on OpenRouter, NVIDIA, and Cloudflare requests. Set to `false` to omit them. |

## Related

- [Environment variables](environment-variables.md) lists runtime overrides.
- [Keybindings](keybindings.md) covers `keybindings.json`.
- [Models](models.md) covers model selection.
