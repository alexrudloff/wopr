# Session runtime reliability

## Read first

- `../docs/project/CONTEXT.md`

## Contract

One Session owns one Model Runtime. TUI, print, JSON, and RPC use that runtime. Session replacement cancels and drains prior work. Persistence, paging, compaction, and reconciliation preserve order and identity.

## Failure modes

| Failure | Detection | Required result |
|---|---|---|
| A mode uses a different model path | Run one request in every mode | Match request, events, and result |
| Session replacement leaks prior work | Replace an active Session | Cancel and drain the prior runtime |
| Paging loses or duplicates an append | Append during page transfer | Deliver each entry once, in order |
| Resume scans or copies full history on input | Measure editor and command paths on a large Session | Keep work bounded |
| Compaction races with queued input | Queue input during compaction | Deliver queued input after compaction, in order |
| Poisoned history breaks a later turn | Load aborted, errored, empty, and orphaned entries | Recover or fail at the owning boundary |
| Shutdown leaves work or files open | Cancel during provider and tool activity | Drain owned work and close resources |

## Evidence

Persistence, resume, fork, and compaction keep a small behavioral test each, because a regression loses data. Do not pin exact event sequences or formatting. Measure latency, allocations, and memory when a change touches large Sessions.
