# WOPR maintainer documentation

This directory contains WOPR's maintainer references and public documentation site. Public user documentation is authored under `site/docs/`. The same pages are embedded in the binary (`site/docs/embed.go`) and synced to `~/.wopr/docs` as the offline and agent reference.

Start here, choose one row, and read that focused document completely. Do not
load every document for an unrelated task.

## Task router

| If you are changing or deciding... | Read first | Then read |
|---|---|---|
| overall process/runtime boundaries and the embedding SDK | [Architecture](architecture.md) | `coding` package docs |
| model routing | [Routing](routing.md) | `internal/codingagent/router` |
| token efficiency | [Efficiency](efficiency.md) | `internal/codingagent/efficiency` |
| security boundaries, assets, threats, or mitigations | [Threat model](threat-model.md) | |

## Documentation rules

1. Keep one focused concept per file.
2. Put fields, commands, states, and compatibility matrices in tables.
3. Use prose for rationale, invariants, and failure boundaries.
4. Mark current and target behavior explicitly in design documents.
5. User docs describe shipped behavior only and change atomically with commands.
6. Link to direct neighbors and this router instead of repeating their content.
7. A removed embedded page must be pruned by `wopr docs sync` so stale files do
   not survive in `~/.wopr/docs`.
8. Keep code/JSON/records/API/UI/prose vocabulary identical and gate stale terms.

## Source of truth

- User documentation, also embedded in the binary as the agent reference: `site/docs/`.
- Project terms: [Project context](project/CONTEXT.md).
- Source lineage and third-party material: [Acknowledgments](../README.md#acknowledgments) and [Notices](../NOTICE).
- Embedding API: the `coding` package (`Services`, `Runtime`, `Session`).
