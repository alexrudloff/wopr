package coding

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
)

// Model routing. Harness-specific: each prompt is routed to a model by Jev or
// the Basic rules, and a failing model falls through to the next candidate.
// The Session's own Model() stays what the user selected; while a mode
// routes, a run with no eligible model fails with the reason rather than
// falling back to it.

// routeState is the model the current run's requests use, or err when no
// model is eligible.
type routeState struct {
	decision *router.Decision
	model    *ai.Model
	thinking ai.ThinkingLevel
	err      error
}

// routeThinking is a decision's thinking level on m: the router's, or the
// session's when the decision leaves it to the user (Basic rules).
func (s *Session) routeThinking(m *ai.Model, d *router.Decision) ai.ThinkingLevel {
	if d.Thinking == "" {
		return ai.ClampThinkingLevel(m, s.agent.ThinkingLevel())
	}
	return ai.ClampThinkingLevel(m, d.Thinking)
}

// noRouteError is the error for a request no eligible model can take.
func (s *Session) noRouteError(contextTokens int, o router.Objective) error {
	return errors.New(s.router.NoRouteReason(contextTokens, o) + ". Check /router status, or pick a model with /model or Tab")
}

// RouteError is the current run's no-route error while routing picks the
// orchestrator, else nil.
func (s *Session) RouteError() error {
	if !s.routingActive() {
		return nil
	}
	if rs := s.route.Load(); rs != nil {
		return rs.err
	}
	return nil
}

// routerHost adapts the Session's services to router.Host.
type routerHost struct{ s *Session }

func (h routerHost) ModelInfo(provider, model string) (router.ModelInfo, bool) {
	m, err := h.s.routeModel(provider, model)
	if err != nil {
		return router.ModelInfo{}, false
	}
	return router.ModelInfo{
		DisplayName:     m.DisplayName,
		ContextWindow:   m.Capabilities.ContextWindow,
		MaxOutputTokens: m.Capabilities.MaxOutputTokens,
		MaxThinking:     m.Capabilities.MaxThinking,
	}, true
}

func (h routerHost) APIKey(provider string) string {
	registry := h.s.services.Registry()
	if key, ok := registry.RuntimeAPIKey(provider); ok && key != "" {
		return key
	}
	if entry, ok := registry.Resolve(provider, ""); ok && entry.APIKey != "" {
		return entry.APIKey
	}
	if auth := h.s.services.Auth(); auth != nil {
		if cred, ok, err := auth.Get(provider); err == nil && ok && cred.Key != "" {
			return cred.Key
		}
	}
	return ""
}

// initRouter loads router.json and installs the router. A configuration
// error leaves routing off and is reported by /router.
func (s *Session) initRouter() {
	s.routerLoadError = ""
	cfg, err := router.Load(s.services.AgentDir())
	if err != nil {
		s.routerLoadError = err.Error()
		cfg.Enabled = false
	}
	s.router = router.New(cfg, routerHost{s: s})
	// Quota balancing is an efficiency setting; a bad efficiency.json keeps
	// the default, as initEfficiency does.
	eff, err := efficiency.Load(s.services.AgentDir())
	if err != nil {
		eff = efficiency.DefaultConfig()
	}
	s.router.SetQuotaBalance(eff.QuotaBalance)
	s.routeModels = map[string]*ai.Model{}
}

// recordToolOutcome counts an orchestrator tool call, and whether it
// failed, for the model that made it.
func (s *Session) recordToolOutcome(end agent.ToolExecutionEndEvent) {
	m := s.activeModel()
	if m == nil {
		return
	}
	o := router.OutcomeCounts{ToolCalls: 1}
	if end.Result.IsError {
		o.ToolErrors = 1
	}
	router.RecordOutcome(s.services.AgentDir(), providerID(m)+"/"+m.ID, o)
}

// routingEntryType is the custom session entry recording the routing
// choice, so a resumed session routes as it did.
const routingEntryType = "routing"

// RoutingChoice is the orchestrator's choice as settings and sessions
// record it: the mode while the router picks the orchestrator, else
// "pinned" (the user's model).
func (s *Session) RoutingChoice() string {
	if s.router == nil {
		return ""
	}
	if s.router.Auto() {
		return string(s.router.Objective())
	}
	return string(router.ModePinned)
}

