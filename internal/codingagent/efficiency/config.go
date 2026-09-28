// Package efficiency implements four mechanisms that cut the tokens each
// request carries: Action Fusion, ObservationPack, the Evidence-Preserving
// Reducer, and Online Context Compact. The reducer runs on the router's
// cheapest tier, and journals are files rather than session entries.
package efficiency

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ConfigFileName is the mechanism configuration under the agent directory.
const ConfigFileName = "efficiency.json"

// DefaultCacheWriteReadRatio is the default prompt-cache write cost: 12.5
// cache reads on the frontier providers it was tuned against.
const DefaultCacheWriteReadRatio = 12.5

// Config selects mechanisms. A missing file or key keeps the default: every
// mechanism on.
type Config struct {
	Version                   int     `json:"version"`
	ActionFusion              bool    `json:"actionFusion"`
	ObservationPack           bool    `json:"observationPack"`
	EvidencePreservingReducer bool    `json:"evidencePreservingReducer"`
	OnlineContextCompact      bool    `json:"onlineContextCompact"`
	CacheWriteReadRatio       float64 `json:"cacheWriteReadRatio"`
	// ReducerProvider and ReducerModel pin the reducer route. Empty means
	// "ask the model router for its cheapest tier that fits".
	ReducerProvider string `json:"evidencePreservingReducerProvider,omitempty"`
	ReducerModel    string `json:"evidencePreservingReducerModel,omitempty"`
	// ToolOutputHalfLife cuts tool results older than the newest few to a
	// short excerpt for models with a small context window; ObservationPack
	// keeps the originals for obs_recall. Needs ObservationPack.
	ToolOutputHalfLife bool `json:"toolOutputHalfLife"`
	// StallNudge tells the model to change approach when it repeats the
	// same tool call or goes many turns without changing a file.
	StallNudge bool `json:"stallNudge"`
	// TestRerunCap answers a test command that already passed, with no
	// file changed since, without running it again.
	TestRerunCap bool `json:"testRerunCap"`
	// LazyTools holds rarely used tools (task, web_fetch, web_search)
	// back until the model loads them with load_tools.
	LazyTools bool `json:"lazyTools"`
	// ApplyPatch declares the apply_patch tool in place of edit to OpenAI
	// GPT and Codex models, whose training uses that patch format.
	ApplyPatch bool `json:"applyPatch"`
	// Learn tunes the numbers above per model from real sessions (see
	// Learner).
	Learn bool `json:"learn"`
	// QuotaBalance moves routed work to a similarly ranked model on another
	// subscription plan when the top pick's plan gets tight.
	QuotaBalance bool `json:"quotaBalance"`
}

// DefaultConfig turns every mechanism on.
func DefaultConfig() Config {
	return Config{
		Version:                   1,
		ActionFusion:              true,
		ObservationPack:           true,
		EvidencePreservingReducer: true,
		OnlineContextCompact:      true,
		CacheWriteReadRatio:       DefaultCacheWriteReadRatio,
		ToolOutputHalfLife:        true,
		StallNudge:                true,
		TestRerunCap:              true,
		LazyTools:                 true,
		ApplyPatch:                true,
		QuotaBalance:              true,
		Learn:                     true,
	}
}

// Enabled reports whether any mechanism is on.
func (c Config) Enabled() bool {
	return c.ActionFusion || c.ObservationPack || c.EvidencePreservingReducer || c.OnlineContextCompact || c.StallNudge || c.TestRerunCap || c.LazyTools || c.ApplyPatch
}

// Load reads <agentDir>/efficiency.json. Unknown keys and bad values are
// errors, so a typo never silently disables a mechanism.
func Load(agentDir string) (Config, error) {
	cfg := DefaultConfig()
	path := filepath.Join(agentDir, ConfigFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("efficiency: read %s: %w", path, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return cfg, fmt.Errorf("efficiency: parse %s: %w", path, err)
	}
	known := map[string]bool{
		"version": true, "actionFusion": true, "observationPack": true, "evidencePreservingReducer": true,
		"onlineContextCompact": true, "cacheWriteReadRatio": true,
		"evidencePreservingReducerProvider": true, "evidencePreservingReducerModel": true,
		"toolOutputHalfLife": true, "stallNudge": true, "testRerunCap": true, "lazyTools": true, "applyPatch": true, "quotaBalance": true, "learn": true,
	}
	for key := range raw {
		if !known[key] {
			return cfg, fmt.Errorf("efficiency: unknown config key %q in %s", key, path)
		}
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("efficiency: parse %s: %w", path, err)
	}
	if cfg.Version != 1 {
		return cfg, fmt.Errorf("efficiency: config version must be 1 in %s", path)
	}
	if cfg.CacheWriteReadRatio < 0 {
		return cfg, fmt.Errorf("efficiency: cacheWriteReadRatio must be non-negative in %s", path)
	}
	if (cfg.ReducerProvider == "") != (cfg.ReducerModel == "") {
		return cfg, fmt.Errorf("efficiency: set both evidencePreservingReducerProvider and evidencePreservingReducerModel, or neither, in %s", path)
	}
	return cfg, nil
}

// RuntimeRoot is the per-session archive directory: next to the session
// file, under harness/<session id>. It is empty when the session is not
// persisted, which disables the mechanisms that archive.
func RuntimeRoot(sessionPath, sessionID string) string {
	if sessionPath == "" || sessionID == "" || !safeID(sessionID) {
		return ""
	}
	return filepath.Join(filepath.Dir(sessionPath), "harness", sessionID)
}

func safeID(id string) bool {
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '_' || r == '-'):
		default:
			return false
		}
	}
	return id != ""
}
