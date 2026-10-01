package codingagent

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
)

// minStreamedGeneration is the shortest first-token-to-end time that
// reflects streaming rather than a reply delivered in one burst.
const minStreamedGeneration = 250 * time.Millisecond

// generationTime is the span an output rate is measured over: first token
// to end, or the whole request when the reply arrived in one burst and
// first-token-to-end time measures nothing.
func generationTime(start, firstToken, end time.Time) time.Duration {
	if generation := end.Sub(firstToken); generation >= minStreamedGeneration {
		return generation
	}
	return end.Sub(start)
}

// replySpeed sums the timed replies of one session, overall and per model.
type replySpeed struct {
	session string
	speedSums
	models map[string]*modelUsage
}

// speedSums are the timing totals of a set of replies.
type speedSums struct {
	replies    int
	ttft       time.Duration
	outTokens  int
	generation time.Duration
}

// modelUsage is one model's replies in the session: tokens and cost of every
// reply, and timing of the timed ones.
type modelUsage struct {
	provider, model string
	calls           int
	input, output   int
	cost            float64
	costUnknown     bool
	timed           speedSums
}

func (s *speedSums) add(ttft, generation time.Duration, outTokens int) {
	s.replies++
	s.ttft += ttft
	if generation > 0 && outTokens > 0 {
		s.outTokens += outTokens
		s.generation += generation
	}
}

// reset starts over when the session changed.
func (r *replySpeed) reset(session string) {
	if r.session != session {
		*r = replySpeed{session: session}
	}
}

// add records one timed reply.
func (r *replySpeed) add(session string, ttft, generation time.Duration, outTokens int) {
	r.reset(session)
	r.speedSums.add(ttft, generation, outTokens)
}

// model returns the usage entry for provider/model, creating it.
func (r *replySpeed) model(session, provider, model string) *modelUsage {
	r.reset(session)
	if r.models == nil {
		r.models = map[string]*modelUsage{}
	}
	key := provider + "/" + model
	if r.models[key] == nil {
		r.models[key] = &modelUsage{provider: provider, model: model}
	}
	return r.models[key]
}

// averages are the mean time to first token and the output rate over all
// generation time, so long replies weigh by their length.
func (r *speedSums) averages() (ttft time.Duration, tokensPerSec float64) {
	if r.replies == 0 {
		return 0, 0
	}
	if r.generation > 0 {
		tokensPerSec = float64(r.outTokens) / r.generation.Seconds()
	}
	return r.ttft / time.Duration(r.replies), tokensPerSec
}

// sidebarState is the data the fullscreen sidebar shows beyond the status
// line snapshot. It is updated from agent events and background refreshes on
// the main loop, so the sidebar's Render only reads it.
type sidebarState struct {
	// Card rotation: page shown at pageEpoch, the page last drawn, and the
	// number of pages drawn.
	pageBase  int
	pageEpoch time.Time
	lastPage  int
	// lastHeight is the height of the last render; cardTop and cardRows
	// locate the rotating card in it for mouse clicks.
	lastHeight int
	cardTop    int
	cardRows   int

	// decision is the latest routing decision.
	decision routeDecision
	targets  []router.TargetStatus

	// Lower sections' pages: the one shown, how many the last render had,
	// and the row of their dots (-1 without dots).
	panelPage  int
	panelPages int
	dotsRow    int
	// agentTop is the row of the Agents section (-1 when not drawn), and
	// agentRowIDs the agent on each of its rows, for mouse clicks.
	agentTop    int
	agentRowIDs []string
	// hideHintShown is set once the sidebar-hidden toast has shown.
	hideHintShown bool

	// tasks counts finished subagent tasks; taskRuns counts them by where
	// they ran, in first-seen order.
	tasks    int
	taskRuns []taskRun

	// Efficiency savings: estimated tokens and the number of savings.
	savedTokens int
	savings     int
	// routingSaved estimates what routed replies would have cost on the
	// reference model, minus what they cost.
	routingSaved float64
	reference    *ai.Model
	referenceRef router.ModelRef
	referenceSet bool

	// Reply timing: request start, first streamed token, and the results
	// for the last completed reply.
	requestStart time.Time
	firstToken   time.Time
	tokensPerSec float64
	ttft         time.Duration
	// speed sums every timed reply of the current session for the averages.
	speed replySpeed

	// Estimated tokens of the system prompt and the tool definitions.
	systemTokens int
	toolTokens   int

	git        gitSummary
	gitRunning bool
}

