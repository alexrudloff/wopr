package codingagent

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/alexrudloff/wopr/agent"
)

const savingsStatusKey = "savings"

// handleSavingsEvent shows an efficiency mechanism's saving: one dim
// transcript line and a footer status that clears after a few seconds.
func (m *InteractiveMode) handleSavingsEvent(e agent.SavingsEvent) {
	m.showStatus(fmt.Sprintf("⚡ %s · %s", e.Mechanism, e.Saving))
	line := m.statusLine
	line.SetKeyedStatus(savingsStatusKey, fmt.Sprintf("⚡ %s · %s", e.Mechanism, e.Saving))
	time.AfterFunc(4*time.Second, func() { line.SetKeyedStatus(savingsStatusKey, "") })
}

// handleRouteEvent shows a routing decision: one dim transcript line and a
// footer status so the active model is always visible.
func (m *InteractiveMode) handleRouteEvent(e agent.RouteEvent) {
	thinking := cmp.Or(e.Thinking, "off")
	arrow := "⇄"
	verb := "routed"
	if e.Fallback {
		arrow = "↪"
		verb = "rerouted"
	}
	m.route = routeInfo{provider: e.Provider, model: e.DisplayName, tier: e.Tier, thinking: e.Thinking}
	m.route.model = cmp.Or(m.route.model, e.Model)
	m.statusLine.SetKeyedStatus("route", fmt.Sprintf("%s %s/%s · %s", arrow, e.Tier, e.Model, thinking))
	target := m.route.model
	if provider := m.providerName(e.Provider); provider != "" {
		target += " " + provider
	}
	notice := fmt.Sprintf("%s %s to %s", arrow, titlecase(verb), target)
	if e.Kind != "" {
		// Basic decisions don't classify the prompt.
		notice += " · " + e.Kind
	}
	if thinking != "off" {
		notice += " · thinking " + thinking
	}
	m.showStatus(notice)
}

// builtinPromptCommands are the report-only review prompts offered as slash
// commands.
var builtinPromptCommands = []PromptTemplate{
	{
		Name:        "review",
		Description: "Review the current changes: bugs first, then simplifications",
		Content:     "Review the current code changes. Report correctness bugs first, then over-engineering and simplification opportunities. One line per finding: <file>:<line>: <tag> <problem>. <fix>. Tags: bug (wrong behavior, missed edge case, broken error handling), delete (dead code or speculative feature), stdlib (reinvented standard library), native (dependency doing what the platform does), yagni (abstraction with one implementation), shrink (same logic, fewer lines). End with the bug count and the net lines removable. If nothing to report: 'No findings. Ship.' Report only, change nothing.",
	},
	{
		Name:        "audit",
		Description: "Audit the whole repository for over-engineering",
		Content:     "Audit the entire repository for over-engineering. Scan the whole tree, not a diff. One line per finding, ranked biggest cut first: <tag> <what to cut>. <replacement>. [path]. Tags: delete (dead code or speculative feature), stdlib (reinvented standard library), native (dependency doing what the platform does), yagni (abstraction with one implementation), shrink (same logic, fewer lines). End with the net lines and dependencies removable. If nothing to cut: 'Nothing to cut. Ship.' Report only, change nothing.",
	},
	{
		Name:        "debt",
		Description: "List every simplify: comment as a debt ledger",
		Content:     "Harvest every `simplify:` comment in this repository into a debt ledger so deferrals do not rot. Grep the whole tree for comment markers (grep -rnE '(#|//) ?simplify:' ., skipping node_modules, .git, and build output). One row per marker, grouped by file: <file>:<line> — <what was simplified>. ceiling: <the limit named in the comment>. upgrade: <the trigger to revisit>. Tag any marker that names no upgrade path or trigger as no-trigger. End with the count of markers and how many lack a trigger. If none: 'No simplify: debt.' Report only, change nothing.",
	},
}

// withBuiltinPromptCommands adds the built-in review commands after the
// loaded templates, so a user or project template with the same name wins.
func withBuiltinPromptCommands(templates []PromptTemplate) []PromptTemplate {
	out := templates
	for _, command := range builtinPromptCommands {
		if slices.ContainsFunc(templates, func(t PromptTemplate) bool { return t.Name == command.Name }) {
			continue
		}
		command.Scope = "builtin"
		out = append(out, command)
	}
	return out
}
