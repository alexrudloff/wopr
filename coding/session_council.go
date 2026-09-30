package coding

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/internal/codingagent/router"
	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
	"github.com/alexrudloff/wopr/internal/codingagent/web"
	"github.com/alexrudloff/wopr/internal/text"
)

// War council: in a Global Thermonuclear War session every user prompt,
// and every council tool call, goes in parallel to every routed model the
// user set up, each at its own deepest thinking. Their proposals come back
// to the orchestrator in one message to synthesize before it works.

// warCouncilEntryType is the custom session entry that turns the council on
// for a session, so a resumed session keeps it.
const warCouncilEntryType = "war_council_on"

// sessionCouncil is the Session's war council state.
type sessionCouncil struct {
	on      atomic.Bool
	private atomic.Bool
	// before is the caller's record of the session's setup before the
	// council came on, restored when it goes off.
	before atomic.Pointer[json.RawMessage]
	// timeout overrides the settings' time limit (tests).
	timeout time.Duration
	// streamFn overrides member provider streams (tests).
	streamFn agent.StreamFn
	// limiter runs members at once, bounded only by per-provider limits:
	// GTW is all-out, so the subagents' overall cap doesn't apply.
	limiterOnce sync.Once
	limiter     *subagent.Limiter
}

// councilMaxParallel bounds the members running at once; it is above any
// realistic council size.
const councilMaxParallel = 64

// EnableWarCouncil turns the war council on for this session. private keeps
// it to private connections, as private mode does.

func (s *Session) EnableWarCouncil(private bool) { s.SetWarCouncil(true, private, nil) }

// SetWarCouncil turns the war council on or off for this session and records
// it, so a resumed session keeps it. before is the caller's record of the
// setup to restore when it goes off; it is kept with the session until then.
func (s *Session) SetWarCouncil(on, private bool, before json.RawMessage) {
	if !on {
		private, before = false, nil
	}
	s.council.on.Store(on)
	s.council.private.Store(private)
	if before == nil {
		s.council.before.Store(nil)
	} else {
		s.council.before.Store(&before)
	}
	if s.inner != nil {
		_ = s.inner.AppendCustomEntry(warCouncilEntryType, map[string]any{"on": on, "private": private, "before": before})
	}
	s.setCouncilTool(on)
}

// WarCouncil reports whether the war council is on for this session.
func (s *Session) WarCouncil() bool { return s.council.on.Load() }

// WarCouncilSize is how many members the council would ask now.
func (s *Session) WarCouncilSize() int {
	members, _ := s.councilMembers()
	return len(members)
}

// WarCouncilBefore is the record SetWarCouncil kept when the council came
// on, or nil.
func (s *Session) WarCouncilBefore() json.RawMessage {
	if p := s.council.before.Load(); p != nil {
		return *p
	}
	return nil
}

// restoreWarCouncil turns the council on or off as the session recorded.
func (s *Session) restoreWarCouncil(inner *icodingagent.Session) {
	on, private := false, false
	var before json.RawMessage
	if inner != nil {
		for _, entry := range inner.Entries() {
			if entry.Base.Type != "custom" {
				continue
			}
			var custom struct {
				CustomType string `json:"customType"`
				Data       struct {
					On      bool            `json:"on"`
					Private bool            `json:"private"`
					Before  json.RawMessage `json:"before"`
				} `json:"data"`
			}
			if json.Unmarshal(entry.Raw(), &custom) == nil && custom.CustomType == warCouncilEntryType {
				on, private, before = custom.Data.On, custom.Data.Private, custom.Data.Before
			}
		}
	}
	s.council.on.Store(on)
	s.council.private.Store(private)
	if on && len(before) > 0 && string(before) != "null" {
		s.council.before.Store(&before)
	} else {
		s.council.before.Store(nil)
	}
	s.setCouncilTool(on)
}

// setCouncilTool installs or removes the council tool.
func (s *Session) setCouncilTool(on bool) {
	if s.agent == nil {
		return
	}
	tools := slices.DeleteFunc(slices.Clone(s.agent.Tools()), func(t agent.AgentTool) bool { return t.Name() == councilToolName })
	s.tools = slices.DeleteFunc(s.tools, func(t agent.AgentTool) bool { return t.Name() == councilToolName })
	if on && s.tasks.tool != nil {
		tool := councilTool{s: s}
		s.tools = append(s.tools, tool)
		tools = append(tools, tool)
	}
	s.agent.SetTools(tools)
}