// taskRun is how many tasks ran on one target.
type taskRun struct {
	target string
	count  int
}

// routeDecision is one routing decision as the sidebar lists it.
type routeDecision struct {
	tier, model, reason string
}

const sidebarPageInterval = 8 * time.Second

// observeSidebarEvent records what the sidebar shows from an agent event.
func (m *InteractiveMode) observeSidebarEvent(ev agent.AgentEvent) {
	side := &m.side
	switch e := ev.(type) {
	case agent.TurnStartEvent:
		side.requestStart, side.firstToken = e.Timestamp, time.Time{}
	case agent.RouteEvent:
		// Routing runs between the turn start and the request.
		side.requestStart, side.firstToken = time.Now(), time.Time{}
		model := cmp.Or(e.DisplayName, e.Model)
		side.decision = routeDecision{tier: e.Tier, model: model, reason: e.Reason}
		m.refreshRouteTargets()
	case agent.MessageUpdateEvent:
		if e.Message.Assistant != nil && side.firstToken.IsZero() {
			side.firstToken = time.Now()
		}
	case agent.MessageEndEvent:
		message := e.Message.Assistant
		if message == nil {
			return
		}
		session := m.crashSessionFile()
		var used *modelUsage
		if message.Usage != nil && message.ModelID != "" {
			used = side.speed.model(session, message.Provider, message.ModelID)
			used.calls++
			used.input += message.Usage.Input + message.Usage.CacheRead + message.Usage.CacheWrite
			used.output += message.Usage.Output
			used.cost += message.Usage.Cost.Total
			used.costUnknown = used.costUnknown || message.Usage.Cost.Unknown
		}
		if !side.requestStart.IsZero() && !side.firstToken.IsZero() && message.Usage != nil && message.StopReason != ai.StopReasonError {
			side.ttft = side.firstToken.Sub(side.requestStart)
			generation := generationTime(side.requestStart, side.firstToken, time.Now())
			if generation > 0 && message.Usage.Output > 0 {
				side.tokensPerSec = float64(message.Usage.Output) / generation.Seconds()
			}
			side.speed.add(session, side.ttft, generation, message.Usage.Output)
			if used != nil {
				used.timed.add(side.ttft, generation, message.Usage.Output)
			}
		}
		side.requestStart, side.firstToken = time.Time{}, time.Time{}
		if message.Usage != nil && m.routingEnabled() {
			side.routingSaved += m.routingSaving(message)
		}
	case agent.SavingsEvent:
		side.savings++
		side.savedTokens += max(0, e.Tokens)
	case agent.ToolExecutionEndEvent:
		if d, ok := taskDetails(e.Result.Details); ok && e.ToolName == "task" && d.Spec != "" && !d.Background {
			provider, _, _ := strings.Cut(d.Spec, "/")
			m.recordTaskRun(m.targetName(provider))
		}
	case agent.AgentEndEvent:
		m.refreshSidebarData()
		m.saveSidebarSnapshot()
	}
}

// recordTaskRun counts a finished task on target.
func (m *InteractiveMode) recordTaskRun(target string) {
	side := &m.side
	side.tasks++
	for i := range side.taskRuns {
		if side.taskRuns[i].target == target {
			side.taskRuns[i].count++
			return
		}
	}
	side.taskRuns = append(side.taskRuns, taskRun{target: target, count: 1})
}

// routingSaving estimates what a routed reply saved: its cost on the
// reference model at list price minus its actual cost.
func (m *InteractiveMode) routingSaving(message *agent.AssistantMessage) float64 {
	side := &m.side
	if !side.referenceSet {
		side.reference, side.referenceRef = m.referenceModel()
		side.referenceSet = true
	}
	ref := side.referenceRef
	if side.reference == nil || (message.Provider == ref.Provider && message.ModelID == ref.Model) {
		return 0
	}
	// Price a copy: CalculateCost writes Cost into the usage it is given.
	usage := *message.Usage
	return max(0, ai.CalculateCost(side.reference, &usage).Total-message.Usage.Cost.Total)
}

