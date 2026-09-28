// Package router chooses a model per prompt. It classifies each prompt with
// TypeSafe Jev (a decisions endpoint), then picks the cheapest configured
// tier that can handle the task and fit the context. Failures on one model
// fall through to the next candidate without user intervention. Routing
// needs Jev: without an endpoint it is off, and when Jev stops answering it
// pauses and the user's selected model carries on.
//
// The design goal is the user's priority order: free and local first, the
// subscription (quota-limited, zero marginal cost) for orchestration and
// review, and pay-per-use only when nothing else can do the job.
package router

import (
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/ai"
)

// ConfigFileName is the router configuration file under the agent directory.
const ConfigFileName = "router.json"

// Cost classes order tiers from cheapest to most expensive.
const (
	CostFreeLocal    = "free-local"   // runs on this machine
	CostFreeRemote   = "free-remote"  // runs on hardware the user owns
	CostSubscription = "subscription" // flat-rate plan with usage quotas
	CostPaid         = "paid"         // pay per token
)

// ModelRef names one candidate model inside a tier.
type ModelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	// Thinking, when set, caps the thinking level for this model
	// (e.g. "low" for a slow local model).
	Thinking string `json:"thinking,omitempty"`
	// Effort is this model's default thinking level. Auto routing starts
	// from it instead of the prompt's demand (kind boosts, the mode's shift,
	// and the caps still apply); a session that starts on this model without
	// a thinking level chosen in settings or on the command line uses it.
	Effort string `json:"effort,omitempty"`
	// Capability is this model's strength on 0..1. Zero inherits the tier's.
	Capability float64 `json:"capability,omitempty"`
	// Affinity is a per-domain bonus (for example "frontend_ui": 0.1) that
	// breaks ties among adequate models of the same cost class when routing
	// subagent briefs. It never makes a model adequate or moves work to a
	// more expensive class. The defaults are editable priors.
	Affinity map[string]float64 `json:"affinity,omitempty"`
	// Uncensored marks a model without refusal training (an abliterated
	// model). Uncensored mode routes only to flagged models.
	Uncensored bool `json:"uncensored,omitempty"`
	// Hashline turns hashline anchors on read and edit on or off for this
	// model. Unset follows the "hashline" setting, whose default "auto"
	// turns them on for free-local and free-remote tiers only.
	Hashline *bool `json:"hashline,omitempty"`
}

// Spec returns the "provider/model" form.
func (m ModelRef) Spec() string { return m.Provider + "/" + m.Model }

// TierConfig is one cost tier: an ordered chain of candidate models.
type TierConfig struct {
	Name string `json:"name"`
	// Cost is one of the Cost* classes.
	Cost string `json:"cost"`
	// Capability is the default strength (0..1) of the tier's models. A
	// model is adequate for a prompt when capability+Slack covers the
	// prompt's demand.
	Capability float64 `json:"capability"`
	// Models is the candidate chain, tried in order.
	Models []ModelRef `json:"models"`
	// ProbeURL, when set, is fetched with a short timeout before the tier is
	// used; a failed probe marks the tier down for ProbeIntervalSeconds.
	ProbeURL string `json:"probeUrl,omitempty"`
	// ContextShare, when below 1, caps the fraction of a model's window the
	// router fills before skipping it. By default a model takes what
	// ai.UsableContext allows, the point where compaction would start.
	ContextShare float64 `json:"contextShare,omitempty"`
	// MaxThinking caps thinking for every model in the tier.
	MaxThinking string `json:"maxThinking,omitempty"`
}

// KindPolicy tunes routing for one task kind.
type KindPolicy struct {
	// MinCapability raises the demand floor for the kind (for example,
	// plan and review require frontier-class models).
	MinCapability float64 `json:"minCapability,omitempty"`
	// ThinkingBoost raises the thinking level by this many steps.
	ThinkingBoost int `json:"thinkingBoost,omitempty"`
}

// JevConfig configures the classifier call.
type JevConfig struct {
	// Endpoint is the decisions endpoint or its base URL (TypeSafe's
	// https://api.typesafe.ai, OpenRouter's https://openrouter.ai/api, or a
	// proxy); see NormalizeJevEndpoint. Without one, routing is off.
	Endpoint string `json:"endpoint,omitempty"`
	// Model is the Jev model id, as TypeSafe names it (jev-1.13); OpenRouter
	// ids are derived. Pin a version: thresholds are tuned against one model.
	Model string `json:"model,omitempty"`
	// APIKeyProvider picks the key: unset uses the key saved for Jev, then
	// the environment (see ResolveJevKey); "none" sends no Authorization
	// header (a proxy that holds the upstream key itself); another id uses
	// that provider's key.
	APIKeyProvider string `json:"apiKeyProvider,omitempty"`
	// TimeoutMs is the per-attempt timeout (default 1300).
	TimeoutMs int `json:"timeoutMs,omitempty"`
	// Retries is how many times a failed call is retried (default 1).
	Retries *int `json:"retries,omitempty"`
	// MaxPromptChars bounds the prompt text sent for classification.
	MaxPromptChars int `json:"maxPromptChars,omitempty"`
	// Disabled turns routing off and keeps the endpoint for later.
	Disabled bool `json:"disabled,omitempty"`
}

