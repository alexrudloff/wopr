package pruning

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
)

// Strategy names, as reported in savings and edits.
const (
	StrategyDedupe      = "dedupe"
	StrategyPurgeErrors = "purgeErrors"
	StrategySupersede   = "supersedeReads"
	StrategyCompress    = "compress"
)

const (
	// MinEditTokens is the smallest saving worth an edit entry.
	MinEditTokens = 40
	// PurgeAfterAssistants is how many later assistant messages show the
	// model has moved on from a failed call.
	PurgeAfterAssistants = 2
	// purgeArgBytes is the size above which a failed call's string argument
	// is dropped.
	purgeArgBytes = 256
)

// Edit is one planned context edit.
type Edit struct {
	// Index is the item the edit changes.
	Index    int
	TargetID string
	// Replacement is the new content (a JSON string or content array), or
	// nil to omit the entry from context.
	Replacement json.RawMessage
	Strategy    string
	// Saved is the estimated number of context tokens the edit removes.
	Saved int
}

// dedupeTools are the read-only tools whose repeated identical calls are
// deduplicated. Tools with side effects (bash, edit, write, task, mcp) are
// left alone: an older result may be the only record of what happened.
var dedupeTools = map[string]bool{
	"read": true, "grep": true, "find": true, "ls": true,
	"obs_recall": true, "web_fetch": true, "web_search": true,
}

// mutationTools change a file named by their path argument.
var mutationTools = map[string]bool{"edit": true, "write": true}

type call struct {
	item int // index of the assistant item that made the call
	tc   ai.ToolCall
}

// Plan returns the automatic edits for items: dedupe, errored-input purge and
// superseded reads, as cfg enables them. Items that already carry an edit are
// never changed again. cwd resolves relative paths.
func Plan(cfg Config, items []Item, cwd string) []Edit {
	if !cfg.Automatic() {
		return nil
	}
	calls := map[string]call{}
	stubbed := map[int]bool{}
	var edits []Edit
	stub := func(index int, text, strategy string) {
		item := items[index]
		if item.EntryID == "" || item.Edited || stubbed[index] {
			return
		}
		replacement, _ := json.Marshal(text)
		saved := item.Tokens - compaction.EstimateTokens(agent.AgentMessage{ToolResult: &agent.ToolResultMessage{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: text}}}})
		if saved < MinEditTokens {
			return
		}
		stubbed[index] = true
		edits = append(edits, Edit{Index: index, TargetID: item.EntryID, Replacement: replacement, Strategy: strategy, Saved: saved})
	}

	// One forward pass records, for every tool result, its call and the
	// mutation state it observed.
	type observation struct {
		index   int
		name    string
		path    string
		sig     string
		epoch   int // mutations of path (read) or of any file (search) so far
		ranged  bool
		isError bool
	}
	var observations []observation
	pathVersion := map[string]int{}
	written := map[string][]int{} // path -> indexes of successful full writes
	mutations := 0
	assistantsAfter := make([]int, len(items))
	total := 0
	for i := len(items) - 1; i >= 0; i-- {
		assistantsAfter[i] = total
		if items[i].Message.Assistant != nil {
			total++
		}
	}
	for i, item := range items {
		if a := item.Message.Assistant; a != nil {
			for _, block := range a.Content {
				if tc, ok := block.(ai.ToolCall); ok {
					calls[tc.ID] = call{item: i, tc: tc}
				}
			}
			continue
		}
		result := item.Message.ToolResult
		if result == nil {
			continue
		}
		c, ok := calls[result.ToolCallID]
		if !ok {
			continue
		}
		name := c.tc.Name
		path := argPath(c.tc.Arguments, cwd)
		if mutationTools[name] && !result.IsError && path != "" {
			pathVersion[path]++
			mutations++
			if name == "write" {
				written[path] = append(written[path], i)
			}
			continue
		}
		if !dedupeTools[name] {
			continue
		}
		o := observation{index: i, name: name, path: path, sig: signature(name, c.tc.Arguments, path), isError: result.IsError}
		if name == "read" {
			o.epoch = pathVersion[path]
			_, hasOffset := c.tc.Arguments["offset"]
			_, hasLimit := c.tc.Arguments["limit"]
			o.ranged = hasOffset || hasLimit
		} else {
			o.epoch = mutations
		}
		observations = append(observations, o)
	}

	if cfg.Dedupe {
		// Keep the newest of each identical call within one mutation epoch.
		last := map[string]int{}
		for k, o := range observations {
			last[fmt.Sprintf("%s\x00%d", o.sig, o.epoch)] = k
		}
		for k, o := range observations {
			if last[fmt.Sprintf("%s\x00%d", o.sig, o.epoch)] != k {
				stub(o.index, "[pruned: this call was repeated later with the same arguments; see the newer result]", StrategyDedupe)
			}
		}
	}

	if cfg.SupersedeReads {
		for k, o := range observations {
			if o.name != "read" || o.path == "" || o.isError {
				continue
			}
			superseded := slices.ContainsFunc(written[o.path], func(w int) bool { return w > o.index })
			if !superseded {
				// An edit made this read stale; a later read at a newer
				// version that covers at least as much replaces it.
				for _, later := range observations[k+1:] {
					if later.name == "read" && later.path == o.path && later.epoch > o.epoch && !later.isError && (!later.ranged || later.sig == o.sig) {
						superseded = true
						break
					}
				}
			}
			if superseded {
				stub(o.index, fmt.Sprintf("[pruned: stale read of %s; the file changed later]", filepath.Base(o.path)), StrategySupersede)
			}
		}
	}

	if cfg.PurgeErrors {
		edits = append(edits, purgeErrors(items, calls, assistantsAfter)...)
	}
	slices.SortFunc(edits, func(a, b Edit) int { return a.Index - b.Index })
	return edits
}