// referenceModel is the most capable routing target with a list price: what
// every prompt would run on without routing.
func (m *InteractiveMode) referenceModel() (*ai.Model, router.ModelRef) {
	r := m.sessionRouter()
	if r == nil {
		return nil, router.ModelRef{}
	}
	var best *ai.Model
	var bestRef router.ModelRef
	bestCapability := -1.0
	for _, tier := range r.Config().Tiers {
		for _, ref := range tier.Models {
			capability := cmp.Or(ref.Capability, tier.Capability)
			generated, ok := ai.LookupModel(ref.Spec())
			if !ok || capability <= bestCapability {
				continue
			}
			if model := generated.ToModel(); model.CostRates().Input > 0 || model.CostRates().Output > 0 {
				best, bestRef, bestCapability = model, ref, capability
			}
		}
	}
	return best, bestRef
}

// refreshRouteTargets copies the router's cached tier health.
func (m *InteractiveMode) refreshRouteTargets() {
	if r := m.sessionRouter(); r != nil {
		m.side.targets = r.Targets()
	} else {
		m.side.targets = nil
	}
}

// refreshSidebarData updates the sidebar's estimates and starts a git status
// refresh; it runs on the main loop at startup and after each run.
func (m *InteractiveMode) refreshSidebarData() {
	m.side.systemTokens = ai.EstimateTextTokens(m.currentSystemPrompt())
	m.side.toolTokens = 0
	if m.agent != nil {
		for _, tool := range m.agent.Tools() {
			if data, err := json.Marshal(tool.Schema()); err == nil {
				m.side.toolTokens += ai.EstimateTextTokens(string(data))
			}
		}
	}
	m.refreshRouteTargets()
	if m.side.gitRunning || m.opts.CWD == "" {
		return
	}
	m.side.gitRunning = true
	cwd := m.opts.CWD
	go func() {
		summary := readGitSummary(cwd)
		m.postUITask(func() {
			m.side.git, m.side.gitRunning = summary, false
			m.tuiInst.RequestRender()
		})
	}()
}

// startSidebar refreshes the sidebar data and advances the rotating card:
// every second it asks for a render when the card's page changed.
func (m *InteractiveMode) startSidebar(ctx context.Context) {
	m.side.pageEpoch = time.Now()
	m.refreshSidebarData()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.postUITask(func() {
					if m.homeVisible() {
						return
					}
					// Running agents tick the prompt's indicator and the
					// sidebar's elapsed times; finished ones fold after a
					// minute.
					if m.agentsLive(time.Now()) {
						m.tuiInst.RequestRender()
						return
					}
					if m.side.lastPage < 0 || !m.sidebarShown(m.tuiInst.Width()) {
						return
					}
					if _, page := sidebarCardLayout(m.side.lastHeight, sidebarCardPages, m.side.pageBase, time.Since(m.side.pageEpoch)); page != m.side.lastPage {
						m.tuiInst.RequestRender()
					}
				})
			}
		}
	}()
}

// sidebarStackMinRows is the terminal height from which the card shows all
// its pages stacked instead of rotating.
const sidebarStackMinRows = 55

// sidebarCardLayout decides whether the card stacks its pages at a terminal
// height (0 when unknown), and otherwise which page shows after elapsed
// since the page base was set.
func sidebarCardLayout(height, pages, base int, elapsed time.Duration) (stacked bool, page int) {
	if pages <= 0 {
		return false, 0
	}
	if height >= sidebarStackMinRows {
		return true, -1
	}
	return false, (base + int(max(0, elapsed)/sidebarPageInterval)) % pages
}

// gitSummary is the working tree state from git status.
type gitSummary struct {
	branch        string
	changed       int
	ahead, behind int
}

// readGitSummary runs git status without network access or optional locks.
func readGitSummary(cwd string) gitSummary {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "status", "--porcelain=v2", "--branch")
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	out, err := cmd.Output()
	if err != nil {
		return gitSummary{}
	}
	return parseGitStatusV2(string(out))
}

// parseGitStatusV2 reads `git status --porcelain=v2 --branch`: the branch
// headers and one line per changed or untracked path.
func parseGitStatusV2(out string) gitSummary {
	var summary gitSummary
	for line := range strings.Lines(out) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			if head := strings.TrimPrefix(line, "# branch.head "); head != "(detached)" {
				summary.branch = head
			}
		case strings.HasPrefix(line, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(fields) == 2 {
				summary.ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
				summary.behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
			}
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "), strings.HasPrefix(line, "? "):
			summary.changed++
		}
	}
	return summary
}
