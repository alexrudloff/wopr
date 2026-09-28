package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// ToolName is the proxy tool's name.
const ToolName = "mcp"

// Output limits: a result longer than inlineLimit keeps a headBytes head in
// context and the rest in the observation archive.
const (
	inlineLimit      = 16 * 1024
	headBytes        = 8 * 1024
	listDescription  = 90
	directDescLimit  = 1024
	directNamePrefix = "mcp__"
)

// ProxyTool is the one tool through which the model reaches every server.
type ProxyTool struct {
	Manager *Manager
	// Archive, when set, stores oversized results for obs_recall.
	Archive tools.Archive
}

// NewProxyTool returns the proxy tool for m.
func NewProxyTool(m *Manager) *ProxyTool { return &ProxyTool{Manager: m} }

func (t *ProxyTool) Name() string  { return ToolName }
func (t *ProxyTool) Label() string { return "MCP" }

func (t *ProxyTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name:        ToolName,
		Description: "Use MCP server tools. Servers: " + strings.Join(t.Manager.Names(), ", ") + ". list shows tool names (tool=server adds descriptions); describe returns a tool's argument schema; call runs it with args.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"list", "describe", "call"}},
				"tool":   map[string]any{"type": "string", "description": "server/tool, or server for list"},
				"args":   map[string]any{"type": "object", "description": "Arguments for call"},
			},
			"required": []string{"action"},
		},
	}
}

// ExecutionMode is sequential: a server tool may change state.
func (t *ProxyTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }

