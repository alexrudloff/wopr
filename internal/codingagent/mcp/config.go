// Package mcp is wopr's Model Context Protocol client. Configured servers are
// reached through one proxy tool (mcp) whose list, describe and call actions
// load server tool schemas on demand, so no server schema rides on every
// request. Servers start on first use. A server's frequently used tools can be
// promoted to first-class tools with directTools.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Transport kinds.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
	TransportSSE   = "sse"
)

// Default timeouts.
const (
	DefaultStartTimeout = 60 * time.Second
	DefaultCallTimeout  = 120 * time.Second
)

// ServerConfig is one entry of mcpServers. The shape is the one Claude Code,
// Cursor and opencode share: command/args/env for stdio, url/headers for
// streamable HTTP. opencode's command arrays, "environment", "local"/"remote"
// and "enabled" spellings are accepted too.
type ServerConfig struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Disabled keeps an entry in the file without starting it.
	Disabled bool `json:"disabled,omitempty"`
	// TimeoutMs bounds one tool call; StartTimeoutMs bounds start plus
	// initialization.
	TimeoutMs      int `json:"timeout,omitempty"`
	StartTimeoutMs int `json:"startTimeout,omitempty"`
	// DirectTools names this server's tools to expose as first-class tools
	// (mcp__<server>__<tool>). Each costs its schema on every request.
	DirectTools []string `json:"directTools,omitempty"`
	// Source records where the entry came from: global, .mcp.json or project.
	Source string `json:"-"`
}

