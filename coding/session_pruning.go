package coding

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/pruning"
)

// Context pruning wired into the Session (see internal/codingagent/pruning).
//
// Edits are planned before every provider request and applied in batches, so
// the provider's prompt cache breaks rarely: everything applies when the
// cache is already cold (idle past its TTL, after a compaction, on the first
// request of a process) or right before a threshold compaction; otherwise an
// edit applies only when its saving pays for rewriting the cache after it, or
// when it sits after a queued compress that breaks the cache anyway.

// compressForceMargin is how far past the compress threshold the tool is
// offered even though the cache is warm.
const compressForceMargin = 0.2

// compressMinAssistants is how much work must be in context before a span
// can be finished: a single large prompt past the threshold has nothing to
// compress.
const compressMinAssistants = 3

type sessionPruning struct {
	cfg      pruning.Config
	problems []string
	ordinals pruning.Ordinals
	tool     *pruning.CompressTool

	mu          sync.Mutex
	pending     []pruning.Edit
	active      bool // compress offered and markers rendered
	cold        bool
	lastRequest time.Time
	// pack archives compressed spans when ObservationPack is off.
	pack *efficiency.Pack
}

// initPruning reads the contextPruning settings. It runs after initEfficiency so
// compressed spans reuse the observation archive.
func (s *Session) initPruning() {
	global, project := s.services.SettingsManager().RawSection(pruning.SettingsKey)
	cfg, problems := pruning.ParseConfig(global, project)
	st := &sessionPruning{cfg: cfg, problems: problems, cold: true}
	if cfg.CompressOn() {
		st.tool = &pruning.CompressTool{Host: pruneHost{s: s}}
	}
	s.pruning = st
}

// pruneCold records that the provider cache no longer holds the prefix, so
// the next batch is free.
func (s *Session) pruneCold() {
	if st := s.pruning; st != nil {
		st.mu.Lock()
		st.cold = true
		st.mu.Unlock()
	}
}

func (st *sessionPruning) isCold(idleWarm bool, ttl time.Duration) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cold || (!idleWarm && time.Since(st.lastRequest) >= ttl)
}

// pruneCache is the prompt cache the conversation uses: its lifetime and a
// write's cost in reads. With the long cache, a pause over 5 minutes leaves
// the prefix cached, and taking the cache for cold there applied every queued
// edit and rewrote a warm 600K-token Opus prefix.
func (s *Session) pruneCache() (time.Duration, float64) {
	if s.promptCacheRetention(ai.StreamOptions{}) == ai.CacheRetentionLong {
		return pruning.LongCacheTTL, pruning.LongCacheWriteReadRatio
	}
	ratio := efficiency.DefaultCacheWriteReadRatio
	if s.efficiency != nil && s.efficiency.cfg.CacheWriteReadRatio > 1 {
		ratio = s.efficiency.cfg.CacheWriteReadRatio
	}
	return pruning.CacheTTL, ratio
}

