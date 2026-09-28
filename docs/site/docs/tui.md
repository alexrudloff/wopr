# Terminal UI

WOPR provides a Go-native terminal interface: a fullscreen layout with a home screen, a scrolling transcript, a prompt panel, a sidebar, and dialogs over a dimmed backdrop.

This page describes the WOPR user interface. It does not expose WOPR's internal TUI packages as a stable application SDK.

## Start the TUI

Run `wopr` without print or RPC mode:

```bash
wopr
```

The TUI uses the current working directory, settings, model selection, and trusted Resources.

## Main areas

| Area | Purpose |
|---|---|
| Home | Shown while the session is empty: the lamp panel, the WOPR wordmark, the greeting, the prompt, a tip, and the directory and version. The lamps show live numbers in binary: the clock, the git commit, and the session. |
| Transcript | User messages as cards, indented assistant text, "Thought" headers for reasoning, one-line tool rows, tool panels for commands, edits, and writes, and a closing `▣ agent · model · duration` line per turn. |
| Prompt | A panel with the agent, the routing mode while model routing picks the model (`auto`, `cost`, `speed`, `quality`, `uncensored`), or the model and provider, a thinking-level meter, the subagents' choice when it differs from the orchestrator's (`· subagents auto`), and `N running` while background agents run, under the text. With the sidebar open, the working spinner and interrupt hint sit at the right of that line, and the sidebar footer carries the directory and the command palette key. With the sidebar hidden, a row below the panel shows the spinner and interrupt hint, or the directory, context usage, and the command palette key. |
| Sidebar | On terminals wider than 120 columns: the session title, context use and cost, routing while it is on (the orchestrator, the subagents when their choice differs, task counts, and target health), then Agents, a card of three pages (usage and limits; speed, with the last reply and the session average; and models, with each model's replies, subagents, tokens, cost, and average speed this session), the work Queue, and modified files. When those do not fit they split into pages with dots at the bottom: `ctrl+x o` or a click on the dots switches page. Toggle the sidebar with `ctrl+x b`; the choice is saved (`fullscreenSidebar`). |
| Dialogs | The command palette, model, theme, and session pickers, settings, and other selectors open a quarter of the way down over a dimmed screen. |
| Toasts | Warnings and notices appear at the top right for a few seconds. |

## Core interaction

- Type `/` to open command completion, or `@` to search for project files.
- Press Enter to submit; Shift+Enter or Ctrl+J inserts a newline.
- Press Ctrl+P to open the command palette.
- Press Ctrl+X, then a key, for quick actions: `n` new session, `l` sessions, `m` model, `t` theme, `b` sidebar, `c` compact, `e` external editor, `y` copy, `x` export, `j` session tree, `g` Global Thermonuclear War (the strongest model at maximum thinking), `a` agents, `o` next sidebar page, `?` a waiting question, `q` quit.
- Press Tab on an empty prompt to cycle the models you set up in `/setup`. With model routing on, Tab cycles the routing modes (auto, cost, speed, quality, and uncensored when a model is flagged) first; a mode Tab picks applies to the subagents too, a model only to the orchestrator; `/model` → Subagents splits them.
- A `task` call renders as one row, `│ Explore — description`, with a `↳` line: the current tool while it runs, then tool calls, duration, and model. A background task's row reads `background · model`; when it finishes, its result arrives as a `◆ task_<id> finished` block (expand it with `ctrl+o`) and the agent reads it after its current turn, or right away when idle.
- The sidebar's Agents section lists running tasks (label, elapsed, tool calls, tokens, cost) and, for a minute, finished ones before they fold into `N done · $x.xx`. `ctrl+x a`, `/agents`, or a click on a row opens an agent's brief, tool calls or transcript, and result; `ctrl+k` in the `/agents` list, the palette's "Stop agent…", or `/agents stop <id>` stops one. Escape never stops background agents.
- The Queue section shows the work queue the agent keeps with `update_plan`: open items marked `[ ]` pending, `[•]` in progress, `[▸]` running on an agent, `[!]` blocked, `[✗]` failed, `[↻]` interrupted; completed items are counted beside the title.
- When a decision is yours, the agent can ask with `ask_user`: a Question dialog with its options and a "Type your own answer" row. Pick with the arrows and enter, a number key, or a click; esc skips the question and the agent proceeds on its own judgment. A question that arrives while you are typing waits with a "Question waiting · ctrl+x ?" toast and opens once the prompt is empty. The tool exists only in the TUI: print, JSON, and RPC modes and subagents never see it.
- `/goal <objective>` keeps the agent working across turns until an independent audit confirms the goal; the Queue section pins it as `◎` with its turns, spend, and time, and a toast reports when it is met or paused. See [Routing and subagents](routing.md#goals).
- Quitting while background agents run asks first. They stop with wopr and are recorded as interrupted; on resume a notice offers the palette's "Re-dispatch interrupted agents".
- Press Escape twice to interrupt a running turn; the first press arms the interrupt.
- Press Ctrl+T to cycle the thinking level and F2 to cycle models.
- Press Ctrl+C to clear the prompt. Press it again within 500 ms to exit.

See [Keybindings](keybindings.md) for the complete binding list.

## Resizing

The TUI responds to terminal resize. It calculates width in terminal cells and preserves Unicode and ANSI rendering boundaries.

## Themes

WOPR bundles a set of themes, with `wopr` as the default, and loads opencode theme files unchanged. Press `ctrl+x t` to pick a theme with a live preview.

See [Themes](themes.md).

## Accessibility and terminal behavior

WOPR supports keyboard operation for its interactive surfaces. Theme authors must preserve readable contrast, focus indication, terminal-cell width, and reduced-motion expectations.

Use a supported terminal and verify behavior at narrow and wide widths. See [Terminal setup](terminal-setup.md).

## Related documentation

- [Using WOPR](usage.md)
- [Keybindings](keybindings.md)
- [Terminal setup](terminal-setup.md)
