package export

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestExportHTML_DoesNotInlineRawScriptTag(t *testing.T) {
	sd := SessionData{
		Header: mustRaw(map[string]any{"type": "session", "id": "xss", "cwd": "/tmp"}),
		Entries: []json.RawMessage{mustRaw(map[string]any{
			"type": "message",
			"id":   "u1",
			"message": map[string]any{
				"role":    "user",
				"content": []map[string]any{{"type": "text", "text": `<script>alert("xss")</script>`}},
			},
		})},
	}
	html := ToHTML(sd)
	if strings.Contains(html, `<script>alert("xss")</script>`) {
		t.Fatal("raw script tag leaked into HTML source")
	}
}
