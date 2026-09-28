// Package pruning removes stale material from the model context without a
// model call (duplicate tool calls, inputs of failed calls, reads a later
// change made stale) and gives the model a compress tool for spans it has
// finished with. Every change is an append-only context_edit entry, so the
// session file keeps the original, and a compressed span is archived for
// obs_recall.
//
// Pruning rewrites history, and a rewrite invalidates the provider's prompt
// cache from the first changed message onward. The package therefore plans
// edits on every request but applies them in batches: when the cache is
// already cold, when the saving pays back the cache rewrite (see Choose), just
// before a compaction, or together with a compress call that breaks the cache
// anyway.
package pruning

import (
	"encoding/json"
	"fmt"
)

// SettingsKey is the settings.json key this package owns.
const SettingsKey = "contextPruning"

// DefaultCompressThreshold is the context-window fraction at which the
// compress tool is offered.
const DefaultCompressThreshold = 0.4

// Config selects the strategies. Every strategy defaults on.
type Config struct {
	Enabled        bool
	Dedupe         bool
	PurgeErrors    bool
	SupersedeReads bool
	Compress       bool
	// CompressThreshold is the fraction of the context window in use before
	// the compress tool is offered.
	CompressThreshold float64
}

// DefaultConfig turns every strategy on.
func DefaultConfig() Config {
	return Config{Enabled: true, Dedupe: true, PurgeErrors: true, SupersedeReads: true, Compress: true, CompressThreshold: DefaultCompressThreshold}
}

// Automatic reports whether any no-model strategy runs.
func (c Config) Automatic() bool {
	return c.Enabled && (c.Dedupe || c.PurgeErrors || c.SupersedeReads)
}

// CompressOn reports whether the compress tool may be offered.
func (c Config) CompressOn() bool { return c.Enabled && c.Compress }

type configWire struct {
	Enabled           *bool    `json:"enabled"`
	Dedupe            *bool    `json:"dedupe"`
	PurgeErrors       *bool    `json:"purgeErrors"`
	SupersedeReads    *bool    `json:"supersedeReads"`
	Compress          *bool    `json:"compress"`
	CompressThreshold *float64 `json:"compressThreshold"`
}

// ParseConfig layers the global then the project contextPruning section over
// the defaults. A malformed layer is reported and skipped.
func ParseConfig(global, project json.RawMessage) (Config, []string) {
	cfg := DefaultConfig()
	var problems []string
	for _, layer := range []struct {
		source string
		raw    json.RawMessage
	}{{"global", global}, {"project", project}} {
		if len(layer.raw) == 0 {
			continue
		}
		var wire configWire
		if err := json.Unmarshal(layer.raw, &wire); err != nil {
			problems = append(problems, fmt.Sprintf("%s %s: %v", layer.source, SettingsKey, err))
			continue
		}
		set := func(dst, src *bool) {
			if src != nil {
				*dst = *src
			}
		}
		set(&cfg.Enabled, wire.Enabled)
		set(&cfg.Dedupe, wire.Dedupe)
		set(&cfg.PurgeErrors, wire.PurgeErrors)
		set(&cfg.SupersedeReads, wire.SupersedeReads)
		set(&cfg.Compress, wire.Compress)
		if t := wire.CompressThreshold; t != nil {
			if *t <= 0 || *t >= 1 {
				problems = append(problems, fmt.Sprintf("%s %s.compressThreshold must be between 0 and 1", layer.source, SettingsKey))
			} else {
				cfg.CompressThreshold = *t
			}
		}
	}
	return cfg, problems
}
