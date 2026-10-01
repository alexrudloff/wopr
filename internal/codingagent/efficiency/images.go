package efficiency

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Image pruning: a screenshot stays useful for a turn or two, but every
// image stays in the conversation, and base64 images make requests large
// enough for a provider to refuse them by size (HTTP 413) long before the
// context window fills. A request keeps only the newest images; older ones
// become a one-line placeholder. The session file keeps them all.

const (
	// imagePruneStep is how many images past the kept count accumulate
	// before the oldest are pruned together, so the cached prompt prefix
	// changes only every few images instead of on every one.
	imagePruneStep = 3
	// keepUserImages is how many of the user's own newest pasted images stay
	// whatever the kept count.
	keepUserImages = 3
)

// ImageProjection reports what ProjectImages pruned.
type ImageProjection struct {
	// Pruned counts images replaced by the kept-count rule; SizePruned the
	// ones the size limit replaced on top of it.
	Pruned, SizePruned int
	// Bytes estimates the request's message bytes after pruning.
	Bytes int
}

type imageLoc struct {
	msg, block int
	user       bool
	at         int64
	name       string
	size       int
}

// ProjectImages replaces all but the newest keep images with a placeholder,
// in steps of imagePruneStep, except the user's newest keepUserImages. keep
// 0 turns that rule off. When the estimated message bytes still exceed
// maxBytes (0: no limit), the oldest remaining images are replaced too,
// down to the newest one. Text is never dropped. Messages are copied, never
// mutated. name returns a tool result's file name by tool call ID, or "".
func ProjectImages(messages []agent.AgentMessage, keep, maxBytes int, name func(callID string) string) ([]agent.AgentMessage, ImageProjection) {
	var locs []imageLoc
	for i, m := range messages {
		switch {
		case m.User != nil:
			for j, block := range m.User.Content {
				if img, ok := block.(ai.ImageContent); ok {
					locs = append(locs, imageLoc{msg: i, block: j, user: true, at: m.User.Timestamp, name: "your image", size: len(img.Data)})
				}
			}
		case m.ToolResult != nil:
			for j, block := range m.ToolResult.Content {
				if img, ok := block.(ai.ImageContent); ok {
					label := m.ToolResult.ToolName
					if name != nil {
						if n := name(m.ToolResult.ToolCallID); n != "" {
							label = n
						}
					}
					locs = append(locs, imageLoc{msg: i, block: j, at: m.ToolResult.Timestamp, name: label, size: len(img.Data)})
				}
			}
		}
	}
	var out ImageProjection
	out.Bytes = messageBytes(messages)
	if len(locs) == 0 {
		return messages, out
	}
	protected := map[int]bool{}
	users := 0
	for k := len(locs) - 1; k >= 0 && users < keepUserImages; k-- {
		if locs[k].user {
			protected[k] = true
			users++
		}
	}
	pruned := make([]bool, len(locs))
	if keep > 0 && len(locs) > keep {
		frontier := (len(locs) - keep) / imagePruneStep * imagePruneStep
		for k := range frontier {
			if !protected[k] {
				pruned[k] = true
				out.Pruned++
				out.Bytes -= locs[k].size
			}
		}
	}
	if maxBytes > 0 {
		left := len(locs) - out.Pruned
		for k := 0; k < len(locs) && out.Bytes > maxBytes && left > 1; k++ {
			if pruned[k] {
				continue
			}
			pruned[k] = true
			out.SizePruned++
			out.Bytes -= locs[k].size
			left--
		}
	}
	if out.Pruned+out.SizePruned == 0 {
		return messages, out
	}
	projected := slices.Clone(messages)
	copied := map[int]bool{}
	for k, loc := range locs {
		if !pruned[k] {
			continue
		}
		placeholder := ai.TextContent{Text: imagePlaceholder(loc)}
		out.Bytes += len(placeholder.Text)
		m := projected[loc.msg]
		if !copied[loc.msg] {
			copied[loc.msg] = true
			if m.User != nil {
				user := *m.User
				user.Content = slices.Clone(user.Content)
				m.User = &user
			} else {
				result := *m.ToolResult
				result.Content = slices.Clone(result.Content)
				m.ToolResult = &result
			}
		}
		if m.User != nil {
			m.User.Content[loc.block] = placeholder
		} else {
			m.ToolResult.Content[loc.block] = placeholder
		}
		projected[loc.msg] = m
	}
	return projected, out
}

func imagePlaceholder(loc imageLoc) string {
	if loc.at > 0 {
		return fmt.Sprintf("[image removed from context: %s, %s]", loc.name, time.UnixMilli(loc.at).Format("15:04"))
	}
	return fmt.Sprintf("[image removed from context: %s]", loc.name)
}

// messageBytes estimates the serialized size of messages: images and text
// by length, tool call arguments by their JSON.
func messageBytes(messages []agent.AgentMessage) int {
	n := 0
	for _, m := range messages {
		switch {
		case m.User != nil:
			for _, block := range m.User.Content {
				n += blockBytes(block)
			}
		case m.Assistant != nil:
			for _, block := range m.Assistant.Content {
				switch b := block.(type) {
				case ai.TextContent:
					n += len(b.Text)
				case ai.ThinkingContent:
					n += len(b.Thinking)
				case ai.ToolCall:
					args, _ := json.Marshal(b.Arguments)
					n += len(args)
				}
			}
		case m.ToolResult != nil:
			for _, block := range m.ToolResult.Content {
				n += blockBytes(block)
			}
		}
	}
	return n
}

func blockBytes(block any) int {
	switch b := block.(type) {
	case ai.TextContent:
		return len(b.Text)
	case ai.ImageContent:
		return len(b.Data)
	}
	return 0
}
