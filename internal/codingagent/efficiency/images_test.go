package efficiency

import (
	"fmt"
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
	reply := agent.AgentMessage{Assistant: &agent.AssistantMessage{Content: []ai.AssistantContentBlock{ai.TextContent{Text: "reading"}}}}
	conversation := []agent.AgentMessage{user}
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		conversation = append(conversation, reply, shot(id))
	}
	images := func(messages []agent.AgentMessage) (n int, placeholders []string) {
		for _, m := range messages {
			var blocks []any
			if m.Assistant != nil {
				continue
			}
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
	if _, report := ProjectImages(conversation[1:11], 3, 0, 0, name); report.Pruned != 0 {
		t.Fatalf("5 images, keep 3: pruned %d, want 0 (pruning waits for a full step)", report.Pruned)
	}
	// Eight images, keep 3: the oldest step of three goes, except the
	// user's own image.
	projected, report := ProjectImages(conversation, 3, 0, 0, name)
	if n, ph := images(projected); report.Pruned != 2 || n != 6 || len(ph) != 2 || !strings.Contains(ph[0], "/tmp/a.png") {
		t.Fatalf("8 images, keep 3: pruned %d, %d images left, placeholders %q", report.Pruned, n, ph)
	}
	// A large window's step lets them accumulate: no prefix change yet.
	if _, report := ProjectImages(conversation, 3, ImagePruneStepLarge, 0, name); report.Pruned != 0 {
		t.Fatalf("8 images, step %d: pruned %d, want 0", ImagePruneStepLarge, report.Pruned)
	}
	if n, _ := images(conversation); n != 8 {
		t.Fatalf("the conversation lost images: %d left of 8", n)
	}
	// A tight size limit prunes oldest first down to one image, and every
	// text block survives.
	projected, report = ProjectImages(conversation, 3, 0, 1500, name)
	if n, _ := images(projected); n != 1 || report.Bytes > 1500+8*64 {
		t.Fatalf("size limit: %d images left, %d bytes", n, report.Bytes)
	}
	if text := projected[0].User.Content[0]; text != (ai.TextContent{Text: "look"}) {
		t.Fatalf("size limit dropped text: %v", text)
	}

	// Ten images one turn read arrive whole, before the model has seen them
	// and while they are the newest batch after it has.
	batch := []agent.AgentMessage{reply, shot("old1"), reply, shot("old2"), reply, shot("old3"), reply}
	for i := range 10 {
		batch = append(batch, shot(fmt.Sprint("sq", i)))
	}
	if projected, report := ProjectImages(batch, 3, 0, 0, name); report.Pruned != 3 {
		t.Fatalf("unseen batch of 10: pruned %d, want only the 3 older images", report.Pruned)
	} else if n, _ := images(projected); n != 10 {
		t.Fatalf("unseen batch of 10: %d images left", n)
	}
	if _, report := ProjectImages(append(batch, reply), 3, 0, 0, name); report.Pruned != 3 {
		t.Fatalf("newest batch of 10 after a reply: pruned %d, want 3", report.Pruned)
	}
}