// UnmarshalJSON accepts command as a string or an argv array and the
// opencode aliases.
func (c *ServerConfig) UnmarshalJSON(data []byte) error {
	type plain ServerConfig
	var wire struct {
		plain
		Command     json.RawMessage   `json:"command,omitempty"`
		Environment map[string]string `json:"environment,omitempty"`
		Enabled     *bool             `json:"enabled,omitempty"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*c = ServerConfig(wire.plain)
	if len(wire.Command) > 0 {
		var cmd string
		var argv []string
		switch {
		case json.Unmarshal(wire.Command, &cmd) == nil:
			c.Command = cmd
		case json.Unmarshal(wire.Command, &argv) == nil && len(argv) > 0:
			c.Command = argv[0]
			c.Args = append(slices.Clone(argv[1:]), c.Args...)
		default:
			return errors.New("command must be a string or a non-empty array of strings")
		}
	}
	if c.Env == nil && wire.Environment != nil {
		c.Env = wire.Environment
	}
	if wire.Enabled != nil && !*wire.Enabled {
		c.Disabled = true
	}
	return nil
}

// Transport reports the server's transport kind.
func (c ServerConfig) Transport() string {
	switch strings.ToLower(c.Type) {
	case "sse":
		return TransportSSE
	case "http", "streamable-http", "streamablehttp", "remote":
		return TransportHTTP
	case "stdio", "local":
		return TransportStdio
	}
	if c.URL != "" && c.Command == "" {
		return TransportHTTP
	}
	return TransportStdio
}

func (c ServerConfig) validate() error {
	if c.Transport() == TransportStdio {
		if c.Command == "" {
			return errors.New("stdio server has no command")
		}
		return nil
	}
	if c.URL == "" {
		return errors.New("HTTP server has no url")
	}
	return nil
}

func (c ServerConfig) callTimeout() time.Duration {
	if c.TimeoutMs > 0 {
		return time.Duration(c.TimeoutMs) * time.Millisecond
	}
	return DefaultCallTimeout
}

func (c ServerConfig) startTimeout() time.Duration {
	if c.StartTimeoutMs > 0 {
		return time.Duration(c.StartTimeoutMs) * time.Millisecond
	}
	return DefaultStartTimeout
}

// Config is the merged server set.
type Config struct {
	Servers map[string]ServerConfig
	// Problems lists entries that were skipped and why.
	Problems []string
}

// Names returns the enabled server names, sorted.
func (c Config) Names() []string {
	var names []string
	for name, server := range c.Servers {
		if !server.Disabled {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

var serverNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ParseConfig merges the mcpServers sections in increasing precedence:
// global settings, the project .mcp.json, then project settings. A later
// layer replaces an earlier entry of the same name. Each layer is either the
// mcpServers object itself or a whole file that contains one.
func ParseConfig(global, projectFile, project json.RawMessage) Config {
	cfg := Config{Servers: map[string]ServerConfig{}}
	for _, layer := range []struct {
		source string
		raw    json.RawMessage
	}{{"global", global}, {".mcp.json", projectFile}, {"project", project}} {
		if len(layer.raw) == 0 {
			continue
		}
		servers, err := decodeServers(layer.raw)
		if err != nil {
			cfg.Problems = append(cfg.Problems, fmt.Sprintf("%s mcpServers: %v", layer.source, err))
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(servers)) {
			var server ServerConfig
			if err := json.Unmarshal(servers[name], &server); err != nil {
				cfg.Problems = append(cfg.Problems, fmt.Sprintf("%s server %q: %v", layer.source, name, err))
				continue
			}
			if !serverNameRE.MatchString(name) {
				cfg.Problems = append(cfg.Problems, fmt.Sprintf("%s server %q: names may use letters, digits, '.', '_' and '-'", layer.source, name))
				continue
			}
			if err := server.validate(); err != nil && !server.Disabled {
				cfg.Problems = append(cfg.Problems, fmt.Sprintf("%s server %q: %v", layer.source, name, err))
				continue
			}
			server.Source = layer.source
			cfg.Servers[name] = server
		}
	}
	return cfg
}

// decodeServers reads either {"name": {...}} or a file {"mcpServers": {...}}.
func decodeServers(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, errors.New("must be a JSON object")
	}
	if nested, ok := object["mcpServers"]; ok {
		object = nil
		if err := json.Unmarshal(nested, &object); err != nil {
			return nil, errors.New("mcpServers must be a JSON object")
		}
	}
	return object, nil
}

var envRefRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// expandEnv replaces ${VAR} and ${VAR:-default} with values from lookup.
func expandEnv(s string, lookup func(string) (string, bool)) string {
	return envRefRE.ReplaceAllStringFunc(s, func(ref string) string {
		m := envRefRE.FindStringSubmatch(ref)
		if v, ok := lookup(m[1]); ok && v != "" {
			return v
		}
		return m[2]
	})
}

// expanded returns the config with ${VAR} references resolved.
func (c ServerConfig) expanded(lookup func(string) (string, bool)) ServerConfig {
	out := c
	out.Command = expandEnv(c.Command, lookup)
	out.Cwd = expandEnv(c.Cwd, lookup)
	out.URL = expandEnv(c.URL, lookup)
	out.Args = make([]string, len(c.Args))
	for i, arg := range c.Args {
		out.Args[i] = expandEnv(arg, lookup)
	}
	out.Env = make(map[string]string, len(c.Env))
	for k, v := range c.Env {
		out.Env[k] = expandEnv(v, lookup)
	}
	out.Headers = make(map[string]string, len(c.Headers))
	for k, v := range c.Headers {
		out.Headers[k] = expandEnv(v, lookup)
	}
	return out
}

// inheritedEnv lists the variables a stdio server inherits from wopr's
// environment. Everything else, notably provider API keys, stays out unless
// the server's env names it.
var inheritedEnv = []string{
	"HOME", "PATH", "USER", "LOGNAME", "SHELL", "TERM", "LANG", "TZ", "TMPDIR", "TMP", "TEMP",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS",
	// Windows essentials.
	"APPDATA", "LOCALAPPDATA", "USERPROFILE", "SYSTEMROOT", "SystemRoot", "SYSTEMDRIVE", "COMSPEC", "ComSpec",
	"PATHEXT", "WINDIR", "windir", "ProgramFiles", "ProgramFiles(x86)", "ProgramData",
}

// serverEnv builds a stdio server's environment: the inherited allowlist,
// LC_* and XDG_* variables, then the configured env.
func serverEnv(environ []string, configured map[string]string) []string {
	var out []string
	for _, kv := range environ {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, override := configured[key]; override {
			continue
		}
		if slices.Contains(inheritedEnv, key) || strings.HasPrefix(key, "LC_") || strings.HasPrefix(key, "XDG_") {
			out = append(out, kv)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(configured)) {
		out = append(out, key+"="+configured[key])
	}
	return out
}