// SubagentConfig tunes subagents.
type SubagentConfig struct {
	// MaxParallel caps concurrent subagents (default 4).
	MaxParallel int `json:"maxParallel,omitempty"`
	// ProviderParallel caps concurrent subagents per provider; providers not
	// listed are bounded by MaxParallel.
	ProviderParallel map[string]int `json:"providerParallel,omitempty"`
	// MaxColdStartSeconds keeps a brief off a model whose measured prefill
	// of the brief's expected context would take longer (default 30), so a
	// large brief never lands on a slow-prefill local server.
	MaxColdStartSeconds float64 `json:"maxColdStartSeconds,omitempty"`
}

// Config is the router configuration.
type Config struct {
	// Enabled turns routing on; a session starts in auto mode unless it (or
	// the last session) chose another. The mode is a per-session choice, not
	// a setting; WOPR_ROUTER overrides it.
	Enabled bool `json:"enabled"`
	// Engine is what routes: "basic" (fixed rules on model attributes) or
	// "jev". Unset, it is Jev when Jev is configured, else Basic.
	Engine string    `json:"engine,omitempty"`
	Jev    JevConfig `json:"jev"`
	// Ranking orders the models strongest first, as /setup keeps it; the
	// tiers' capabilities are derived from it (see Strengths).
	Ranking Ranking `json:"ranking,omitempty"`
	// Tiers are ordered cheapest first.
	Tiers []TierConfig `json:"tiers"`
	// Kinds tunes routing per task kind.
	Kinds map[string]KindPolicy `json:"kinds,omitempty"`
	// Slack is how far demand may exceed a tier's Capability before the tier
	// is skipped (default 0.2).
	Slack float64 `json:"slack,omitempty"`
	// PaidLastResort allows a paid tier when no cheaper tier fits.
	PaidLastResort bool `json:"paidLastResort"`
	// EscalateThinking raises the orchestrator's thinking one level after a
	// turn whose tool calls all failed, and lowers it one level after a turn
	// whose tool calls all succeeded, never below the starting level
	// (default off; unmeasured).
	EscalateThinking *bool `json:"escalateThinking,omitempty"`
	// PaidKinds lists kinds allowed to use a paid tier when it is the
	// tier the policy selects (for example "review"). Empty means paid is
	// last-resort only.
	PaidKinds []string `json:"paidKinds,omitempty"`
	// ProbeIntervalSeconds caches probe results (default 30).
	ProbeIntervalSeconds int `json:"probeIntervalSeconds,omitempty"`
	// QuotaBackoffMinutes is how long a subscription tier rests after a
	// rate-limit or quota error (default 15).
	QuotaBackoffMinutes int `json:"quotaBackoffMinutes,omitempty"`
	// SensitiveStaysPrivate keeps prompts Jev flags as sensitive on
	// free-local and free-remote tiers when one fits.
	SensitiveStaysPrivate bool `json:"sensitiveStaysPrivate"`
	// StickyIdleMinutes is how long the orchestrator may sit idle before its
	// prompt cache counts as cold and the next turn re-decides the model
	// freely (default 10, matching provider cache lifetimes).
	StickyIdleMinutes int `json:"stickyIdleMinutes,omitempty"`
	// AffinityWeight scales every model's domain affinity; 0 turns affinity
	// off (default 1).
	AffinityWeight *float64 `json:"affinityWeight,omitempty"`
	// Subagents tunes subagent brief routing and concurrency.
	Subagents SubagentConfig `json:"subagents"`

	// statsDir is where learned speeds persist (the agent directory).
	statsDir string
	// objective is the starting routing mode, from WOPR_ROUTER.
	objective Objective
}

// DefaultConfig is the routing policy without any models: routing is built
// only from the models the user configures (router.json, written by
// /setup). There are no default tiers, capabilities, or classifier
// endpoint; without a Jev endpoint routing is off.
func DefaultConfig() Config {
	return Config{
		Enabled: true,
		Jev: JevConfig{
			TimeoutMs:      1300,
			MaxPromptChars: 6000,
		},
		Kinds: map[string]KindPolicy{
			"plan":   {MinCapability: 0.85, ThinkingBoost: 1},
			"review": {MinCapability: 0.85, ThinkingBoost: 1},
			"debug":  {ThinkingBoost: 1},
		},
		Slack:                 0.05,
		PaidLastResort:        true,
		ProbeIntervalSeconds:  30,
		QuotaBackoffMinutes:   15,
		SensitiveStaysPrivate: true,
		Subagents: SubagentConfig{
			MaxParallel:         4,
			MaxColdStartSeconds: 30,
		},
	}
}

// Routing engines.
const (
	EngineBasic = "basic"
	EngineJev   = "jev"
)

