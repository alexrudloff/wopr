# Threat model

## Scope

This model covers WOPR, its local configuration and caches, self-update, and published release artifacts.

Programs that embed WOPR and deployment platforms have separate threat models. Their credentials, APIs, policy, and infrastructure are outside WOPR.

## Assets

WOPR can access assets available to the user who starts it:

- source code and workspace files;
- credentials in supported local stores and environment variables;
- model prompts and responses;
- session history;
- skill, prompt template, and theme source;
- downloaded or built artifacts; and
- update and release metadata.

## Trust boundaries

### User process boundary

WOPR runs with the user's operating-system permissions. WOPR does not isolate model output or built-in tools from the user account.

### Workspace and configuration boundary

Files in the workspace, `WOPR_HOME`, `WOPR_CODING_AGENT_DIR`, shell environment, and loaded context can influence WOPR. Treat writable content in these locations as trusted input unless the process runs inside a stronger sandbox.

### Resource boundary

Skills, prompt templates, and project settings can influence model behavior. Project discovery requires explicit trust.

### Network boundary

Model providers, catalogs, and update endpoints are external systems. TLS validation is enabled by default. Credentials must not appear in URLs, logs, command-line arguments, release records, or SBOMs.

### Remote serve boundary

`wopr serve` exposes an unauthenticated HTTP and SSE API on a local port for an opencode-compatible phone client. A served session is a remote shell for anyone who can reach the port: the server has no authentication, the process runs as the user, and every route that runs a tool or reads a file is reachable by anyone who can open the socket. A `wopr serve` process serves one launch-time root, and a client's `directory` query only validates against it.

Binding to a loopback address is the default and keeps the socket off the network. A non-loopback bind is not itself a security control; it is trusted to a trusted network only. The only confinement on the file routes is the resolved-path allowlist: `GET /file` and `GET /file/content` are confined to the resolved project root (`resolveInRoot`, `serve_dto.go:159-192`; `os.OpenInRoot`), so a path that leaves the root, including through a symlink, is refused. The tool routes have no such confinement.

Accepted risks for this boundary: no authentication, no TLS, no CORS policy, no rate limiting, no audit log; SSE has no `Last-Event-ID` replay, so a dropped stream does not resume; and one server process serves one working directory.

### Release boundary

Release artifacts cross from protected build infrastructure to users. Checksums, signatures, provenance, and artifact-specific SBOMs bind each published file to the reviewed source commit and workflow.

## Threats and controls

| Threat | Control |
|---|---|
| Untrusted project instructions cause unsafe actions | Trust prompt before loading project resources; document that prompt injection is inside the selected trust boundary |
| Dependency or build input changes without review | Pin manifests, lock files, toolchains, actions, and image bases; run dependency review |
| Vulnerable dependency ships | Run ecosystem scanners; review reachability; bind dispositions to the exact artifact |
| Secret enters source or release output | Run Gitleaks and TruffleHog; inspect generated evidence; prohibit credentials in configuration examples |
| Update replaces the wrong executable or accepts incomplete data | Prove one installation owner; verify size and checksum; perform atomic replacement; do not fall through after a tier starts |
| Published artifact differs from reviewed source | Build in a protected workflow; emit checksums, SBOMs, signatures, and provenance |
| Background work (tools, subagents, MCP servers) outlives its owner | Bind goroutine lifetime to a context; drain shutdown; report errors |
| Background work blocks TUI input or mutates UI off-loop | Keep blocking work off the input/render loop; marshal mutations through the owned UI path |
| Anyone reaching the serve port can read files or run tools | Bind loopback by default; resolve every file path against the project root; answer `501` for unfinished mutating routes |
| A served session overlaps or outlives another writer | The host owns one run per session; a second `prompt_async` is queued to run next, and a request while an `ask_user` question is pending is rejected with `ErrSessionBusy` (409) |
| SBOM omits or misidentifies a component | Reconcile scanner output with manifests, locks, embedded assets, generated data, and archive contents |

## Security assumptions

- The operating system and user account are trusted.
- The user reviews a source before granting project trust.
- A model response is untrusted input.
- External services can fail, delay, or return malformed data.
- A checksum without an authenticated release identity is insufficient.
- A scanner finding is not a final security or licensing decision.
- The server binds loopback unless the operator deliberately sets `server.hostname` or `--hostname`; a non-loopback bind is trusted to a trusted network only and is not itself a security control.

## Review triggers

Review this model when a change adds or changes:

- an executable input source;
- credential storage or authentication;
- network transport;
- archive extraction;
- self-update;
- sandbox or process isolation;
- release publication;
- telemetry; or
- a new distributed artifact type.

