package coding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/mcp"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
	"github.com/alexrudloff/wopr/internal/codingagent/web"
)

// Web and MCP tools (see internal/codingagent/web and internal/codingagent/mcp).
// web_fetch and web_search are always installed (see session_websearch.go
// for how a search runs); mcp only when a server is configured.

// MCPSchemaCacheFileName holds the schemas of promoted MCP tools under the
// agent directory.
const MCPSchemaCacheFileName = "mcp-cache.json"

type sessionMCP struct {
	manager  *mcp.Manager
	problems []string
	// pending lists servers whose promoted tools are not cached yet.
	pending []string
}

// harnessToolAllowed reports whether the session options let a harness tool
// in: it counts as a built-in for --no-builtin-tools, the default active set,
// the allowlist and the denylist.
func harnessToolAllowed(opts SessionOptions, name string) bool {
	has := func(set map[string]struct{}) bool { _, ok := set[name]; return ok }
	return !opts.SkipBuiltinTools &&
		(opts.ActiveBuiltinTools == nil || has(opts.ActiveBuiltinTools)) &&
		(opts.AllowedTools == nil || has(opts.AllowedTools)) &&
		!has(opts.ExcludedTools)
}

// archive stores oversized tool results in the ObservationPack archive, or
// is nil when ObservationPack is off.
func (s *Session) archive() tools.Archive {
	if s.efficiency == nil || s.efficiency.pack == nil {
		return nil
	}
	return s.efficiency.pack.Archive
}

// initWebAndMCP installs web_fetch, web_search and the MCP tools. It runs
// after initEfficiency so large results can use the observation archive.
func (s *Session) initWebAndMCP(opts SessionOptions) {
	sm := s.services.SettingsManager()
	var extra []agent.AgentTool
	if harnessToolAllowed(opts, web.FetchToolName) || harnessToolAllowed(opts, web.SearchToolName) {
		cfg := web.ParseConfig(sm.RawSection("web"))
		if harnessToolAllowed(opts, web.FetchToolName) {
			fetch := web.NewFetchTool(cfg)
			fetch.Archive = s.archive()
			extra = append(extra, fetch)
		}
		if harnessToolAllowed(opts, web.SearchToolName) {
			search := web.NewSearchTool(cfg.Search, web.SearchLookup(os.Getenv, storedKey(s.services.Auth())))
			s.webSearch.tool = search
			extra = append(extra, search)
		}
	}
	if harnessToolAllowed(opts, mcp.ToolName) {
		global, project := sm.RawSection("mcpServers")
		cfg := mcp.ParseConfig(global, sm.ProjectMCPFile(), project)
		s.mcp = &sessionMCP{problems: cfg.Problems}
		if len(cfg.Names()) > 0 {
			manager := mcp.NewManager(cfg, s.services.CWD(), "")
			cache := mcp.NewSchemaCache(filepath.Join(s.services.AgentDir(), MCPSchemaCacheFileName))
			manager.OnTools = cache.Store
			proxy := mcp.NewProxyTool(manager)
			proxy.Archive = s.archive()
			extra = append(extra, proxy)
			direct, pending := mcp.DirectTools(manager, cache)
			for _, tool := range direct {
				tool.Archive = s.archive()
				extra = append(extra, tool)
			}
			s.mcp.manager, s.mcp.pending = manager, pending
			// Starting these servers now caches their promoted tools'
			// schemas for the next session.
			for _, name := range pending {
				go func() { _, _ = manager.Tools(context.Background(), name) }()
			}
		}
	}
	if len(extra) > 0 {
		s.tools = append(s.tools, extra...)
		s.agent.SetTools(append(s.agent.Tools(), extra...))
	}
}

// closeMCP stops the session's MCP servers.
func (s *Session) closeMCP() {
	if s.mcp != nil && s.mcp.manager != nil {
		s.mcp.manager.Close()
	}
}

// MCPCommand runs one /mcp subcommand and returns markdown: status (the
// default), tools <server>, or restart [server].
func (s *Session) MCPCommand(args string) (string, error) {
	fields := strings.Fields(args)
	verb := "status"
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}
	if s.mcp == nil {
		return "MCP is off in this session: the `mcp` tool is not in the active tool set.", nil
	}
	st := *s.mcp
	if st.manager == nil && verb != "status" && verb != "list" {
		return "", errors.New("no MCP servers are configured")
	}
	switch verb {
	case "status", "list":
		return mcpStatus(st), nil
	case "tools":
		if len(fields) < 2 {
			return "", errors.New("usage: /mcp tools <server>")
		}
		list, err := st.manager.Tools(context.Background(), fields[1])
		if err != nil {
			return "", err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "**%s** (%d tools)\n\n", fields[1], len(list))
		for _, tool := range list {
			desc, _, _ := strings.Cut(strings.TrimSpace(tool.Description), "\n")
			fmt.Fprintf(&b, "- `%s` %s\n", tool.Name, desc)
		}
		return b.String(), nil
	case "restart":
		name := ""
		if len(fields) > 1 {
			name = fields[1]
		}
		if err := st.manager.Restart(name); err != nil {
			return "", err
		}
		if name == "" {
			return "Stopped every MCP server; each starts again on its next use.", nil
		}
		return fmt.Sprintf("Stopped %s; it starts again on its next use.", name), nil
	}
	return "", fmt.Errorf("unknown /mcp subcommand %q; use status, tools <server> or restart [server]", verb)
}

func mcpStatus(st sessionMCP) string {
	var b strings.Builder
	if st.manager == nil {
		b.WriteString("No MCP servers are configured. Add `mcpServers` to settings.json or a trusted project's `.mcp.json`.\n")
	} else {
		b.WriteString("| Server | Transport | Source | State | Tools |\n| --- | --- | --- | --- | --- |\n")
		for _, server := range st.manager.Status() {
			state := server.State
			if server.Err != "" {
				desc, _, _ := strings.Cut(server.Err, "\n")
				state += ": " + strings.ReplaceAll(desc, "|", `\|`)
			}
			tools := "-"
			if server.State == mcp.StateReady {
				tools = fmt.Sprint(server.Tools)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", server.Name, server.Transport, server.Source, state, tools)
		}
		b.WriteString("\nServers start on first use; the model reaches them through the `mcp` tool.\n")
		if len(st.pending) > 0 {
			fmt.Fprintf(&b, "Promoted tools of %s become direct tools in the next session, once their schemas are cached.\n", strings.Join(st.pending, ", "))
		}
	}
	for _, problem := range st.problems {
		b.WriteString("\n**Config problem:** " + problem)
	}
	return b.String()
}
