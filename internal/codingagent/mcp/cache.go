package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// CachedTool is a tool schema remembered for directTools, so a promoted tool
// can be declared at session start without starting its server.
type CachedTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	InputSchema any    `json:"inputSchema"`
}

type cachedServer struct {
	Key   string       `json:"key"`
	Tools []CachedTool `json:"tools"`
}

// SchemaCache persists the schemas of promoted tools in one JSON file.
type SchemaCache struct {
	path string
	mu   sync.Mutex
}

// NewSchemaCache returns the cache stored at path.
func NewSchemaCache(path string) *SchemaCache { return &SchemaCache{path: path} }

// configKey identifies a server's launch configuration; a changed command,
// url or tool selection invalidates its cached schemas.
func configKey(cfg ServerConfig) string {
	data, _ := json.Marshal(struct {
		Command, URL, Type string
		Args, Direct       []string
	}{cfg.Command, cfg.URL, cfg.Type, cfg.Args, cfg.DirectTools})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func (c *SchemaCache) read() map[string]cachedServer {
	out := map[string]cachedServer{}
	if data, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

// Lookup returns the cached promoted tools of server, in directTools order,
// and whether every promoted tool was found.
func (c *SchemaCache) Lookup(server string, cfg ServerConfig) ([]CachedTool, bool) {
	c.mu.Lock()
	entry, ok := c.read()[server]
	c.mu.Unlock()
	if !ok || entry.Key != configKey(cfg) {
		return nil, false
	}
	var out []CachedTool
	for _, name := range cfg.DirectTools {
		i := slices.IndexFunc(entry.Tools, func(t CachedTool) bool { return t.Name == name })
		if i < 0 {
			return out, false
		}
		out = append(out, entry.Tools[i])
	}
	return out, true
}

// Store records the promoted tools among a server's fetched tools. It is
// Manager.OnTools.
func (c *SchemaCache) Store(server string, cfg ServerConfig, tools []*sdk.Tool) {
	if len(cfg.DirectTools) == 0 {
		return
	}
	entry := cachedServer{Key: configKey(cfg)}
	for _, tool := range tools {
		if slices.Contains(cfg.DirectTools, tool.Name) {
			entry.Tools = append(entry.Tools, CachedTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	all := c.read()
	all[server] = entry
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, c.path)
	}
}

// DirectTools returns the promoted tools whose schemas are cached, and the
// servers whose promoted tools are not all cached yet. Warming those servers
// fills the cache for the next session.
func DirectTools(m *Manager, cache *SchemaCache) (direct []*DirectTool, pending []string) {
	for _, name := range m.Names() {
		cfg := m.cfg.Servers[name]
		if len(cfg.DirectTools) == 0 {
			continue
		}
		cached, complete := cache.Lookup(name, cfg)
		for _, spec := range cached {
			direct = append(direct, &DirectTool{Manager: m, Server: name, Spec: spec})
		}
		if !complete {
			pending = append(pending, name)
		}
	}
	return direct, pending
}
