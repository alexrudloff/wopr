# Using WOPR

Run `wopr` in the directory you want to work in to start the interactive TUI. WOPR commands include top-level CLI verbs, slash commands inside the TUI, and keyboard shortcuts.

- [Command line](cli.md) lists the CLI verbs, the generic subcommands, and the `wopr docs` commands for the embedded reference bundle.
- [Slash commands](slash-commands.md) lists the commands you type in the TUI editor.
- [Keybindings](keybindings.md) describes how to change the default shortcuts.

## Keyboard shortcuts

| Key | Action |
|---|---|
| `Enter` | Submit message. |
| `Shift+Enter` / `Ctrl+J` | Newline in editor. |
| `Esc` | Cancel the active dialog, compaction, or Bash command. While a model turn runs, press it twice within five seconds to interrupt. |
| `Ctrl+C` | Clear the editor; press it again within 500 ms to exit. |
| `Ctrl+D` | Exit on empty editor. |
| `Ctrl+P` | Open the command palette. |
| `F2` / `Shift+F2` | Cycle to the next/previous model you set up in `/setup`. |
| `Ctrl+T` | Cycle thinking level on reasoning-capable models. |
| `Tab` | Autocomplete (slash commands, paths, mentions). |
| `Ctrl+L` | Open the model selector. |
| `Ctrl+O` | Expand or collapse tool output. |
| `Ctrl+X` `H` | Show or hide thinking blocks. |
| `Ctrl+X` `Y` | Copy the selection or the last assistant message. |
| `Ctrl+X` `E` | Open the current editor buffer in `externalEditor`, `$VISUAL`, `$EDITOR`, Notepad on Windows, or `nano` elsewhere. |
