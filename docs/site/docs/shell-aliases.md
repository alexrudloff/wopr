# Shell Aliases

WOPR runs every shell command in a new, non-interactive Bash process. Non-interactive Bash does not expand aliases, and it does not read the startup files that your interactive terminal reads. An alias that works in your terminal therefore fails inside WOPR until you load it.

Two settings control this. `shellPath` chooses the Bash executable. `shellCommandPrefix` runs setup before every command.

## Which shell runs a command

| Command source | Shell |
|---|---|
| The model calls the built-in `bash` tool | The Bash executable that WOPR resolves |
| You type `!command` or `!!command` in the editor | The same Bash executable |
| An RPC client sends the `bash` command | The same Bash executable |
| The model calls the `powershell` tool on Windows | PowerShell |
| An MCP server runs its own shell | Whatever that tool runs |

WOPR starts Bash as `bash -c "<command>"`. It resolves the executable in this order:

1. `shellPath` from [settings](settings.md), if you set it. WOPR reports `Custom shell path not found` when the file does not exist.
2. On Linux and macOS: `/bin/bash`, then `bash` on your `PATH`, then `sh`.
3. On Windows: Git Bash under `Program Files`, then `bash.exe` on your `PATH`. See [Windows setup](windows.md).

WOPR does not use your `$SHELL`. The tools send Bash syntax, so a zsh or fish login shell is not a substitute.

## Choose a Bash executable

Set `shellPath` in `~/.wopr/agent/settings.json` to use a specific Bash:

```json
{
  "shellPath": "~/.local/bin/bash"
}
```

WOPR expands a leading `~/`. On Windows, write backslashes twice or use forward slashes:

```json
{
  "shellPath": "C:\\cygwin64\\bin\\bash.exe"
}
```

Run `/reload` after you change the setting.

## Run setup before every command

Set `shellCommandPrefix` to run shell code before each command. WOPR applies it to the `bash` tool, to `!` and `!!` commands, and to RPC `bash` commands:

```json
{
  "shellCommandPrefix": "export CI=1"
}
```

WOPR joins the prefix and the command with a newline. The prefix runs again for every command, so keep it fast and make sure it never waits for input.

## Enable aliases

Keep the aliases that WOPR needs in a small Bash file instead of loading your whole interactive configuration. Create `~/.bash_aliases`:

```bash
alias ll='ls -la'
alias gs='git status --short'
```

Turn on alias expansion and load the file:

```json
{
  "shellCommandPrefix": "shopt -s expand_aliases\nsource ~/.bash_aliases"
}
```

Run `/reload`, then test an alias from the editor:

```text
!ll
```

The output matches `ls -la`.

Write aliases in Bash syntax. Do not load `~/.zshrc` into Bash. Zsh options, functions and plugins often fail to parse in Bash, or they behave differently there.

## Troubleshooting

### The prefix works for `!` but not for another tool

`shellCommandPrefix` applies to WOPR's built-in Bash execution only. An MCP server that runs its own shell sets up its own environment. Read that tool's documentation.

### `shopt: command not found`

WOPR fell back to `sh`, or `shellPath` points to a shell that is not Bash. Install Bash, or set `shellPath` to a Bash executable.

### Every command hangs

A command in `shellCommandPrefix` waits for input. Remove interactive commands from the prefix.

### An alias works in the terminal but not in WOPR

Check that `shellCommandPrefix` turns on `expand_aliases` before it loads the alias file. Bash reads each alias definition before it runs the next line, so the alias must be defined on a line before the command that uses it. WOPR puts the prefix on its own lines before the command, which meets this rule.

## Related

- [Settings](settings.md) lists `shellPath` and `shellCommandPrefix`.
- [Windows setup](windows.md) covers the Windows shell defaults.
