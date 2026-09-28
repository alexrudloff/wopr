# TUI reliability

## Read first

- `../docs/project/CONTEXT.md`

## Contract

The Main Screen preserves scrollback. The Editor changes only the required visible rows. Frame work does not scale with unchanged Session history.

## Failure modes

| Failure | Detection | Required result |
|---|---|---|
| Ordinary editor shrink repaints the viewport | Delete one newline or wrap row | Use a local differential update |
| The viewport snaps backward | Compare viewport origin before and after shrink | Keep the viewport origin stable |
| A frame replays transcript history | Compare short and large Sessions | Keep frame work bounded |
| A stale or duplicate row remains | Render and compare rows | No stale or duplicate row |
| Scrollback is cleared | Detect `CSI 2J` and `CSI 3J` | Emit neither for an ordinary edit |
| The cursor homes | Detect `CSI H` | Use relative cursor movement |
| Wrapped input enters history early | Move Up within wrapped input | Move within the wrapped input before recalling history |
| Resize corrupts layout | Change width and height with ANSI, wide text, and images | No row exceeds the width |
| A hidden-row change cannot be repaired locally | Change content above the viewport | Use a full recovery render |

## Evidence

Reproduce a rendering bug in the terminal before changing renderer code. Do not add snapshot, golden, or exact-frame tests; a width or input-decoding invariant test is enough when a regression is likely to recur. Profile editor shrink, streaming, overlays, and large transcripts. Run `go test -race ./tui` and `make test-integration`.