// councilProposal is one member's answer as the orchestrator reads it.
type councilProposal struct {
	name, spec string
	text       string
	// sources are the web pages the proposal cites.
	sources []string
}

// CouncilResult is one council round: the proposals and who could not
// answer.
type CouncilResult struct {
	// Proposals are the members that answered, in ranking order.
	Proposals []councilProposal
	// Skipped names the members that could not be asked or answered, with why.
	Skipped []string
}

// Models names the members that answered.
func (r CouncilResult) Models() []string {
	out := make([]string, len(r.Proposals))
	for i, p := range r.Proposals {
		out[i] = p.name
	}
	return out
}

// Sources are each answering member's cited web pages, by member name;
// members that cited none are left out.
func (r CouncilResult) Sources() map[string][]string {
	out := map[string][]string{}
	for _, p := range r.Proposals {
		if len(p.sources) > 0 {
			out[p.name] = p.sources
		}
	}
	return out
}

// webSourceRe finds the page of a "web:" evidence line in a rendered
// proposal.
var webSourceRe = regexp.MustCompile(`web:\s*<?(https?://[^\s"'<>` + "`" + `]+)`)

// webSources lists the distinct pages a proposal cites, in order.
func webSources(answer string) []string {
	var out []string
	for _, m := range webSourceRe.FindAllStringSubmatch(answer, -1) {
		if u := strings.TrimRight(m[1], ".,;:)"); !slices.Contains(out, u) {
			out = append(out, u)
		}
	}
	return out
}

// councilMembers is who the council asks: every routed model but the
// orchestrator and those the user left out, private connections only when
// the session is private. Members that can't answer are returned as skipped.
func (s *Session) councilMembers() (members []router.CouncilMember, skipped []string) {
	if s.router == nil {
		return nil, nil
	}
	excluded, _ := s.services.Settings().GetWarCouncil()
	if m := s.Model(); m != nil {
		excluded = append(slices.Clone(excluded), providerID(m)+"/"+m.ID)
	}
	for _, m := range s.router.CouncilMembers(s.council.private.Load(), excluded...) {
		if m.Skip != "" {
			skipped = append(skipped, m.Ref.Spec()+" ("+m.Skip+")")
			continue
		}
		members = append(members, m)
	}
	return members, skipped
}

// runCouncil asks every council member question in parallel and collects
// their checked proposals. A member past the time limit is dropped, not
// waited for.
func (s *Session) runCouncil(ctx context.Context, question string) CouncilResult {
	members, skipped := s.councilMembers()
	_, timeout := s.services.Settings().GetWarCouncil()
	if s.council.timeout > 0 {
		timeout = s.council.timeout
	}
	brief := s.councilBrief(question)
	var calls []councilCall
	for _, member := range members {
		model, err := s.routeModel(member.Ref.Provider, member.Ref.Model)
		if err != nil {
			skipped = append(skipped, member.Ref.Spec()+" (unavailable: "+text.Clip(err.Error(), 80)+")")
			continue
		}
		route := subagent.Route{
			Model:    model,
			Thinking: ai.ClampThinkingLevel(model, ai.ThinkingMax),
			Provider: member.Ref.Provider,
			Spec:     member.Ref.Spec(),
			Reason:   "war council",
			Source:   "council",
			Mode:     "council",
		}
		name := cmp.Or(model.DisplayName, member.Ref.Spec())
		calls = append(calls, councilCall{name: name, spec: route.Spec, run: func(ctx context.Context) (string, error) {
			spec := subagent.Spec{Type: subagent.TypePropose, Description: "council · " + name, Brief: brief, Effort: subagent.EffortThorough}
			for attempt := 0; ; attempt++ {
				id := subagent.NewTaskID(fmt.Sprintf("council\x00%s\x00%d\x00%s", route.Spec, time.Now().UnixNano(), question))
				details, answer, err := s.councilRunner(councilHost{s: s, route: route}).Run(ctx, id, spec, nil)
				// A provider that refuses its server search answers again
				// with DuckDuckGo, as the orchestrator does.
				if attempt == 0 && details.State != subagent.StatusDone && s.refuseServerSearch(member.Ref.Provider, answer, err) {
					continue
				}
				return answer, err
			}
		}})
	}
	proposals, late := gatherCouncil(ctx, calls, timeout)
	return CouncilResult{Proposals: proposals, Skipped: append(skipped, late...)}
}

// councilCall is one member's run.
type councilCall struct {
	name, spec string
	run        func(context.Context) (string, error)
}

