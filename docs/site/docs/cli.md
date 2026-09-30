# Command line

This page lists the `wopr` command-line verbs and subcommands, and the `wopr docs` commands for the embedded reference bundle.

## CLI verbs

```
wopr [options] [prompt]
```

| Verb | Purpose |
|---|---|
| `wopr` | Start interactive TUI in current directory. |
| `wopr --print <prompt>` | One-shot: send prompt, print response, exit. |
| `wopr --model <provider/model>` | Override model for this run. |
| `wopr --skill <path>` | Load a Skill file or directory. Repeat the option for several Skills. |
| `wopr --session-id <id>` | Use an exact session ID; may be combined with `--no-session` for provider cache affinity without disk persistence. |
| `wopr --mode rpc` | Start the JSONL RPC command loop on stdin/stdout. |
| `wopr --version` | Print the WOPR version. |
| `wopr version` | Detailed wopr/Go/platform/build banner. |

## Generic subcommands

| Subcommand | Purpose |
|---|---|
| `wopr setup` | Open the interactive UI on the setup screen, as `/setup` does. |
| `wopr login [provider]` | Authenticate an OAuth provider. Credentials go to `auth.json`. |
| `wopr logout [provider]` | Remove OAuth credentials from `auth.json`, as TUI `/logout` does. |
| `wopr auth check --provider <provider> [--model <model>] [--json] [--credentials] [--no-refresh]` | Print `ready`, `not_ready`, or `invalid` and exit 0, 1, or 2. `--json` writes the structured result; `--credentials` emits the resolved credential when ready. Expired OAuth credentials are refreshed unless `--no-refresh` is given, which also leaves `auth.json` and its directory untouched. |
| `wopr auth print-api-key --provider <provider> [--model <model>]` | Print the resolved API key for an external client. Refuses a provider configured with OAuth. |
| `wopr auth print-bearer-token --provider <provider> [--model <model>] [--min-expiry <duration>]` | Print an OAuth bearer token, refreshing it when less than `--min-expiry` (default `30m`; units `ms`, `s`, `m`, `h`) remains. Refuses a provider configured with an API key. |
| `wopr update [self\|wopr] [--force]` | Update the wopr binary. `--force` reinstalls when the version is current. |
| `wopr status [--json]` | Side-effect-free overview: Resource health, canonical paths, providers with stored credentials, and the default model; invalid state exits non-zero. |
| `wopr login --list [--json]` | List OAuth authentication targets without reading credentials. |
| `wopr config [--local]` | Open the Resource filter TUI. Press Tab to switch global and project scope. |
| `wopr verify [--json] [--checksums <file>] [--provenance] [path...]` | Verify this binary or downloaded files by SHA-256 digest, optionally against a SHA256SUMS file and GitHub build provenance. |
| `wopr docs [sync\|path\|list\|show <name>]` | Materialize and read the documentation bundled with WOPR. |

## Options

These options apply to `wopr [options] [prompt]`. Run `wopr --help` for the full list.

| Option | Purpose |
|---|---|
| `--provider <name>`, `--api-key <key>` | Select a provider and pass its API key for this run. |
| `--thinking <level>` | Set the thinking level: `off`, `minimal`, `low`, `medium`, `high`, `xhigh`, or `max`. |
| `--system-prompt <text>` | Replace the system prompt. |
| `--append-system-prompt <text>` | Append text or a file's contents to the system prompt. Repeat it to append more. |
| `--continue`, `-c` | Continue the previous session. |
| `--resume`, `-r` | Select a session to resume. |
| `--session <path\|id>` | Use a session file or a partial session UUID. |
| `--fork <path\|id>` | Fork a session file or partial UUID into a new session. |
| `--session-dir <dir>` | Directory for session storage and lookup. |
| `--no-session` | Do not save the session. |
| `--name`, `-n <name>` | Set the session display name. |
| `--no-skills`, `-ns` | Turn off skill discovery and loading. |
| `--prompt-template <path>` | Load a prompt template file or directory. Repeat it for more. |
| `--no-prompt-templates`, `-np` | Turn off prompt template discovery and loading. |
| `--theme <path>` | Load a theme file or directory. Repeat it for more. |
| `--use-theme <name>` | Set the initial interactive theme for this run. |
| `--no-themes` | Turn off theme discovery and loading. |
| `--no-context-files`, `-nc` | Do not load `AGENTS.md` or `CLAUDE.md` files. |
| `--export <file> [output]` | Export a session file to HTML and exit. |
| `--list-models [search]` | List available models, with optional fuzzy search. |
| `--verbose` | Force verbose startup, overriding `quietStartup`. |
| `--approve`, `-a` | Trust project-local files for this run. |
| `--no-approve`, `-na` | Ignore project-local files for this run. |
| `--offline` | Turn off startup network operations, as `WOPR_OFFLINE=1` does. |
| `--gtw` | In print or JSON mode, run the session in Global Thermonuclear War, as `WOPR_GTW=1` does: the top model in your routing order at its maximum thinking, routing off, and the war council on. Needs model routing set up. |

## Tools

```bash
wopr --tools read,grep,find,ls -p "Review the code in src/"
```

The tool options select the tools the model can call for one run. See [Settings](settings.md#tools) to change the default selection.

| Option | Effect |
|---|---|
| `--tools`, `-t <list>` | Replace the default selection with a comma-separated allowlist. It applies to built-in and custom tools. |
| `--exclude-tools`, `-xt <list>` | Turn off the named tools after all other selection options. It applies to built-in and custom tools. An excluded tool cannot be called. |
| `--no-builtin-tools`, `-nbt` | Turn off the default built-in tools and keep custom tools. |
| `--no-tools`, `-nt` | Start with every built-in and custom tool turned off. |

Without these options, WOPR enables `read`, `bash`, `edit`, and `write`, unless the `defaultTools` setting changes the set.

| Built-in tool | Purpose | On by default |
|---|---|---|
| `read` | Read text files and supported images | yes |
| `bash` | Run shell commands | yes |
| `powershell` | Run PowerShell commands on Windows | no |
| `edit` | Replace exact text in an existing file | yes |
| `write` | Create or overwrite a file | yes |
| `grep` | Search file contents | no |
| `find` | Find paths by glob pattern | no |
| `ls` | List directory contents | no |

For a read-only session, allow only the tools that cannot change files:

```bash
wopr --tools read,grep,find,ls
```

To keep the default set without one tool, exclude it:

```bash
wopr --exclude-tools bash
```

## `wopr docs`

WOPR materializes its reference docs into `~/.wopr/docs/` so the coding
agent can read the API implemented by the running binary.

```
wopr docs              # sync + summary (default)
wopr docs sync         # force re-sync
wopr docs path         # print docs directory
wopr docs list         # list available files
wopr docs show <name>  # print one doc to stdout
```

`EnsureSynced` runs at startup. A content digest marker prevents redundant
writes on warm starts and updates the materialized copy when embedded content
changes.