type proxyParams struct {
	Action string         `json:"action"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
}

func (t *ProxyTool) Execute(ctx context.Context, toolCallID string, raw json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var p proxyParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return agent.AgentToolResult{}, fmt.Errorf("mcp: invalid params: %w", err)
	}
	server, tool := splitTool(p.Tool)
	switch p.Action {
	case "list":
		return textResult(t.list(ctx, server)), nil
	case "describe":
		if tool == "" {
			return agent.AgentToolResult{}, errors.New("describe needs tool as server/tool")
		}
		spec, err := t.lookup(ctx, server, tool)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		return textResult(describe(server, spec)), nil
	case "call":
		if tool == "" {
			return agent.AgentToolResult{}, errors.New("call needs tool as server/tool")
		}
		return callTool(ctx, t.Manager, t.Archive, server, tool, p.Args, toolCallID)
	}
	return agent.AgentToolResult{}, fmt.Errorf("unknown action %q; use list, describe or call", p.Action)
}

// splitTool accepts server/tool, mcp__server__tool, or a bare server name.
func splitTool(s string) (server, tool string) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, directNamePrefix); ok {
		if server, tool, ok := strings.Cut(rest, "__"); ok {
			return server, tool
		}
	}
	server, tool, _ = strings.Cut(s, "/")
	return server, tool
}

func (t *ProxyTool) lookup(ctx context.Context, server, tool string) (*sdk.Tool, error) {
	list, err := t.Manager.Tools(ctx, server)
	if err != nil {
		return nil, err
	}
	return findTool(list, server, tool)
}

// list reports every server's tool names, or one server's tools with short
// descriptions. Servers start in parallel.
func (t *ProxyTool) list(ctx context.Context, server string) string {
	if server != "" {
		list, err := t.Manager.Tools(ctx, server)
		if err != nil {
			return err.Error()
		}
		var b strings.Builder
		for _, tool := range list {
			fmt.Fprintf(&b, "%s/%s: %s\n", server, tool.Name, firstLine(tool.Description, listDescription))
		}
		if len(list) == 0 {
			return server + ": no tools"
		}
		return strings.TrimRight(b.String(), "\n")
	}
	names := t.Manager.Names()
	lines := make([]string, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Go(func() {
			list, err := t.Manager.Tools(ctx, name)
			if err != nil {
				lines[i] = name + ": unavailable: " + firstLine(err.Error(), 200)
				return
			}
			lines[i] = name + ": " + orNone(toolNames(list))
		})
	}
	wg.Wait()
	if len(lines) == 0 {
		return "No MCP servers are configured."
	}
	return strings.Join(lines, "\n")
}

func describe(server string, spec *sdk.Tool) string {
	schema, _ := json.Marshal(cleanSchema(spec.InputSchema))
	return fmt.Sprintf("%s/%s: %s\nargs schema: %s", server, spec.Name, strings.TrimSpace(spec.Description), schema)
}

// firstLine returns s's first line, cut to max bytes.
func firstLine(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > maxLen {
		cut := maxLen
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		s = strings.TrimSpace(s[:cut]) + "…"
	}
	return s
}

// cleanSchema returns an input schema as a map without its $schema key, the
// form providers accept for tool parameters.
func cleanSchema(schema any) map[string]any {
	var out map[string]any
	switch v := schema.(type) {
	case map[string]any:
		out = v
	default:
		data, err := json.Marshal(v)
		if err == nil {
			_ = json.Unmarshal(data, &out)
		}
	}
	if out == nil {
		return map[string]any{"type": "object"}
	}
	if _, ok := out["$schema"]; ok {
		out = maps.Clone(out)
		delete(out, "$schema")
	}
	return out
}

func textResult(text string) agent.AgentToolResult { return agent.AgentToolResult{Content: text} }

// callTool runs one server tool and maps its result.
func callTool(ctx context.Context, m *Manager, archive tools.Archive, server, tool string, args map[string]any, key string) (agent.AgentToolResult, error) {
	if args == nil {
		args = map[string]any{}
	}
	result, err := m.Call(ctx, server, tool, args)
	if err != nil {
		var rpcErr *jsonrpc.Error
		if (errors.As(err, &rpcErr) && rpcErr.Code == jsonrpc.CodeInvalidParams) || badArguments.MatchString(err.Error()) {
			err = fmt.Errorf("%w\n%s", err, schemaHint(ctx, m, server, tool))
		}
		return agent.AgentToolResult{}, err
	}
	text, images := renderResult(result)
	if result.IsError && badArguments.MatchString(text) {
		text += "\n" + schemaHint(ctx, m, server, tool)
	}
	text = tools.ClipForContext(text, inlineLimit, headBytes, archive, "mcp:"+server+"/"+tool, key)
	return agent.AgentToolResult{
		Content: text,
		Images:  images,
		IsError: result.IsError,
		Details: map[string]any{"server": server, "tool": tool},
	}, nil
}

// badArguments matches server messages about invalid arguments.
var badArguments = regexp.MustCompile(`(?i)(invalid|missing|required|validat|unexpected)[^\n]*(argument|param|propert|field)`)

// schemaHint returns the tool's schema, which saves the model a describe
// round trip after a bad-arguments error.
func schemaHint(ctx context.Context, m *Manager, server, tool string) string {
	list, err := m.Tools(ctx, server)
	if err != nil {
		return ""
	}
	if spec, err := findTool(list, server, tool); err == nil {
		return describe(server, spec)
	}
	return ""
}

// renderResult maps MCP content to wopr text and images.
func renderResult(result *sdk.CallToolResult) (string, []ai.ImageContent) {
	var parts []string
	var images []ai.ImageContent
	for _, content := range result.Content {
		switch c := content.(type) {
		case *sdk.TextContent:
			parts = append(parts, c.Text)
		case *sdk.ImageContent:
			images = append(images, ai.ImageContent{Data: base64.StdEncoding.EncodeToString(c.Data), MimeType: c.MIMEType})
		case *sdk.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio %s, %d bytes, not shown]", c.MIMEType, len(c.Data)))
		case *sdk.ResourceLink:
			parts = append(parts, fmt.Sprintf("[resource %s: %s]", c.Name, c.URI))
		case *sdk.EmbeddedResource:
			if c.Resource == nil {
				continue
			}
			if c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else {
				parts = append(parts, fmt.Sprintf("[resource %s (%s), %d bytes, not shown]", c.Resource.URI, c.Resource.MIMEType, len(c.Resource.Blob)))
			}
		}
	}
	if len(parts) == 0 && result.StructuredContent != nil {
		if data, err := json.Marshal(result.StructuredContent); err == nil {
			parts = append(parts, string(data))
		}
	}
	text := strings.Join(parts, "\n")
	if text == "" && len(images) == 0 {
		text = "(no output)"
	}
	return text, images
}

// DirectTool exposes one server tool as a first-class tool.
type DirectTool struct {
	Manager *Manager
	Server  string
	Spec    CachedTool
	Archive tools.Archive
}

// DirectToolName is the first-class name of a server tool.
func DirectToolName(server, tool string) string { return directNamePrefix + server + "__" + tool }

func (t *DirectTool) Name() string  { return DirectToolName(t.Server, t.Spec.Name) }
func (t *DirectTool) Label() string { return t.Server + "/" + t.Spec.Name }

func (t *DirectTool) Schema() ai.ToolSchema {
	desc := strings.TrimSpace(t.Spec.Description)
	if len(desc) > directDescLimit {
		desc = firstLine(desc, directDescLimit)
	}
	return ai.ToolSchema{Name: t.Name(), Description: desc, Parameters: cleanSchema(t.Spec.InputSchema)}
}

func (t *DirectTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }

func (t *DirectTool) Execute(ctx context.Context, toolCallID string, raw json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var args map[string]any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return agent.AgentToolResult{}, fmt.Errorf("%s: invalid params: %w", t.Name(), err)
		}
	}
	return callTool(ctx, t.Manager, t.Archive, t.Server, t.Spec.Name, args, toolCallID)
}
