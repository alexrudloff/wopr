# serve-cli-doc — document `wopr serve` in `cli.md`

**Item:** `serve-cli-doc` (`BACKLOG.md` ~825). Doc-only gap: `docs/site/docs/cli.md`
has no row for the live `wopr serve` verb.

## Change (one file, one row)

Add a row to the **Generic subcommands** table in `docs/site/docs/cli.md`:

| Subcommand | Purpose |
|---|---|
| `wopr serve [--port N] [--hostname H]` | Start the HTTP/SSE server for the working directory. `--port` overrides `server.port` (default `4096`); `--hostname` overrides `server.hostname` (default `127.0.0.1`). Unauthenticated and no TLS; see [server](server.md). |

Placement: last row of the generic subcommands table (after `wopr docs`).

## Verified facts (against code, not memory)

- Dispatch: `cmd/wopr/main.go:228` runs `runServeCommand` before flag parsing.
- Flags: `cmd/wopr/serve.go:35-38` declares `--port` and `--hostname`;
  `serve.go:43-47` rejects an empty value; `serve.go:64-73` parses port as a
  positive int.
- Defaults: `internal/codingagent/settings.go:257-258` (`defaultServerPort = 4096`,
  `defaultServerHostname = "127.0.0.1"`); accessed at `settings.go:722-736`.
- Binding precedence: flag overrides setting (`serve.go:91-98`).
- `docs/site/docs/server.md` exists and `docs.json:30` classifies it; the
  `[server](server.md)` link is a plain same-directory Markdown link, which
  `tests/docs-drift` `TestInternalLinksResolve` checks.

## Definition of done

- The row exists in the Generic subcommands table and names both real flags,
  both defaults, and a working link to `server.md`.
- `go test ./tests/docs-drift/ -count=1` passes.
- No file other than `docs/site/docs/cli.md` and this spec changed.

## Out of scope

- `server.md`, README, `docs.json`.
- Any change to flags, defaults, or `serve.go`.
- Duplicating `server.md`'s endpoint table.

## Open questions

None. Table choice resolved: `serve` is a subcommand (dispatched in `main.go`,
not a `wopr [options] [prompt]` form), so it belongs in Generic subcommands.