// SubagentsSame is the subagent choice that runs them on the
// orchestrator's current model.
const SubagentsSame = "same"

// SubagentChoice is the subagents' choice as settings and sessions record
// it: a routing mode, a "provider/model" spec, or SubagentsSame.
func (s *Session) SubagentChoice() string {
	switch {
	case s.router != nil && s.router.SubagentRouting():
		return string(s.router.SubagentObjective())
	case s.subagentModel != "":
		return s.subagentModel
	}
	return SubagentsSame
}

// SetSubagentChoice sets what subagent briefs and side tasks run on: a
// routing mode (routed under it; routing must be on), a
// "provider/model" spec, or SubagentsSame. The orchestrator's choice does
// not change.
func (s *Session) SetSubagentChoice(choice string) error {
	if s.router == nil {
		return errors.New("model routing is not available")
	}
	switch {
	case choice == SubagentsSame || choice == "":
		s.router.StopSubagentRouting()
		s.subagentModel = ""
	case strings.Contains(choice, "/"):
		provider, model, _ := strings.Cut(choice, "/")
		if _, err := s.routeModel(provider, model); err != nil {
			return err
		}
		s.router.StopSubagentRouting()
		s.subagentModel = choice
	default:
		o, ok := router.ParseObjective(choice)
		if !ok {
			return fmt.Errorf("unknown subagent choice %q", choice)
		}
		if err := s.router.SetSubagentObjective(o); err != nil {
			return err
		}
		s.subagentModel = ""
	}
	return nil
}

// RecordRouting writes the orchestrator's and the subagents' choices to the
// session when they changed.
func (s *Session) RecordRouting() {
	if s.router == nil || s.inner == nil {
		return
	}
	choice, subagents := s.RoutingChoice(), s.SubagentChoice()
	if choice == s.recordedRouting && subagents == s.recordedSubagents {
		return
	}
	if s.inner.AppendCustomEntry(routingEntryType, map[string]string{"routing": choice, "subagents": subagents}) == nil {
		s.recordedRouting, s.recordedSubagents = choice, subagents
	}
}

// sessionRouting is the last orchestrator and subagent choices a session
// recorded, or "".
func sessionRouting(inner *icodingagent.Session) (choice, subagents string) {
	for _, entry := range inner.Entries() {
		if entry.Base.Type != "custom" {
			continue
		}
		var custom struct {
			CustomType string `json:"customType"`
			Data       struct {
				Routing   string `json:"routing"`
				Subagents string `json:"subagents"`
			} `json:"data"`
		}
		if json.Unmarshal(entry.Raw(), &custom) == nil && custom.CustomType == routingEntryType && custom.Data.Routing != "" {
			choice, subagents = custom.Data.Routing, custom.Data.Subagents
		}
	}
	return choice, subagents
}

// restoreRouting applies the choices a resumed session recorded, or else
// the last ones saved in settings, so a new start keeps the orchestrator's
// mode (only while routing is on) and the subagents' choice; then it
// records them in the session. WOPR_ROUTER, when set, wins. haveModel
// reports whether an orchestrator model resolved, which "pinned" needs.
func (s *Session) restoreRouting(haveModel bool) {
	if s.router == nil || s.routerLoadError != "" || strings.TrimSpace(os.Getenv("WOPR_ROUTER")) != "" {
		return
	}
	s.recordedRouting, s.recordedSubagents = "", ""
	if s.inner != nil {
		s.recordedRouting, s.recordedSubagents = sessionRouting(s.inner)
	}
	settings := s.services.Settings()
	last := cmp.Or(s.recordedRouting, settings.Routing)
	subagents := s.recordedSubagents
	if s.recordedRouting == "" {
		subagents = settings.Subagents
	}
	switch last {
	case "":
	case string(router.ModePinned), string(router.ModeOff):
		if haveModel {
			s.router.PinOrchestrator()
		}
		if last == string(router.ModeOff) && subagents == "" {
			// Recorded before subagents had their own choice.
			subagents = SubagentsSame
		}
	default:
		if o, ok := router.ParseObjective(last); ok {
			// A mode no longer available (routing off, no abliterated
			// model left) keeps the router's own.
			_ = s.router.SetObjective(o)
		}
	}
	if subagents != "" {
		_ = s.SetSubagentChoice(subagents)
	}
	s.RecordRouting()
}