// gatherCouncil runs the calls in parallel, each under the time limit, and
// returns their proposals in call order and why the rest aren't there. It
// returns once every call finished or the limit passed: a late member is
// cancelled and dropped, never waited for, even if its provider is slow to
// notice.
func gatherCouncil(ctx context.Context, calls []councilCall, timeout time.Duration) (proposals []councilProposal, missing []string) {
	type done struct {
		i      int
		answer string
		err    error
	}
	finished := make(chan done, len(calls))
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for i, call := range calls {
		go func() {
			answer, err := call.run(callCtx)
			if callCtx.Err() != nil {
				// An answer cut off by the limit or a stop isn't a proposal.
				err = callCtx.Err()
			}
			finished <- done{i, answer, err}
		}()
	}
	results := make([]*done, len(calls))
	for pending := len(calls); pending > 0; pending-- {
		select {
		case d := <-finished:
			results[d.i] = &d
		case <-callCtx.Done():
			pending = 0
		}
	}
	for i, call := range calls {
		r := results[i]
		switch {
		case r != nil && r.err == nil:
			proposals = append(proposals, councilProposal{name: call.name, spec: call.spec, text: r.answer, sources: webSources(r.answer)})
		case ctx.Err() != nil:
			missing = append(missing, call.name+" (stopped)")
		case r == nil || errors.Is(callCtx.Err(), context.DeadlineExceeded):
			missing = append(missing, fmt.Sprintf("%s (late: dropped after %s)", call.name, timeout.Round(time.Second)))
		default:
			missing = append(missing, call.name+" (failed: "+text.Clip(r.err.Error(), 120)+")")
		}
	}
	return proposals, missing
}

// councilRunner is a task runner on host, fixed to one member's route,
// sharing the session's task registry (the sidebar lists members as live
// agents) and provider limits.
func (s *Session) councilRunner(host subagent.Host) *subagent.Tool {
	stream := s.council.streamFn
	if stream == nil {
		stream = func(ctx context.Context, model *ai.Model, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
			options.ServerWebSearch = s.councilWeb() == councilWebOpen && s.useServerSearch(model, transcript)
			return model.Provider.Stream(ctx, transcript, options)
		}
	}
	s.council.limiterOnce.Do(func() {
		var perProvider map[string]int
		if s.router != nil {
			perProvider = s.router.Config().Subagents.ProviderParallel
		}
		s.council.limiter = subagent.NewLimiter(councilMaxParallel, perProvider)
	})
	t := &subagent.Tool{Host: host, SessionID: s.ID(), StreamFn: stream, Limiter: s.council.limiter}
	if s.tasks.tool != nil {
		t.Registry = s.tasks.tool.Registry
	}
	return t
}

// councilHost routes every attempt to its member's model: no escalation or
// failover to another model, since each member is its own voice.
type councilHost struct {
	taskHost
	route subagent.Route
}

// Tools are the explore tools plus the council's web tools.
func (h councilHost) Tools() []agent.AgentTool {
	return append(h.taskHost.Tools(), h.s.councilWebTools()...)
}

// councilWebTools are the web tools the council may use: both normally,
// only a private search in a private council.
func (s *Session) councilWebTools() []agent.AgentTool {
	var out []agent.AgentTool
	switch s.councilWeb() {
	case councilWebOpen:
		for _, name := range []string{web.SearchToolName, web.FetchToolName} {
			if tool := s.toolNamed(name); tool != nil {
				out = append(out, tool)
			}
		}
	case councilWebPrivate:
		if tool := s.toolNamed(web.SearchToolName); tool != nil {
			out = append(out, tool)
		}
	}
	return out
}

// councilWebAccess is what web the council members get.
type councilWebAccess int

const (
	councilWebNone    councilWebAccess = iota // a private council without a private search
	councilWebPrivate                         // a private council: the private SearXNG only
	councilWebOpen                            // web_search and web_fetch
)

// councilWeb is the council's web access. A private council keeps to a
// SearXNG marked private and never fetches pages, since a fetch sends the
// URL out.
func (s *Session) councilWeb() councilWebAccess {
	if !s.council.private.Load() {
		return councilWebOpen
	}
	if s.webSearch.tool != nil && s.webSearch.tool.Backend.Private() {
		return councilWebPrivate
	}
	return councilWebNone
}

// refuseServerSearch turns server search off for provider when a member's
// failure came from it, and reports whether to ask the member again.
func (s *Session) refuseServerSearch(provider, answer string, err error) bool {
	text := answer
	if err != nil {
		text += " " + err.Error()
	}
	return s.serverSearchRefused(&agent.AssistantMessage{Provider: provider, StopReason: ai.StopReasonError, ErrorMessage: text})
}