// purgeErrors shrinks the large string arguments of failed calls once the
// model has moved on. The error text in the tool result is kept, and so is
// the call itself, so tool-call/tool-result pairing stays intact.
func purgeErrors(items []Item, calls map[string]call, assistantsAfter []int) []Edit {
	failed := map[int]map[string]bool{} // assistant item -> failed call ids
	for i, item := range items {
		result := item.Message.ToolResult
		if result == nil || !result.IsError || assistantsAfter[i] < PurgeAfterAssistants {
			continue
		}
		c, ok := calls[result.ToolCallID]
		if !ok {
			continue
		}
		if failed[c.item] == nil {
			failed[c.item] = map[string]bool{}
		}
		failed[c.item][result.ToolCallID] = true
	}
	var edits []Edit
	for index, ids := range failed {
		item := items[index]
		if item.EntryID == "" || item.Edited {
			continue
		}
		content := slices.Clone(item.Message.Assistant.Content)
		changed := false
		for b, block := range content {
			tc, ok := block.(ai.ToolCall)
			if !ok || !ids[tc.ID] {
				continue
			}
			if args, shrunk := shrinkArgs(tc.Arguments); shrunk {
				tc.Arguments = args
				content[b] = tc
				changed = true
			}
		}
		if !changed {
			continue
		}
		replacement, err := json.Marshal(content)
		if err != nil {
			continue
		}
		after := agent.AgentMessage{Assistant: &agent.AssistantMessage{Role: agent.RoleAssistant, Content: content}}
		saved := item.Tokens - compaction.EstimateTokens(after)
		if saved < MinEditTokens {
			continue
		}
		edits = append(edits, Edit{Index: index, TargetID: item.EntryID, Replacement: replacement, Strategy: StrategyPurgeErrors, Saved: saved})
	}
	return edits
}

// shrinkArgs replaces every large string value (at any depth) with a short
// note, keeping the argument object valid and its small fields (the path).
func shrinkArgs(args ai.JsonObject) (ai.JsonObject, bool) {
	shrunk := false
	var walk func(value any) any
	walk = func(value any) any {
		switch v := value.(type) {
		case string:
			if len(v) > purgeArgBytes {
				shrunk = true
				return fmt.Sprintf("[pruned: %d bytes of a failed call's input]", len(v))
			}
			return v
		case ai.JsonObject:
			return walk(map[string]any(v))
		case map[string]any:
			out := make(map[string]any, len(v))
			for key, inner := range v {
				out[key] = walk(inner)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, inner := range v {
				out[i] = walk(inner)
			}
			return out
		}
		return value
	}
	out := walk(map[string]any(args)).(map[string]any)
	return out, shrunk
}

// argPath returns the call's cleaned absolute path argument, or "".
func argPath(args ai.JsonObject, cwd string) string {
	for _, key := range []string{"path", "file_path"} {
		if p, ok := args[key].(string); ok && p != "" {
			if !filepath.IsAbs(p) && cwd != "" {
				p = filepath.Join(cwd, p)
			}
			return filepath.Clean(p)
		}
	}
	return ""
}

// signature identifies a call by tool and canonical arguments, with the path
// resolved so "a.go" and "./a.go" match.
func signature(name string, args ai.JsonObject, path string) string {
	canonical := make(map[string]any, len(args))
	for key, value := range args {
		if value != nil {
			canonical[key] = value
		}
	}
	if path != "" {
		delete(canonical, "file_path")
		canonical["path"] = path
	}
	data, _ := json.Marshal(canonical) // map keys marshal sorted
	return name + "\x00" + string(data)
}
