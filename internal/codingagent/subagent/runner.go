package subagent

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Budget bounds one child attempt.
type Budget struct {
	Turns int
	Time  time.Duration
	// Tokens caps the cumulative input tokens across the child's requests.
	Tokens int
	// ToolCalls caps the child's tool calls.
	ToolCalls int
}

// Efforts a brief may ask for.
const (
	EffortQuick    = "quick"
	EffortMedium   = "medium"
	EffortThorough = "thorough"
)

// BudgetFor returns the explore budget for an effort (default medium).
func BudgetFor(effort string) Budget {
	switch effort {
	case EffortQuick:
		return Budget{Turns: 6, Time: 60 * time.Second, Tokens: 100_000, ToolCalls: 10}
	case EffortThorough:
		return Budget{Turns: 30, Time: 400 * time.Second, Tokens: 1_000_000, ToolCalls: 80}
	}
	return Budget{Turns: 16, Time: 180 * time.Second, Tokens: 400_000, ToolCalls: 30}
}

// Wrap-up messages. The nudge arrives at 80% of any budget; once a budget is
// spent every tool call is refused and the child gets graceTurns more
// requests to answer.
const (
	wrapUpNudge   = "Budget nearly exhausted: stop exploring and give your final answer now in the required format (STATUS, CONFIDENCE, ANSWER, EVIDENCE, NOT_CHECKED)."
	budgetRefusal = "Budget exhausted: no more tool calls. Give your final answer now in the required format with the evidence you already have."
	graceTurns    = 2
	graceTime     = 45 * time.Second
)

// Attempt is one child run.
type Attempt struct {
	Model     *ai.Model
	Thinking  ai.ThinkingLevel
	Tools     []agent.AgentTool
	System    string
	Prompt    string
	Budget    Budget
	SessionID string
	// Project rewrites each request's context (ObservationPack), or nil.
	Project func([]agent.AgentMessage) []agent.AgentMessage
	// Progress receives the tool call count and the current tool.
	Progress func(toolCalls int, current string)
	// StreamFn overrides the provider stream (tests).
	StreamFn agent.StreamFn
}

// Outcome is what one attempt produced.
type Outcome struct {
	Text      string
	ToolCalls int
	Turns     int
	Usage     ai.Usage
	Duration  time.Duration
	// BudgetHit names the exhausted budget: "turns", "tokens", "tool calls",
	// or "time".
	BudgetHit string
	// ProviderError is set when the last response failed at the provider.
	ProviderError string
	// Outputs are the tool results, for verifying command quotes.
	Outputs    []string
	Transcript string
}

// Run executes one attempt to completion or budget exhaustion.
func Run(ctx context.Context, at Attempt) Outcome {
	start := time.Now()
	var closed atomic.Bool
	gated := make([]agent.AgentTool, len(at.Tools))
	for i, tool := range at.Tools {
		gated[i] = gatedTool{AgentTool: tool, closed: &closed}
	}
	var (
		mu       sync.Mutex
		out      Outcome
		nudged   bool
		hardStop int // request index at which the gate closed
	)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var child *agent.Agent
	stop := func(reason string) {
		// Called with mu held.
		if out.BudgetHit == "" {
			out.BudgetHit = reason
			closed.Store(true)
			hardStop = out.Turns
		}
	}
	nudge := func() {
		// Called with mu held.
		if !nudged {
			nudged = true
			child.Steer(agent.AgentMessage{User: &agent.UserMessage{Role: agent.RoleUser, Content: []ai.UserContentBlock{ai.TextContent{Text: wrapUpNudge}}, Timestamp: time.Now().UnixMilli()}})
		}
	}
	child = agent.NewAgent(agent.AgentOptions{
		Model:         at.Model,
		Tools:         gated,
		SystemPrompt:  at.System,
		ThinkingLevel: at.Thinking,
		MaxTurns:      at.Budget.Turns + graceTurns + 1,
		SessionID:     at.SessionID,
		StreamFn:      at.StreamFn,
		PrepareRequest: func(_ context.Context, req agent.PrepareRequestContext) *agent.AgentRequestUpdate {
			mu.Lock()
			out.Turns++
			switch {
			case out.BudgetHit != "" && out.Turns > hardStop+graceTurns:
				cancel()
			case out.Turns > at.Budget.Turns:
				stop("turns")
			case out.Turns*5 >= at.Budget.Turns*4:
				nudge()
			}
			mu.Unlock()
			if at.Project == nil {
				return nil
			}
			return &agent.AgentRequestUpdate{Context: at.Project(req.Context)}
		},
		OnEvent: func(ev agent.AgentEvent) {
			switch e := ev.(type) {
			case agent.ToolExecutionStartEvent:
				mu.Lock()
				out.ToolCalls++
				n := out.ToolCalls
				switch {
				case at.Budget.ToolCalls > 0 && n >= at.Budget.ToolCalls:
					stop("tool calls")
				case at.Budget.ToolCalls > 0 && n*5 >= at.Budget.ToolCalls*4:
					nudge()
				}
				mu.Unlock()
				if at.Progress != nil {
					at.Progress(n, describeCall(e.ToolName, e.Args))
				}
			case agent.MessageEndEvent:
				message := e.Message.Assistant
				if message == nil || message.Usage == nil {
					return
				}
				mu.Lock()
				addUsage(&out.Usage, *message.Usage)
				used := out.Usage.Input + out.Usage.CacheRead + out.Usage.CacheWrite
				switch {
				case at.Budget.Tokens > 0 && used >= at.Budget.Tokens:
					stop("tokens")
				case at.Budget.Tokens > 0 && used*5 >= at.Budget.Tokens*4:
					nudge()
				}
				mu.Unlock()
			}
		},
	})
	nudgeTimer := time.AfterFunc(at.Budget.Time*4/5, func() {
		mu.Lock()
		nudge()
		mu.Unlock()
	})
	defer nudgeTimer.Stop()
	stopTimer := time.AfterFunc(at.Budget.Time, func() {
		mu.Lock()
		stop("time")
		mu.Unlock()
		time.AfterFunc(graceTime, cancel)
	})
	defer stopTimer.Stop()

	messages, _ := child.Send(runCtx, at.Prompt)
	mu.Lock()
	defer mu.Unlock()
	out.Duration = time.Since(start)
	out.Text, out.ProviderError = finalText(messages)
	out.Outputs, out.Transcript = transcript(messages)
	if out.BudgetHit == "" && runCtx.Err() != nil && ctx.Err() == nil {
		out.BudgetHit = "time"
	}
	return out
}