// ReloadRouter rebuilds the router from router.json after setup rewrote
// it. Learned speeds and the new tiers take effect at once; both choices
// carry over while they are still possible, and resting and sticky state
// start over.
func (s *Session) ReloadRouter() error {
	var auto, subagents bool
	var objective, subObjective router.Objective
	had := s.router != nil
	if had {
		auto, objective = s.router.Auto(), s.router.Objective()
		subagents, subObjective = s.router.SubagentRouting(), s.router.SubagentObjective()
	}
	s.initRouter()
	if had && s.routerLoadError == "" {
		if !auto || s.router.SetObjective(objective) != nil {
			s.router.PinOrchestrator()
		}
		if !subagents || s.router.SetSubagentObjective(subObjective) != nil {
			s.router.StopSubagentRouting()
		}
	}
	if s.routerLoadError != "" {
		return errors.New(s.routerLoadError)
	}
	return nil
}

// Router returns the session's model router.
func (s *Session) Router() *router.Router { return s.router }

// routingActive reports whether the router picks the orchestrator's model.
func (s *Session) routingActive() bool {
	return s != nil && s.router != nil && s.router.Auto()
}

// subagentRoutingActive reports whether subagent briefs and side tasks are
// routed instead of running on the orchestrator's model.
func (s *Session) subagentRoutingActive() bool {
	return s != nil && s.router != nil && s.router.SubagentRouting()
}

// routingMode is the subagents' routing mode for logs, or "off".
func (s *Session) routingMode() string {
	if !s.subagentRoutingActive() {
		return string(router.ModeOff)
	}
	return string(s.router.SubagentObjective())
}

// objectiveGate refuses a model the orchestrator's mode forbids, so no
// request silently falls back to one: in uncensored mode a model not
// flagged uncensored, in cost mode a model not listed in a free or
// subscription tier.
func (s *Session) objectiveGate(model *ai.Model) error {
	if s == nil || s.router == nil {
		return nil
	}
	return s.gate(model, s.router.Objective())
}

// subagentGate is objectiveGate for a model a subagent or side task would
// run on: routed subagents answer to their own mode, a model the user chose
// for them is theirs to use, and the orchestrator's model answers to the
// orchestrator's mode.
func (s *Session) subagentGate(model *ai.Model) error {
	switch {
	case s == nil || s.router == nil:
		return nil
	case s.router.SubagentRouting():
		return s.gate(model, s.router.SubagentObjective())
	case s.subagentModel != "":
		return nil
	}
	return s.objectiveGate(model)
}

func (s *Session) gate(model *ai.Model, o router.Objective) error {
	if model == nil {
		return nil
	}
	spec := providerID(model) + "/" + model.ID
	if s.router.Admits(o, spec) {
		return nil
	}
	if o == router.ObjectiveCost {
		return fmt.Errorf("cost mode never pays per token, and %s is not in a free or subscription tier. Pick another mode or a model with /model or Tab", spec)
	}
	return fmt.Errorf("uncensored mode runs only models marked abliterated, and %s is not. Pick another mode or a model with /model or Tab", spec)
}

// subagentBaseModel is the model subagents and side tasks run on when they
// are not routed: the one the user chose for them, else the orchestrator's.
func (s *Session) subagentBaseModel() *ai.Model {
	if s.subagentModel != "" {
		provider, model, _ := strings.Cut(s.subagentModel, "/")
		if m, err := s.routeModel(provider, model); err == nil {
			return m
		}
	}
	return s.activeModel()
}

// activeModel is the model the next provider request will use: the routed
// model while routing is on, else the user's selection.
func (s *Session) activeModel() *ai.Model {
	if s.routingActive() {
		if rs := s.route.Load(); rs != nil && rs.model != nil {
			return rs.model
		}
	}
	return s.Model()
}

