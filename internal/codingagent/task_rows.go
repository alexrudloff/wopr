package codingagent

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent/subagent"
	"github.com/alexrudloff/wopr/tui"
)

// Task rows: a task call renders as "│ Explore — description" with a
// "↳ current tool" line while it runs and "↳ n toolcalls · duration · model"
// when done.

// taskDetails decodes a task result's details: the live struct, or the JSON
// form a resumed transcript carries.
func taskDetails(v any) (subagent.Details, bool) {
	switch d := v.(type) {
	case subagent.Details:
		return d, true
	case *subagent.Details:
		return *d, d != nil
	case nil:
		return subagent.Details{}, false
	}
	if d, ok := decodeAs[subagent.Details](v); ok && d.ID != "" {
		return d, true
	}
	return subagent.Details{}, false
}

// decodeAs re-decodes a JSON-shaped value, such as the details of a resumed
// or extension-supplied tool result, as a T. ok is false when v does not
// decode; the result may then hold the fields decoded before the failure.
func decodeAs[T any](v any) (out T, ok bool) {
	data, err := json.Marshal(v)
	if err != nil {
		return out, false
	}
	return out, json.Unmarshal(data, &out) == nil
}

// taskSubline is the row's "↳" line.
func taskSubline(d subagent.Details) string {
	if d.Background {
		return "background · " + d.Model
	}
	if d.Running {
		parts := []string{}
		if d.Current != "" {
			parts = append(parts, d.Current)
		}
		if d.ToolCalls > 0 {
			parts = append(parts, fmt.Sprintf("%d toolcalls", d.ToolCalls))
		}
		return strings.Join(append(parts, d.Model), " · ")
	}
	parts := []string{
		fmt.Sprintf("%d toolcalls", d.ToolCalls),
		formatDuration(time.Duration(d.DurationMs) * time.Millisecond),
		d.Model,
	}
	if d.Quotes > 0 {
		parts = append(parts, fmt.Sprintf("verified %d/%d", d.Verified, d.Quotes))
	}
	if d.Escalated != "" {
		from, _, _ := strings.Cut(d.Escalated, " ")
		parts = append(parts, "↑ from "+from)
	}
	if d.State == subagent.StatusFailed {
		parts = append(parts, "not accepted")
	}
	return strings.Join(parts, " · ")
}

// applyTaskRow updates a task card from its details.
func applyTaskRow(comp *tui.ToolExecutionComponent, details any) {
	d, ok := taskDetails(details)
	if !ok {
		return
	}
	comp.Subline = taskSubline(d)
	comp.SubWarn = !d.Running && (d.Escalated != "" || d.State == subagent.StatusFailed)
	comp.Invalidate()
}
