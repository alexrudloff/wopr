package pruning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/compaction"
)

// CompressToolName is the model-invoked span compressor.
const CompressToolName = "compress"

// compressedStub replaces the results of calls whose assistant message hosts
// a compress summary.
const compressedStub = "[compressed]"

// CompressHost gives the compress tool the live context.
type CompressHost interface {
	// CompressItems returns the projected context the model sees.
	CompressItems() []Item
	// ArchiveSpan stores the original span for obs_recall and returns its
	// id, or "" when there is no archive.
	ArchiveSpan(key, text string) string
	// QueueCompress records edits to apply before the next request. It
	// refuses a span that overlaps one already queued.
	QueueCompress(edits []Edit) error
}

// CompressTool lets the model replace a finished span with its summary.
type CompressTool struct {
	Host CompressHost
}

func (t *CompressTool) Name() string  { return CompressToolName }
func (t *CompressTool) Label() string { return "Compress" }

// Schema is deliberately tiny: the tool is declared on every request once
// offered, so each byte is paid again and again. It lists no "required"
// array; Execute rejects a call that omits a field.
func (t *CompressTool) Schema() ai.ToolSchema {
	return ai.ToolSchema{
		Name:        CompressToolName,
		Description: "Free context: swap finished messages #from..#to for your summary.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from":    map[string]any{"type": "integer"},
				"to":      map[string]any{"type": "integer"},
				"summary": map[string]any{"type": "string"},
			},
		},
	}
}

func (t *CompressTool) ExecutionMode() agent.ToolExecutionMode { return agent.ToolModeSequential }

// markerNumber accepts 12, "12" and "#12".
type markerNumber int

