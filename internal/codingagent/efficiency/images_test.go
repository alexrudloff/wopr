package efficiency

import (
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
)

// Pruning only shapes the request: the conversation keeps every image, the
// newest are sent, the user's own images outlast screenshots, and the size
// limit never removes text.
func TestImagePruningKeepsTheSessionWhole(t *testing.T) {
	img := ai.ImageContent{Data: strings.Repeat("x", 1000), MimeType: "image/png"}
	user := agent.AgentMessage{User: &agent.UserMessage{Content: []ai.UserContentBlock{ai.TextContent{Text: "look"}, img}}}
	shot := func(id string) agent.AgentMessage {
		return agent.AgentMessage{ToolResult: &agent.ToolResultMessage{ToolCallID: id, ToolName: "read", Content: []ai.ToolResultMessageContent{img}}}
	}
	conversation := []agent.AgentMessage{user}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		conversation = append(conversation, shot(id))
	}
	images := func(messages []agent.AgentMessage) (n int, placeholders []string) {
		for _, m := range messages {
			var blocks []any
			if m.User != nil {
				for _, b := range m.User.Content {
					blocks = append(blocks, b)
				}
			} else {
				for _, b := range m.ToolResult.Content {
					blocks = append(blocks, b)
				}
			}
			for _, b := range blocks {
				switch b := b.(type) {
				case ai.ImageContent:
					n++
				case ai.TextContent:
					if strings.HasPrefix(b.Text, "[image removed") {
						placeholders = append(placeholders, b.Text)
					}
				}
			}
		}
		return n, placeholders
	}
	name := func(id string) string { return "/tmp/" + id + ".png" }

	// Five images, keep 3: under one step past it, nothing is pruned.
	if _, report := ProjectImages(conversation[2:7], 3, 0, name); report.Pruned != 0 {
		t.Fatalf("5 images, keep 3: pruned %d, want 0 (pruning waits for a full step)", report.Pruned)
	}
	// Eight images, keep 3: the oldest step of three goes, except the
	// user's own image.
	projected, report := ProjectImages(conversation, 3, 0, name)
	if n, ph := images(projected); report.Pruned != 2 || n != 6 || len(ph) != 2 || !strings.Contains(ph[0], "/tmp/a.png") {
		t.Fatalf("8 images, keep 3: pruned %d, %d images left, placeholders %q", report.Pruned, n, ph)
	}
	if n, _ := images(conversation); n != 8 {
		t.Fatalf("the conversation lost images: %d left of 8", n)
	}
	// A tight size limit prunes oldest first down to one image, and every
	// text block survives.
	projected, report = ProjectImages(conversation, 3, 1500, name)
	if n, _ := images(projected); n != 1 || report.Bytes > 1500+8*64 {
		t.Fatalf("size limit: %d images left, %d bytes", n, report.Bytes)
	}
	if text := projected[0].User.Content[0]; text != (ai.TextContent{Text: "look"}) {
		t.Fatalf("size limit dropped text: %v", text)
	}
}