// routeModel builds (and caches) a model for a tier candidate. It fails when
// the provider is not configured or authenticated.
func (s *Session) routeModel(provider, model string) (*ai.Model, error) {
	spec := provider + "/" + model
	s.routeModelsMu.Lock()
	cached, ok := s.routeModels[spec]
	s.routeModelsMu.Unlock()
	if ok {
		return cached, nil
	}
	registry := s.services.Registry()
	if modelRuntimeRequiresAuth(provider) && !registry.HasConfiguredAuth(provider) {
		return nil, fmt.Errorf("provider is not configured: %s", provider)
	}
	if !registry.HasModelDefinition(provider, model) {
		if _, generated := ai.LookupModelExact(spec); !generated {
			return nil, fmt.Errorf("unknown model: %s", spec)
		}
	}
	built, err := BuildModel(spec, s.services)
	if err != nil {
		return nil, err
	}
	s.routeModelsMu.Lock()
	s.routeModels[spec] = built
	s.routeModelsMu.Unlock()
	return built, nil
}

// beginRouteRun marks that the next provider request starts a new run, so
// the router classifies the new prompt. Later requests in the run keep the
// route unless a failure replaces it.
func (s *Session) beginRouteRun() { s.routeNeeded.Store(true) }

// ensureRoute returns the route for the current run, deciding one at the
// first request of a run. compacted reports that the decision compacted the
// conversation to fit its model, so the caller must re-project.
func (s *Session) ensureRoute(ctx context.Context, context []agent.AgentMessage) (rs *routeState, compacted bool) {
	boundary := s.routeBoundary.Swap(false)
	if !s.routeNeeded.Swap(false) {
		if rs := s.route.Load(); rs != nil {
			return rs, false
		}
	}
	in := router.Input{Prompt: lastUserPrompt(context), ContextTokens: compaction.EstimateContextTokens(context).Tokens}
	if boundary {
		in.CompactedTokens = s.fitCompactedTokens()
	}
	decision := s.router.Decide(ctx, in)
	s.notePause()
	if decision != nil && decision.CompactToFit {
		decision, compacted = s.compactToFit(ctx, decision)
	}
	rs = &routeState{err: s.noRouteError(in.ContextTokens, s.router.Objective())}
	if decision != nil {
		if m, err := s.routeModel(decision.Provider, decision.Model); err == nil {
			rs = &routeState{decision: decision, model: m, thinking: s.routeThinking(m, decision)}
		} else {
			rs.err = fmt.Errorf("routed to %s, which is unavailable: %w", decision.Spec(), err)
		}
	}
	s.route.Store(rs)
	if rs.decision != nil {
		s.emitRoute(rs)
	}
	return rs, compacted
}

// fitCompactedTokens estimates, for the router, the conversation's size
// after compacting it for a model; nil when compaction is off.
func (s *Session) fitCompactedTokens() func(provider, model string) (int, bool) {
	if !s.compactionSettings().Enabled {
		return nil
	}
	branch := s.currentBranch()
	return func(provider, model string) (int, bool) {
		m, err := s.routeModel(provider, model)
		if err != nil {
			return 0, false
		}
		return compaction.CompactedTokens(branch, s.compactionSettingsFor(m))
	}
}

// compactToFit compacts the conversation for a routed model it has
// outgrown. When the compaction fails, the model the conversation fits
// whole takes the run instead (nil: the selected model).
func (s *Session) compactToFit(ctx context.Context, d *router.Decision) (*router.Decision, bool) {
	if m, err := s.routeModel(d.Provider, d.Model); err == nil && s.runCompaction(ctx, "fit", false, "", s.compactionSettingsFor(m)) {
		s.router.FitCompacted(d)
		return d, true
	}
	return s.router.Whole(d), false
}

// lastUserPrompt returns the text of the newest user message.
func lastUserPrompt(context []agent.AgentMessage) string {
	for _, c := range slices.Backward(context) {
		user := c.User
		if user == nil {
			continue
		}
		var b strings.Builder
		for _, block := range user.Content {
			if text, ok := block.(ai.TextContent); ok {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(text.Text)
			}
		}
		return b.String()
	}
	return ""
}