// pruneProjection plans the automatic edits, applies the batch the policy
// selects, and returns the projection with its items (nil when pruning is
// off). force applies every planned edit.
func (s *Session) pruneProjection(force bool) (icodingagent.SessionProjection, []pruning.Item) {
	projection := s.inner.BuildSessionProjection()
	st := s.pruning
	if st == nil || !st.cfg.Enabled {
		return projection, nil
	}
	items := s.pruneItems(projection)
	st.mu.Lock()
	pending := st.pending
	st.pending = nil
	st.mu.Unlock()
	idleWarm := s.services.SettingsManager().GetCacheWarmingMode() == "idle"
	ttl, ratio := s.pruneCache()
	policy := pruning.Policy{Cold: st.isCold(idleWarm, ttl), Force: force, WriteReadRatio: ratio}
	requests := 0
	for _, item := range items {
		if item.Message.Assistant != nil {
			requests++
		}
	}
	policy.Horizon = pruning.Horizon(requests)
	var auto []pruning.Edit
	if st.cfg.Automatic() {
		taken := map[string]bool{}
		for _, e := range pending {
			taken[e.TargetID] = true
		}
		for _, e := range pruning.Plan(st.cfg, items, s.services.CWD()) {
			if !taken[e.TargetID] {
				auto = append(auto, e)
			}
		}
	}
	chosen := pruning.Choose(auto, pending, items, policy)
	if len(chosen) == 0 {
		return projection, items
	}
	saved := map[string]int{}
	for _, e := range chosen {
		var replacement *icodingagent.ContextEditReplacement
		if e.Replacement != nil {
			replacement = &icodingagent.ContextEditReplacement{Content: e.Replacement}
		}
		if _, err := s.inner.AppendContextEdit(e.TargetID, replacement); err != nil {
			continue
		}
		saved[e.Strategy] += e.Saved
	}
	for _, strategy := range []string{pruning.StrategyCompress, pruning.StrategyDedupe, pruning.StrategySupersede, pruning.StrategyPurgeErrors} {
		if tokens := saved[strategy]; tokens > 0 {
			s.emitSavings("Context pruning", fmt.Sprintf("%s removed %d context tokens", strategyLabel(strategy), tokens), tokens)
		}
	}
	projection = s.inner.BuildSessionProjection()
	return projection, s.pruneItems(projection)
}

func strategyLabel(strategy string) string {
	switch strategy {
	case pruning.StrategyCompress:
		return "compress"
	case pruning.StrategyDedupe:
		return "duplicate tool calls"
	case pruning.StrategySupersede:
		return "stale reads"
	case pruning.StrategyPurgeErrors:
		return "failed-call inputs"
	}
	return strategy
}

// pruneItems builds the items, pricing a large tool result at the size of
// its ObservationPack placeholder when the pack will replace it anyway.
func (s *Session) pruneItems(projection icodingagent.SessionProjection) []pruning.Item {
	items := pruning.BuildItems(projection, s.currentBranch(), &s.pruning.ordinals)
	if s.efficiency == nil || s.efficiency.pack == nil {
		return items
	}
	for i, item := range items {
		if result := item.Message.ToolResult; result != nil && !result.IsError {
			if o := s.efficiency.pack.NewObservation(result); o != nil {
				items[i].Tokens = min(item.Tokens, ai.EstimateTextTokens(efficiency.Placeholder(o)))
			}
		}
	}
	return items
}

// pruneRequestContext is the request context: the pruned projection with the
// instruction baseline, ObservationPack applied, and compress markers when
// the tool is offered. It records the request time for the cache clock.
func (s *Session) pruneRequestContext() []agent.AgentMessage {
	projection, items := s.pruneProjection(false)
	context := withInstructionBaseline(s.agent.Messages(), projection.Messages)
	projected := s.efficiencyProject(context)
	if st := s.pruning; st != nil {
		st.mu.Lock()
		st.lastRequest = time.Now()
		st.cold = false
		active := st.active
		st.mu.Unlock()
		if active && len(items) == len(projection.Messages) {
			offset := len(projected) - len(items)
			ordinals := make([]int, len(projected))
			for i, item := range items {
				ordinals[offset+i] = item.Ordinal
			}
			projected = pruning.Mark(projected, ordinals)
		}
	}
	return projected
}

// pruneBeforeCompaction applies every planned edit when the context has
// crossed the compaction threshold, and reports whether any applied, so the
// caller can re-measure before summarizing.
func (s *Session) pruneBeforeCompaction() bool {
	if s.pruning == nil || !s.pruning.cfg.Automatic() {
		return false
	}
	before := len(s.currentBranch())
	s.pruneProjection(true)
	return len(s.currentBranch()) != before
}