// gatedTool refuses calls once the attempt's budget is spent.
type gatedTool struct {
	agent.AgentTool
	closed *atomic.Bool
}

func (g gatedTool) Execute(ctx context.Context, id string, params json.RawMessage, onUpdate agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	if g.closed.Load() {
		return agent.AgentToolResult{Content: budgetRefusal, IsError: true}, nil
	}
	return g.AgentTool.Execute(ctx, id, params, onUpdate)
}

func addUsage(total *ai.Usage, u ai.Usage) {
	total.Input += u.Input
	total.Output += u.Output
	total.CacheRead += u.CacheRead
	total.CacheWrite += u.CacheWrite
	total.TotalTokens += u.TotalTokens
	total.Cost.Total += u.Cost.Total
}

// finalText is the newest assistant text, and the provider error when the
// run's last response failed.
func finalText(messages []agent.AgentMessage) (text, providerError string) {
	for i, message := range slices.Backward(messages) {
		a := message.Assistant
		if a == nil {
			continue
		}
		if i == len(messages)-1 && a.StopReason == ai.StopReasonError {
			providerError = cmp.Or(a.ErrorMessage, "provider error")
		}
		var b strings.Builder
		for _, block := range a.Content {
			if t, ok := block.(ai.TextContent); ok {
				b.WriteString(t.Text)
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return s, providerError
		}
	}
	return "", providerError
}

// transcript renders the child's messages as plain text for the archive and
// collects its tool outputs.
func transcript(messages []agent.AgentMessage) (outputs []string, text string) {
	var b strings.Builder
	for _, m := range messages {
		switch {
		case m.User != nil:
			b.WriteString("## user\n")
			for _, block := range m.User.Content {
				if t, ok := block.(ai.TextContent); ok {
					b.WriteString(t.Text + "\n")
				}
			}
		case m.Assistant != nil:
			b.WriteString("## assistant\n")
			for _, block := range m.Assistant.Content {
				switch c := block.(type) {
				case ai.TextContent:
					b.WriteString(c.Text + "\n")
				case ai.ToolCall:
					args, _ := json.Marshal(c.Arguments)
					fmt.Fprintf(&b, "→ %s %s\n", c.Name, args)
				}
			}
			// A search the provider ran on its own servers leaves no tool
			// result; its pages count as output so web evidence can cite them.
			for _, d := range m.Assistant.Diagnostics {
				if d.Type != ai.DiagnosticServerWebSearch {
					continue
				}
				out := serverSearchText(d.Details)
				outputs = append(outputs, out)
				b.WriteString("## web search (provider)\n" + out + "\n")
			}
		case m.ToolResult != nil:
			out := m.ToolResult.Text()
			outputs = append(outputs, out)
			fmt.Fprintf(&b, "## %s result\n%s\n", m.ToolResult.ToolName, out)
		}
	}
	return outputs, b.String()
}

// serverSearchText lists a provider-run search's query and pages.
func serverSearchText(details map[string]any) string {
	query, _ := details["query"].(string)
	var b strings.Builder
	b.WriteString("query: " + query + "\n")
	results, _ := details["results"].([]any)
	for _, r := range results {
		if m, ok := r.(map[string]any); ok {
			title, _ := m["title"].(string)
			u, _ := m["url"].(string)
			b.WriteString(title + " " + u + "\n")
		}
	}
	return b.String()
}

// describeCall is a short "Tool arg" label for progress rows.
func describeCall(name string, args json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(args, &in)
	for _, key := range []string{"command", "pattern", "path", "id"} {
		if v, ok := in[key].(string); ok && v != "" {
			if len(v) > 60 {
				v = v[:60] + "…"
			}
			return name + " " + v
		}
	}
	return name
}