// notePause reports, once per outage, that the Basic rules route because Jev
// stopped answering.
func (s *Session) notePause() {
	if notice := s.router.TakePauseNotice(); notice != "" {
		s.emitOrderedEvent(agent.RoutingPausedEvent{Reason: notice})
	}
}

func (s *Session) emitRoute(rs *routeState) {
	d := rs.decision
	s.emitOrderedEvent(agent.RouteEvent{
		Provider:    d.Provider,
		Model:       d.Model,
		DisplayName: rs.model.DisplayName,
		Tier:        d.Tier,
		Kind:        string(d.Class.Kind),
		Thinking:    string(rs.thinking),
		Reason:      d.Reason,
		Fallback:    d.Fallback,
	})
}

var rateLimitPattern = regexp.MustCompile(`(?i)(\b429\b|rate[ _-]?limit|quota|usage limit|too many requests|overloaded|capacity|\b529\b)`)

// routeFailover replaces the routed model after a failed response and
// reports whether the run should continue on the replacement. The failed
// attempt is omitted from the transcript the way an automatic retry omits it.
func (s *Session) routeFailover(message *agent.AssistantMessage, toolResults []agent.AgentMessage) bool {
	if !s.routingActive() || message == nil {
		return false
	}
	rs := s.route.Load()
	if rs == nil || rs.decision == nil || rs.model == nil {
		return false
	}
	if message.Provider != providerID(rs.model) || message.ModelID != rs.model.ID {
		return false
	}
	tokens := compaction.EstimateContextTokens(s.agent.Messages()).Tokens
	llm := message.LLMMessage()
	failure := router.Failure{
		Message:       message.ErrorMessage,
		Overflow:      ai.IsContextOverflow(llm, rs.model.Capabilities.ContextWindow),
		RateLimited:   rateLimitPattern.MatchString(message.ErrorMessage),
		ContextTokens: tokens,
	}
	next := s.router.Next(rs.decision, failure)
	for attempts := 0; next != nil && attempts < 8; attempts++ {
		m, err := s.routeModel(next.Provider, next.Model)
		if err != nil {
			next = s.router.Next(next, router.Failure{Message: err.Error(), ContextTokens: tokens})
			continue
		}
		if err := s.omitRecoveryAttempt(message, toolResults); err != nil {
			return false
		}
		replacement := &routeState{decision: next, model: m, thinking: s.routeThinking(m, next)}
		s.router.Adopt(next)
		s.route.Store(replacement)
		s.emitRoute(replacement)
		return true
	}
	return false
}

// sideTaskModel picks the cheapest model that fits contextTokens for
// background work such as compaction summaries. In uncensored and cost mode
// it is nil when no model the mode admits is available.
func (s *Session) sideTaskModel(contextTokens int) *ai.Model {
	fallback := s.subagentBaseModel()
	if s.subagentGate(fallback) != nil || s.RouteError() != nil {
		// A mode never falls back to a model it didn't choose.
		fallback = nil
	}
	if !s.subagentRoutingActive() {
		return fallback
	}
	ref, _, ok := s.router.SideTask(contextTokens)
	if !ok {
		return fallback
	}
	m, err := s.routeModel(ref.Provider, ref.Model)
	if err != nil {
		return fallback
	}
	return m
}

// compactionModel is the side-task model for summarizing the current branch.
func (s *Session) compactionModel() *ai.Model {
	if !s.subagentRoutingActive() {
		return s.subagentBaseModel()
	}
	tokens := compaction.EstimateProjectedContextTokens(s.inner.BuildSessionProjection(), s.currentBranch()).Tokens
	return s.sideTaskModel(tokens)
}

