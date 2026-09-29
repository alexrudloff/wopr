package codingagent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/tui"
)

// serverSearchCards draws each web search a provider ran on its own servers
// during a reply as a web_search card: the query, then the pages it found.
func (m *InteractiveMode) serverSearchCards(message *agent.AssistantMessage) []*tui.ToolExecutionComponent {
	if message == nil {
		return nil
	}
	var out []*tui.ToolExecutionComponent
	for _, d := range message.Diagnostics {
		if d.Type != ai.DiagnosticServerWebSearch {
			continue
		}
		query, _ := d.Details["query"].(string)
		args, _ := json.Marshal(map[string]string{"query": query})
		comp := tui.NewToolExecutionComponent("web_search", tui.HeaderForTool("web_search", args, m.opts.CWD))
		comp.Cwd = m.opts.CWD
		comp.SetArgs(args)
		comp.SetExpanded(m.toolsExpanded)
		var b strings.Builder
		results, _ := d.Details["results"].([]any)
		for i, r := range results {
			entry, _ := r.(map[string]any)
			title, _ := entry["title"].(string)
			url, _ := entry["url"].(string)
			fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, title, url)
		}
		errText, _ := d.Details["error"].(string)
		if errText != "" {
			comp.SetResult("search failed: "+errText, true, 0)
		} else {
			comp.SetResult(strings.TrimRight(b.String(), "\n"), false, 0)
		}
		out = append(out, comp)
	}
	return out
}