func (h councilHost) Route(context.Context, subagent.Request) (subagent.Route, error) {
	return h.route, nil
}

func (councilHost) Escalate(subagent.Request, subagent.Route) (subagent.Route, bool) {
	return subagent.Route{}, false
}

func (councilHost) Failover(subagent.Route, string) (subagent.Route, bool) {
	return subagent.Route{}, false
}

// councilBrief is what every member sees: the question, the conversation's
// recent turns, and the files the session has worked with.
func (s *Session) councilBrief(question string) string {
	var b strings.Builder
	b.WriteString("REQUEST:\n" + strings.TrimSpace(question) + "\n")
	var turns []string
	for _, m := range slices.Backward(s.agent.Messages()) {
		if len(turns) >= 6 {
			break
		}
		switch {
		case m.User != nil:
			if t := strings.TrimSpace(turnText(m)); t != "" && t != strings.TrimSpace(question) {
				turns = append(turns, "User: "+text.Clip(t, 600))
			}
		case m.Assistant != nil:
			if t := strings.TrimSpace(turnText(m)); t != "" {
				turns = append(turns, "Lead model: "+text.Clip(t, 900))
			}
		}
	}
	if len(turns) > 0 {
		slices.Reverse(turns)
		b.WriteString("\nCONVERSATION SO FAR (most recent last):\n" + strings.Join(turns, "\n") + "\n")
	}
	if files := s.fileWatch.paths(); len(files) > 0 {
		b.WriteString("\nFILES THE SESSION HAS WORKED WITH:\n- " + strings.Join(files, "\n- ") + "\n")
	}
	switch s.councilWeb() {
	case councilWebNone:
		b.WriteString("\nNo web access in this private session: answer from the code and what you know.\n")
	default:
		b.WriteString("\nSearch the web when the request needs current information; cite a page as - web: <url> \"<text>\".\n")
	}
	return b.String()
}

// CouncilMessageType is the custom message that carries the council's
// proposals to the orchestrator.
const CouncilMessageType = subagent.CouncilMessageType

// councilMessage is the council round as the orchestrator reads it and the
// transcript shows it.
func councilMessage(r CouncilResult) agent.AgentMessage {
	var b strings.Builder
	if len(r.Proposals) == 0 {
		b.WriteString("War council: no member answered. Work on the request yourself.")
	} else {
		fmt.Fprintf(&b, "War council: %d proposals for the request above, each from another model working independently. Synthesize the best parts into your plan or answer; don't just pick one. Say where they disagree and why your choice wins. wopr checked their quotes; unverified ones are marked.\n", len(r.Proposals))
		for i, p := range r.Proposals {
			fmt.Fprintf(&b, "\n### Proposal %d: %s (%s)\n%s\n", i+1, p.name, p.spec, strings.TrimSpace(p.text))
			if len(p.sources) > 0 {
				b.WriteString("Sources: " + strings.Join(p.sources, ", ") + "\n")
			}
		}
	}
	if len(r.Skipped) > 0 {
		b.WriteString("\nNot heard from: " + strings.Join(r.Skipped, "; ") + ".")
	}
	return agent.AgentMessage{Custom: map[string]any{
		"role":       agent.RoleCustom,
		"customType": CouncilMessageType,
		"content":    b.String(),
		"display":    true,
		"details":    map[string]any{"models": r.Models(), "skipped": r.Skipped, "sources": r.Sources()},
		"timestamp":  time.Now().UnixMilli(),
	}}
}

// councilAtPrompt runs the council on a user prompt in a war council
// session and returns the message to add after it, or nil.
func (s *Session) councilAtPrompt(ctx context.Context, messages []agent.AgentMessage) []agent.AgentMessage {
	if !s.council.on.Load() || !slices.ContainsFunc(messages, func(m agent.AgentMessage) bool { return m.User != nil }) {
		return nil
	}
	question := lastUserPrompt(messages)
	if strings.TrimSpace(question) == "" {
		return nil
	}
	return []agent.AgentMessage{councilMessage(s.runCouncil(ctx, question))}
}

// councilToolName is the tool the orchestrator puts a hard sub-question to
// the council with.
const councilToolName = "council"

// councilTool puts a question to the war council mid-task.
type councilTool struct{ s *Session }

func (councilTool) Name() string  { return councilToolName }
func (councilTool) Label() string { return "War council" }