// RouterCommand implements the /router slash command.
func (s *Session) RouterCommand(args string) (string, error) {
	if s.router == nil {
		return "", errors.New("model routing is not available")
	}
	fields := strings.Fields(strings.TrimSpace(args))
	verb := ""
	if len(fields) > 0 {
		verb = strings.ToLower(fields[0])
	}
	switch verb {
	case "", "status":
		out := s.router.Status()
		if s.routerLoadError != "" {
			out = "**Config error:** " + s.routerLoadError + "\n\n" + out
		}
		if selected := s.Model(); selected != nil {
			out += fmt.Sprintf("your model (the orchestrator when pinned or off): %s/%s\n", providerID(selected), selected.ID)
		}
		out += "\n**Efficiency mechanisms:**\n" + s.EfficiencyStatus()
		out += "\n**Context pruning:**\n" + s.PruningStatus()
		return out, nil
	case "on":
		if s.routerLoadError != "" {
			return "", errors.New("router config error: " + s.routerLoadError)
		}
		if !s.router.Available() {
			return "", errors.New("model routing is off: turn it on in /setup → Model routing")
		}
		s.router.SetMode(router.ModeAuto)
		s.subagentModel = ""
		s.beginRouteRun()
		return "Model routing on (auto): the router picks the orchestrator and routes subagents. Use `/router status` to see tiers.", nil
	case "off":
		s.router.SetMode(router.ModeOff)
		s.subagentModel = ""
		s.route.Store(nil)
		if selected := s.Model(); selected != nil {
			return fmt.Sprintf("Model routing off. %s/%s runs everything, subagents included.", providerID(selected), selected.ID), nil
		}
		return "Model routing off.", nil
	case "pin":
		if len(fields) < 2 {
			return "", errors.New("usage: /router pin <tier>")
		}
		if err := s.router.Pin(fields[1]); err != nil {
			return "", err
		}
		s.beginRouteRun()
		return "Pinned routing to tier " + fields[1] + ". `/router unpin` to release.", nil
	case "unpin":
		_ = s.router.Pin("")
		s.beginRouteRun()
		return "Routing unpinned.", nil
	case "why":
		last := s.router.Last()
		if last == nil {
			return "No routing decision yet.", nil
		}
		return fmt.Sprintf("%s\n\n%s\n\nkind %s (%.2f) · complexity %.2f · capability %.2f · deep reasoning %.2f · sensitive %.2f · demand %.2f · classifier %s",
			last.Summary(), last.Reason, last.Class.Kind, last.Class.KindConfidence, last.Class.Complexity, last.Class.Capability,
			last.Class.DeepReasoning, last.Class.Sensitive, last.Demand, last.Class.Source), nil
	case "classify":
		text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(args), fields[0]))
		if text == "" {
			return "", errors.New("usage: /router classify <prompt>")
		}
		class, err := s.router.Classify(context.Background(), router.Input{Prompt: text})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("kind %s (%.2f) · complexity %.2f · capability %.2f · deep reasoning %.2f · sensitive %.2f · demand %.2f · via %s",
			class.Kind, class.KindConfidence, class.Complexity, class.Capability, class.DeepReasoning, class.Sensitive, class.Demand(), class.Source), nil
	default:
		return "", fmt.Errorf("unknown /router command %q. Use status, on, off, pin <tier>, unpin, why, or classify <prompt>; pick modes with /model or Tab.", verb)
	}
}

// observeRouteTiming measures time to first token for each routed request
// and reports it with the request's token counts, so the router learns each
// model's speed. It runs on the agent goroutine.
func (s *Session) observeRouteTiming(ev agent.AgentEvent) {
	if s.router == nil || s.reqStart.IsZero() {
		return
	}
	switch e := ev.(type) {
	case agent.MessageUpdateEvent:
		if s.reqTTFT == 0 {
			s.reqTTFT = time.Since(s.reqStart)
		}
	case agent.MessageEndEvent:
		message := e.Message.Assistant
		if message == nil {
			return
		}
		ttft := s.reqTTFT
		s.reqStart, s.reqTTFT = time.Time{}, 0
		if ttft == 0 || message.Usage == nil || message.StopReason == ai.StopReasonError || message.StopReason == ai.StopReasonAborted {
			return
		}
		usage := *message.Usage
		s.router.Observe(message.Provider+"/"+message.ModelID, usage.Input+usage.CacheRead+usage.CacheWrite, usage.Input+usage.CacheWrite, ttft)
	}
}