func (n *markerNumber) UnmarshalJSON(data []byte) error {
	var number int
	if json.Unmarshal(data, &number) == nil {
		*n = markerNumber(number)
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	number, err := strconv.Atoi(strings.TrimPrefix(strings.Trim(strings.TrimSpace(text), "[]"), "#"))
	if err != nil {
		return fmt.Errorf("not a message number: %q", text)
	}
	*n = markerNumber(number)
	return nil
}

func (t *CompressTool) Execute(_ context.Context, toolCallID string, params json.RawMessage, _ agent.ToolUpdateCallback) (agent.AgentToolResult, error) {
	var in struct {
		From    markerNumber `json:"from"`
		To      markerNumber `json:"to"`
		Summary string       `json:"summary"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return agent.AgentToolResult{}, err
	}
	plan, err := PlanCompress(t.Host.CompressItems(), int(in.From), int(in.To), in.Summary, toolCallID, t.Host.ArchiveSpan)
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	if err := t.Host.QueueCompress(plan.Edits); err != nil {
		return agent.AgentToolResult{}, err
	}
	text := fmt.Sprintf("Compressed #%d..#%d: about %d tokens removed from context from the next request.", in.From, in.To, plan.Saved)
	if plan.ArchiveID != "" {
		text += " obs_recall id " + plan.ArchiveID + " holds the original."
	}
	return agent.AgentToolResult{
		Content: text,
		Details: map[string]any{"from": int(in.From), "to": int(in.To), "savedTokens": plan.Saved, "archive": plan.ArchiveID},
		Preview: fmt.Sprintf("Compressed #%d..#%d (-%d tokens)", in.From, in.To, plan.Saved),
	}, nil
}

// CompressPlan is the edits for one compress call.
type CompressPlan struct {
	Edits     []Edit
	Saved     int
	ArchiveID string
}

// PlanCompress resolves a span by marker numbers and returns the edits that
// replace it with summary. The span grows to whole tool-call batches, so a
// call is never separated from its result: a span that starts at a tool
// result starts at the assistant message that made the call, and a span that
// ends at a tool result ends after the batch's last result. The summary goes
// into the span's first editable message. When that is an assistant message,
// its tool calls stay (with short arguments) and their results become stubs,
// which keeps both message alternation and call/result pairing valid for
// every provider.
func PlanCompress(items []Item, from, to int, summary, toolCallID string, archive func(key, text string) string) (CompressPlan, error) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return CompressPlan{}, errors.New("summary is empty")
	}
	if from > to {
		from, to = to, from
	}
	start, end := -1, -1
	current := len(items)
	for i, item := range items {
		if item.Ordinal != 0 && item.Ordinal == from {
			start = i
		}
		if item.Ordinal != 0 && item.Ordinal == to {
			end = i
		}
		if toolCallID != "" && hasCall(item.Message, toolCallID) {
			current = i
		}
	}
	if start < 0 || end < 0 {
		return CompressPlan{}, fmt.Errorf("unknown message number; use the [#N] numbers shown on messages still in context")
	}
	if result := items[start].Message.ToolResult; result != nil {
		for i := start - 1; i >= 0; i-- {
			if hasCall(items[i].Message, result.ToolCallID) {
				start = i
				break
			}
		}
	}
	if items[end].Message.ToolResult != nil {
		for end+1 < len(items) && items[end+1].Message.ToolResult != nil {
			end++
		}
	}
	if end >= current {
		return CompressPlan{}, errors.New("the span must end before this compress call")
	}
	host := -1
	for i := start; i <= end; i++ {
		if items[i].EntryID != "" {
			host = i
			break
		}
	}
	if host < 0 {
		return CompressPlan{}, errors.New("nothing in that span can be compressed")
	}

	before := 0
	for i := start; i <= end; i++ {
		if items[i].EntryID != "" {
			before += items[i].Tokens
		}
	}
	archiveID := ""
	if archive != nil {
		archiveID = archive(fmt.Sprintf("%s-%d-%d", items[host].EntryID, from, to), renderSpan(items[start:end+1]))
	}
	header := fmt.Sprintf("[compressed #%d..#%d", from, to)
	if archiveID != "" {
		header += "; original: obs_recall id " + archiveID
	}
	text := header + "]\n" + summary

	var edits []Edit
	after := 0
	add := func(index int, message agent.AgentMessage, replacement json.RawMessage) {
		if replacement != nil {
			after += compaction.EstimateTokens(message)
		}
		edits = append(edits, Edit{Index: index, TargetID: items[index].EntryID, Replacement: replacement, Strategy: StrategyCompress})
	}
	stubbedResults := map[string]bool{}
	if a := items[host].Message.Assistant; a != nil {
		content := []ai.AssistantContentBlock{ai.TextContent{Text: text}}
		for _, block := range a.Content {
			if tc, ok := block.(ai.ToolCall); ok {
				tc.Arguments = briefArgs(tc.Arguments)
				content = append(content, tc)
				stubbedResults[tc.ID] = true
			}
		}
		raw, err := json.Marshal(content)
		if err != nil {
			return CompressPlan{}, err
		}
		add(host, agent.AgentMessage{Assistant: &agent.AssistantMessage{Role: agent.RoleAssistant, Content: content}}, raw)
	} else {
		raw, _ := json.Marshal([]ai.TextContent{{Text: text}})
		add(host, agent.AgentMessage{User: &agent.UserMessage{Role: agent.RoleUser, Content: []ai.UserContentBlock{ai.TextContent{Text: text}}}}, raw)
	}
	stub, _ := json.Marshal(compressedStub)
	for i := host + 1; i <= end; i++ {
		item := items[i]
		if item.EntryID == "" {
			continue
		}
		if result := item.Message.ToolResult; result != nil && stubbedResults[result.ToolCallID] {
			add(i, agent.AgentMessage{ToolResult: &agent.ToolResultMessage{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: compressedStub}}}}, stub)
			continue
		}
		add(i, agent.AgentMessage{}, nil)
	}
	saved := before - after
	if saved <= 0 {
		return CompressPlan{}, fmt.Errorf("the summary (%d tokens) is not smaller than the span (%d tokens)", after, before)
	}
	if len(edits) > 0 {
		edits[0].Saved = saved
	}
	return CompressPlan{Edits: edits, Saved: saved, ArchiveID: archiveID}, nil
}

func hasCall(message agent.AgentMessage, id string) bool {
	if message.Assistant == nil {
		return false
	}
	for _, block := range message.Assistant.Content {
		if tc, ok := block.(ai.ToolCall); ok && tc.ID == id {
			return true
		}
	}
	return false
}

// briefArgs keeps a compressed call's short scalar arguments (a path, a
// pattern) and drops the rest.
func briefArgs(args ai.JsonObject) ai.JsonObject {
	out := ai.JsonObject{}
	for key, value := range args {
		switch v := value.(type) {
		case string:
			if len(v) <= 120 {
				out[key] = v
			}
		case float64, bool, int:
			out[key] = v
		}
	}
	return out
}

// renderSpan writes a span as plain text for the archive.
func renderSpan(items []Item) string {
	var b strings.Builder
	for _, item := range items {
		m := item.Message
		switch {
		case m.User != nil:
			b.WriteString("## user\n")
			for _, block := range m.User.Content {
				if text, ok := block.(ai.TextContent); ok {
					b.WriteString(text.Text + "\n")
				}
			}
		case m.Assistant != nil:
			b.WriteString("## assistant\n")
			for _, block := range m.Assistant.Content {
				switch block := block.(type) {
				case ai.TextContent:
					b.WriteString(block.Text + "\n")
				case ai.ToolCall:
					args, _ := json.Marshal(block.Arguments)
					fmt.Fprintf(&b, "call %s %s\n", block.Name, args)
				}
			}
		case m.ToolResult != nil:
			fmt.Fprintf(&b, "## %s result", m.ToolResult.ToolName)
			if m.ToolResult.IsError {
				b.WriteString(" (error)")
			}
			b.WriteString("\n" + m.ToolResult.Text() + "\n")
		case m.Custom != nil:
			role, _ := m.Custom["role"].(string)
			data, _ := json.Marshal(m.Custom["content"])
			fmt.Fprintf(&b, "## %s\n%s\n", role, data)
		}
	}
	return b.String()
}
