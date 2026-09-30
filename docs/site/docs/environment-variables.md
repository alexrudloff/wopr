# Environment variables

WOPR reads these variables at startup. Each one has a setting or a default that
covers the normal case, so set a variable only when you need to change WOPR from
outside its configuration: in a container, in CI, or while debugging.

## Location

| Variable | Effect |
|---|---|
| `WOPR_HOME` | Configuration root. Default `~/.wopr` |
| `XDG_CONFIG_HOME` | When `WOPR_HOME` is not set, the configuration root is `$XDG_CONFIG_HOME/wopr` |
| `WOPR_CODING_AGENT_DIR` | Agent directory alone. Default `$WOPR_HOME/agent` |

`WOPR_HOME` moves settings, keybindings, sessions, trust decisions and installed
packages together. Use it to run two configurations side by side.

## Network

| Variable | Effect |
|---|---|
| `WOPR_OFFLINE` | Do not check GitHub for a newer wopr release at startup. Accepts `1`, `true` or `yes` |
| `WOPR_GTW` | `1` runs a print or JSON session in Global Thermonuclear War, like `--gtw` |
| `WOPR_OAUTH_CALLBACK_HOST` | Host the OAuth callback listens on |

Set `WOPR_OFFLINE` in CI. Without it, every run checks for updates and fails
slowly when the network is blocked.

## Diagnostics

| Variable | Effect |
|---|---|
| `WOPR_DEBUG` | Write a debug log |
| `WOPR_DEBUG_KEYS` | Log every key as WOPR decodes it |
| `WOPR_DEBUG_TOOLS` | Print the number of tools sent with each OpenAI Chat Completions request to stderr. Needs the exact value `1` |
| `WOPR_STARTUP_TRACE` | Time each startup step |
| `WOPR_PROFILE` | Write Go runtime profiles when WOPR exits. Takes a comma-separated list of `cpu`, `heap`, `allocs`, `block`, `mutex`, `goroutine` and `trace` |
| `WOPR_PROFILE_DIR` | Directory for `WOPR_PROFILE` output. Default: the working directory |

Use `WOPR_DEBUG_KEYS` when a keybinding does not respond: it shows whether the key
reached WOPR at all. See [terminal setup](terminal-setup.md).

`WOPR_DEBUG_TOOLS` needs the exact value `1`. The other diagnostic variables
accept any non-empty value.

## Sessions

| Variable | Effect |
|---|---|
| `WOPR_CODING_AGENT_SESSION_DIR` | Directory for session storage and lookup. `--session-dir` overrides it |

## Updates and deployments

These variables describe how WOPR was installed, so `wopr update` can give the right
instruction. A product deployment sets them. You do not need them for a normal
install.

| Variable | Effect |
|---|---|
| `WOPR_INSTALL_TIER` | Declare the installation type when WOPR cannot detect it: `container`, `image`, or `immutable-binary`. Any other value is an error |
| `WOPR_IMAGE_REF` | Image reference that `wopr update` names for a container or image install |
| `WOPR_IMAGE_PULL_CMD` | Pull command that `wopr update` prints for a container or image install |
| `WOPR_REDEPLOY_INSTRUCTION` | Exact redeploy instruction that `wopr update` prints for a container or image install |

## Display

| Variable | Effect |
|---|---|
| `WOPR_HARDWARE_CURSOR` | Show the terminal's own cursor |
| `WOPR_HYPERLINKS` | Override OSC 8 hyperlink detection with `1`, `0`, or `auto` |
| `WOPR_IMAGE_PROTOCOL` | Override inline image detection with `kitty`, `iterm2`, `none`, or `auto` |
| `WOPR_TRUE_COLOR` | Override true-color detection with `1`, `0`, or `auto`. With true color off, themes use the 256-color palette |

Each has a setting that does the same thing. A `terminal.hyperlinks`,
`terminal.images` or `terminal.trueColor` setting wins over its variable. See [settings](settings.md).

## Process markers

WOPR sets two markers that every child process inherits:

| Variable | Effect |
|---|---|
| `AI_AGENT=wopr` | Generic marker that lets tooling identify the launching agent |
| `WOPR_CODING_AGENT=true` | Lets a child process detect that it runs inside the coding agent |

## Related

- [Providers](providers.md) lists the credential variables for each provider.
- [Settings](settings.md) covers the same behavior in configuration.
- [Settings](settings.md) lists the configuration files and keys.
- [Terminal setup](terminal-setup.md) covers display and key problems.