// EngineName is the configured engine, defaulting to Jev when Jev is
// configured, as configurations written before engines were.
func (c Config) EngineName() string {
	switch c.Engine {
	case EngineBasic, EngineJev:
		return c.Engine
	}
	if c.Jev.Active() {
		return EngineJev
	}
	return EngineBasic
}

// Active reports whether the classifier is configured and on: an endpoint
// and a model, and not disabled.
func (j JevConfig) Active() bool {
	return !j.Disabled && j.Endpoint != "" && j.Model != ""
}

func (c *Config) affinityWeight() float64 {
	if c.AffinityWeight == nil {
		return 1
	}
	return *c.AffinityWeight
}

// Load reads <agentDir>/router.json over DefaultConfig. Routing is opt-in: a
// missing file yields the defaults with Enabled false, so no endpoint is
// probed until the user writes a configuration. WOPR_ROUTER=off disables
// routing regardless of the file; WOPR_ROUTER=auto, cost, speed, quality,
// or uncensored turns it on in that mode.
func Load(agentDir string) (Config, error) {
	cfg := DefaultConfig()
	path := filepath.Join(agentDir, ConfigFileName)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		// Decode over the defaults: omitted keys keep their default values,
		// and a "tiers" or "kinds" key replaces the default list wholesale.
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(data, &probe); err != nil {
			return cfg, fmt.Errorf("router: parse %s: %w", path, err)
		}
		if _, ok := probe["kinds"]; ok {
			cfg.Kinds = nil
		}
		// Decoding into the default tiers would fill each file tier's
		// omitted fields from the default tier at the same position (an
		// "uncensored" or "affinity" the user never wrote).
		if _, ok := probe["tiers"]; ok {
			cfg.Tiers = nil
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("router: parse %s: %w", path, err)
		}
		// /setup writes the model order before routing is ever turned on;
		// without an "enabled" key only a configured Jev turned it on.
		if _, ok := probe["enabled"]; !ok {
			cfg.Enabled = cfg.Jev.Active()
		}
	case errors.Is(err, os.ErrNotExist):
		cfg.Enabled = false
	default:
		return cfg, fmt.Errorf("router: read %s: %w", path, err)
	}
	cfg.statsDir = agentDir
	v := strings.ToLower(strings.TrimSpace(os.Getenv("WOPR_ROUTER")))
	if v == "off" || v == "0" || v == "false" {
		cfg.Enabled = false
	}
	if err := cfg.validate(); err != nil {
		return cfg, err
	}
	if o, ok := ParseObjective(v); ok {
		// A routing mode turns routing on with the router picking the
		// orchestrator, as choosing the mode in the model picker does.
		if o == ObjectiveUncensored && !cfg.hasUncensored() {
			return cfg, errors.New("router: WOPR_ROUTER=uncensored, but no configured model is flagged uncensored")
		}
		cfg.Enabled, cfg.objective = true, o
	}
	return cfg, nil
}

// hasUncensored reports whether any configured model is flagged Uncensored.
func (c *Config) hasUncensored() bool {
	for _, tier := range c.Tiers {
		for _, ref := range tier.Models {
			if ref.Uncensored {
				return true
			}
		}
	}
	return false
}

func (c *Config) validate() error {
	seen := map[string]bool{}
	for i := range c.Tiers {
		tier := &c.Tiers[i]
		if tier.Name == "" {
			return fmt.Errorf("router: tier %d has no name", i)
		}
		if seen[tier.Name] {
			return fmt.Errorf("router: duplicate tier %q", tier.Name)
		}
		seen[tier.Name] = true
		switch tier.Cost {
		case CostFreeLocal, CostFreeRemote, CostSubscription, CostPaid:
		case "":
			tier.Cost = CostPaid
		default:
			return fmt.Errorf("router: tier %q: unknown cost %q", tier.Name, tier.Cost)
		}
		if tier.ContextShare <= 0 || tier.ContextShare > 1 {
			tier.ContextShare = 1
		}
		for j, m := range tier.Models {
			if m.Provider == "" || m.Model == "" {
				return fmt.Errorf("router: tier %q model %d needs provider and model", tier.Name, j)
			}
			if m.Effort != "" && !slices.Contains(thinkingOrder, ai.ThinkingLevel(m.Effort)) {
				return fmt.Errorf("router: tier %q model %s: unknown effort %q", tier.Name, m.Spec(), m.Effort)
			}
		}
	}
	return nil
}

// tierIndex returns the index of the named tier, or -1.
func (c *Config) tierIndex(name string) int {
	return slices.IndexFunc(c.Tiers, func(tier TierConfig) bool { return tier.Name == name })
}

// refs yields each tier index and model entry listing provider/model, in
// config order. A model may appear in more than one tier.
func (c *Config) refs(spec string) iter.Seq2[int, ModelRef] {
	return func(yield func(int, ModelRef) bool) {
		for i, tier := range c.Tiers {
			for _, ref := range tier.Models {
				if ref.Spec() == spec && !yield(i, ref) {
					return
				}
			}
		}
	}
}
