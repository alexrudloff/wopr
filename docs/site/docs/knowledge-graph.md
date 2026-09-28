# Knowledge graph

WOPR is built from a small set of entities. This page lists each entity, where it lives, the command that inspects it, and how the entities relate. Use it to locate a concept before reading its page; each row links the page to read next. The same graph is published as JSON-LD at `docs/knowledge-graph/wopr-knowledge-graph.jsonld` and as Mermaid source in `docs/knowledge-graph/wopr.mmd` in the repository, and it ships inside every wopr binary as `knowledge-graph.md` in the local docs (`wopr docs show knowledge-graph`).

| Entity | What it is | Where it lives | Inspect with | Read |
|---|---|---|---|---|
| WOPR | The Go implementation; executable wopr. | `coding/version/version.go` | `wopr verify` | [index.md](index.md) |
| Session | Append-only JSONL tree of entries for one conversation. | `~/.wopr/agent/sessions` | `/tree, /session` | [sessions.md](sessions.md) |
| Model Runtime | Session-owned model lookup, auth, completion, and streaming used by every mode. | `coding/` | `/model, wopr --list-models` | [models.md](models.md) |
| Provider | Turns a Transcript into one Event Stream for one API. | `ai/` | `wopr --list-models` | [providers.md](providers.md) |
| Mode | Interactive TUI, print, JSON event stream, or RPC over stdin/stdout. | `cmd/wopr` | `wopr --mode rpc` | [cli.md](cli.md) |
| Resource | One capability: skill, prompt template, theme, or context file. | `~/.wopr/agent, .wopr/ in a project` | `wopr status --json` | [concepts.md](concepts.md) |

| From | Relation | To | Note |
|---|---|---|---|
| Session | owns | Model Runtime | one per Session |
| Model Runtime | streams through | Provider | one Provider per request |
| Mode | drives | Session | TUI, print, JSON, RPC share one Session path |

The graph source is `docs/knowledge-graph/wopr.graph.json`. Run `make knowledge-graph` after editing it.