func (councilTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }

func (councilTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name: councilToolName,
		Description: "The war council: every other model the user has set up, working in parallel at its deepest thinking. Slow and expensive.\n" +
			"- ask (default): put a hard question to the council; you get every member's independent proposal to synthesize. For design decisions, tricky bugs, and questions where a second opinion matters, not for lookups.\n" +
			"- build: have every member make a code change in its own copy of the repository and run the tests there; you get each candidate's diff and test result. For significant changes where independent attempts help (a feature, a hard fix, a refactor), or when the user asks the council to build; not for small edits. The user's files are untouched until you apply.\n" +
			"- apply: apply one candidate from the latest build to the user's files (reversible with /undo). To combine candidates, apply the best and merge the rest with your edit tools.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":       map[string]any{"type": "string", "enum": []string{"ask", "build", "apply"}},
				"question":     map[string]any{"type": "string", "description": "ask: the question, with the facts and constraints the members need; they see this and a short brief of the conversation"},
				"task":         map[string]any{"type": "string", "description": "build: the change to make, complete enough to build without asking: goal, files involved, constraints, how to know it works"},
				"test_command": map[string]any{"type": "string", "description": "build: the command that tests a candidate (default: the session's last passing test command, else detected from the project)"},
				"candidate":    map[string]any{"type": "string", "description": "apply: the candidate number"},
			},
		},
	}
}

func (t councilTool) Execute(ctx context.Context, callID string, params json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct {
		Action      string `json:"action"`
		Question    string `json:"question"`
		Task        string `json:"task"`
		TestCommand string `json:"test_command"`
		Candidate   string `json:"candidate"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return agent.AgentToolResult{}, err
	}
	switch in.Action {
	case "build":
		if strings.TrimSpace(in.Task) == "" {
			return agent.AgentToolResult{}, errors.New("council build needs a task")
		}
		content, title, err := t.s.runCouncilBuild(ctx, in.Task, strings.TrimSpace(in.TestCommand))
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{Content: content, Preview: title}, nil
	case "apply":
		content, err := t.s.applyCouncilCandidate(ctx, callID, in.Candidate)
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		return agent.AgentToolResult{Content: content, Preview: content}, nil
	}
	if strings.TrimSpace(in.Question) == "" {
		return agent.AgentToolResult{}, errors.New("council needs a question")
	}
	r := t.s.runCouncil(ctx, in.Question)
	content, _ := councilMessage(r).Custom["content"].(string)
	return agent.AgentToolResult{Content: content, Preview: fmt.Sprintf("%d proposals", len(r.Proposals))}, nil
}

// turnText is the plain text of a user prompt or an assistant reply.
func turnText(m agent.AgentMessage) string {
	var parts []string
	switch {
	case m.User != nil:
		for _, block := range m.User.Content {
			if t, ok := block.(ai.TextContent); ok {
				parts = append(parts, t.Text)
			}
		}
	case m.Assistant != nil:
		for _, block := range m.Assistant.Content {
			if t, ok := block.(ai.TextContent); ok {
				parts = append(parts, t.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// StartWar puts a headless session in Global Thermonuclear War, as ctrl+x g
// does interactively: the top usable model in the router's order (private
// connections only in private mode) at its deepest thinking, routing off,
// and the war council on. It returns the model's spec.
func (s *Session) StartWar() (string, error) {
	if s.router == nil {
		return "", errors.New("it needs model routing set up (router.json)")
	}
	private := s.router.Auto() && s.router.Objective() == router.ObjectivePrivate
	cfg := s.router.Config()
	order := slices.Clone([]string(cfg.Ranking))
	for _, tier := range cfg.Tiers {
		for _, ref := range tier.Models {
			if spec := ref.Provider + "/" + ref.Model; !slices.Contains(order, spec) {
				order = append(order, spec)
			}
		}
	}
	for _, spec := range order {
		if private && !s.router.IsPrivacySafe(spec) {
			continue
		}
		provider, model, _ := strings.Cut(spec, "/")
		m, err := s.routeModel(provider, model)
		if err != nil {
			continue
		}
		s.router.SetMode(router.ModeOff)
		if err := s.SetModel(m); err != nil {
			return "", err
		}
		if levels := ai.GetSupportedThinkingLevels(m); len(levels) > 1 {
			_ = s.SetThinkingLevel(levels[len(levels)-1])
		}
		s.SetWarCouncil(true, private, nil)
		return spec, nil
	}
	return "", errors.New("no model in the router's order can be used")
}
