package codingagent

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/alexrudloff/wopr/internal/text"
)

// ProjectMCPFileName is the project-root MCP server file other agents share
// (Claude Code, Cursor). wopr reads it only for a trusted project.
const ProjectMCPFileName = ".mcp.json"

// RawSection returns one top-level settings key as authored in the global
// file and, when the project is trusted, the project file. Feature packages
// that own their section's shape (mcpServers, web) parse it themselves. The
// files are read on each call, so an edit applies on the next call.
func (sm *SettingsManager) RawSection(key string) (global, project json.RawMessage) {
	global = rawSettingsKey(filepath.Join(sm.agentDir, "settings.json"), key)
	if sm.IsProjectTrusted() {
		project = rawSettingsKey(filepath.Join(sm.cwd, CONFIG_DIR_NAME, "settings.json"), key)
	}
	return global, project
}

// ProjectMCPFile returns the project's .mcp.json content, or nil when the
// project is untrusted or has none.
func (sm *SettingsManager) ProjectMCPFile() json.RawMessage {
	if !sm.IsProjectTrusted() {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(sm.cwd, ProjectMCPFileName))
	if err != nil {
		return nil
	}
	return text.StripBomBytes(data)
}

func rawSettingsKey(path, key string) json.RawMessage {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(text.StripBomBytes(data), &fields) != nil {
		return nil
	}
	return fields[key]
}