// pruneMaybeOffer offers the compress tool once the context passes the
// threshold. Adding a tool and rendering markers rewrite the whole prompt,
// so the offer waits for a cold cache unless the context is well past the
// threshold. It runs before the agent declares tool changes.
func (s *Session) pruneMaybeOffer(tokens, window int) {
	st := s.pruning
	if st == nil || st.tool == nil || window <= 0 {
		return
	}
	st.mu.Lock()
	active := st.active
	st.mu.Unlock()
	if active {
		return
	}
	usage := float64(tokens) / float64(window)
	if usage < st.cfg.CompressThreshold {
		return
	}
	assistants := 0
	for _, message := range s.agent.Messages() {
		if message.Assistant != nil {
			assistants++
		}
	}
	if assistants < compressMinAssistants {
		return
	}
	idleWarm := s.services.SettingsManager().GetCacheWarmingMode() == "idle"
	ttl, _ := s.pruneCache()
	if !st.isCold(idleWarm, ttl) && usage < st.cfg.CompressThreshold+compressForceMargin {
		return
	}
	st.mu.Lock()
	st.active = true
	st.mu.Unlock()
	added := []agent.AgentTool{st.tool}
	if s.toolNamed("obs_recall") == nil {
		if pack := s.pruneArchive(); pack != nil {
			added = append(added, efficiency.NewRecallTool(pack))
		}
	}
	s.tools = append(s.tools, added...)
	s.agent.SetTools(append(s.agent.Tools(), added...))
}

// pruneOfferAtPrompt checks the offer before a user prompt runs.
func (s *Session) pruneOfferAtPrompt() {
	if s.pruning == nil || s.pruning.tool == nil {
		return
	}
	model := s.activeModel()
	if model == nil || model.Capabilities.ContextWindow <= 0 {
		return
	}
	tokens := compaction.EstimateProjectedContextTokens(s.inner.BuildSessionProjection(), s.currentBranch()).Tokens
	s.pruneMaybeOffer(tokens, model.Capabilities.ContextWindow)
}

// pruneArchive returns the pack that archives compressed spans:
// ObservationPack's when it is on, else one of its own, or nil when the
// session is not persisted.
func (s *Session) pruneArchive() *efficiency.Pack {
	if s.efficiency != nil && s.efficiency.pack != nil {
		return s.efficiency.pack
	}
	st := s.pruning
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.pack == nil {
		if root := efficiency.RuntimeRoot(s.inner.Path(), s.inner.ID()); root != "" {
			st.pack = efficiency.NewPack(root)
		}
	}
	return st.pack
}

// PruningStatus reports the pruning configuration for /harness.
func (s *Session) PruningStatus() string {
	st := s.pruning
	if st == nil {
		return "Context pruning: not initialised"
	}
	onOff := func(b bool) string {
		if b && st.cfg.Enabled {
			return "on"
		}
		return "off"
	}
	var b strings.Builder
	for _, problem := range st.problems {
		fmt.Fprintf(&b, "**Config error:** %s\n\n", problem)
	}
	st.mu.Lock()
	offered := st.active
	st.mu.Unlock()
	fmt.Fprintf(&b, "- Dedupe: %s\n- Purge failed-call inputs: %s\n- Supersede stale reads: %s\n- Compress tool: %s (offered at %.0f%% of the window; offered now: %t)\n",
		onOff(st.cfg.Dedupe), onOff(st.cfg.PurgeErrors), onOff(st.cfg.SupersedeReads), onOff(st.cfg.Compress), st.cfg.CompressThreshold*100, offered)
	return b.String()
}

// pruneHost adapts the Session to pruning.CompressHost.
type pruneHost struct{ s *Session }

func (h pruneHost) CompressItems() []pruning.Item {
	return h.s.pruneItems(h.s.inner.BuildSessionProjection())
}

func (h pruneHost) ArchiveSpan(key, text string) string {
	pack := h.s.pruneArchive()
	if pack == nil {
		return ""
	}
	id, err := pack.Archive("compress", key, text)
	if err != nil {
		return ""
	}
	return id
}

func (h pruneHost) QueueCompress(edits []pruning.Edit) error {
	st := h.s.pruning
	st.mu.Lock()
	defer st.mu.Unlock()
	queued := map[string]bool{}
	for _, e := range st.pending {
		queued[e.TargetID] = true
	}
	for _, e := range edits {
		if queued[e.TargetID] {
			return fmt.Errorf("that span overlaps a compress already queued for the next request")
		}
	}
	st.pending = append(st.pending, edits...)
	return nil
}
