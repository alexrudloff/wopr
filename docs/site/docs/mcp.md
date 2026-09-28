# MCP servers

WOPR connects to Model Context Protocol servers over stdio and streamable HTTP (with a fallback to the older SSE transport). The model reaches every server through one `mcp` tool, so a server's tool schemas never ride on every request: the model lists tools, reads one tool's schema when it needs it, and calls it.

## Configure servers

Add `mcpServers` to `~/.wopr/agent/settings.json`, to a project's `.wopr/settings.json`, or put it in a project `.mcp.json`. The format is the one Claude Code, Cursor and opencode use, so an existing file works unchanged:

```json
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "${GITHUB_TOKEN}" }
    },
    "docs": {
      "url": "https://example.com/mcp",
      "headers": { "Authorization": "Bearer ${DOCS_TOKEN}" }
    }
  }
}
```

| Field | Meaning |
|---|---|
| `command`, `args`, `env`, `cwd` | Start a stdio server. `command` may also be an argv array. |
| `url`, `headers` | Connect to a streamable HTTP server. `"type": "sse"` selects the older SSE transport. |
| `disabled` | Keep the entry without starting it (`"enabled": false` works too). |
| `timeout`, `startTimeout` | Milliseconds for one tool call (default 120000) and for start plus handshake (default 60000). |
| `directTools` | Tool names to expose as first-class tools, named `mcp__<server>__<tool>`. |

`${VAR}` and `${VAR:-default}` expand from the environment in `command`, `args`, `env`, `cwd`, `url` and `headers`. Project entries replace global entries of the same name; project settings win over `.mcp.json`.

## Security

A project's `.mcp.json` and `.wopr/settings.json` are read only when the project is trusted, and a `.mcp.json` makes WOPR ask for trust. A stdio server inherits only `PATH`, `HOME`, the locale, the temp directory, the proxy variables and a few platform essentials. Provider API keys and other variables stay out unless the server's `env` names them.

## How the model uses servers

The `mcp` tool takes an `action`:

- `list`: every server's tool names; with `tool` set to a server, each tool with a one-line description.
- `describe`: one tool's argument schema, as `tool: "server/tool"`.
- `call`: runs `server/tool` with `args`. A bad-arguments error returns the schema, which saves a `describe` round trip.

A server starts on first use, not at startup. A server that crashes reports its exit and last stderr lines, then starts again on the next call. An interrupt cancels the call. Results larger than 16 KB keep an 8 KB head in context and the rest in the observation archive, readable with `obs_recall`.

A promoted tool in `directTools` costs its schema on every request, so promote only tools the model calls often. Its schema is cached in `~/.wopr/agent/mcp-cache.json` the first time the server starts, and the tool appears from the next session on.

## The /mcp command

`/mcp` shows each server's transport, source, state and tool count. `/mcp tools <server>` lists one server's tools. `/mcp restart [server]` stops a server, or all of them, so the next use starts it fresh.
