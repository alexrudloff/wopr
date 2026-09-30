package codingagent

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent/sessionblob"
)

// Session images live in the blob store, not the JSONL: they must come back
// byte for byte, be stored once across sessions, degrade to a placeholder
// when their blob is gone, and old sessions with inline images still load.
func TestSessionImagesRoundTripThroughTheBlobStore(t *testing.T) {
	dir := t.TempDir()
	png := make([]byte, 50_000)
	_, _ = rand.Read(png)
	data := base64.StdEncoding.EncodeToString(png)
	withImage := func() agent.AgentMessage {
		return agent.AgentMessage{ToolResult: &agent.ToolResultMessage{
			Role: agent.RoleToolResult, ToolCallID: "c1", ToolName: "read",
			Content:   []ai.ToolResultMessageContent{ai.ImageContent{MimeType: "image/png", Data: data}},
			Timestamp: time.Now().UnixMilli(),
		}}
	}
	sm := NewSessionManagerWithDir("/tmp/blob-test", dir)
	var paths []string
	for _, id := range []string{"blob-a", "blob-b"} {
		sess, err := sm.Create(id, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range []agent.AgentMessage{mkUserMsg("look"), mkAssistantMsg("reading"), withImage()} {
			if _, err := sess.AppendMessage(msg); err != nil {
				t.Fatal(err)
			}
		}
		paths = append(paths, sess.Path())
	}

	file, _ := os.ReadFile(paths[0])
	if bytes.Contains(file, []byte(data[:200])) || !bytes.Contains(file, []byte(`"blob":"`)) {
		t.Fatal("the session file holds the image data instead of a blob reference")
	}
	blobs, _ := os.ReadDir(filepath.Join(dir, sessionblob.DirName))
	if len(blobs) != 1 {
		t.Fatalf("blobs = %d, want the shared image stored once", len(blobs))
	}
	loaded, err := NewSessionManagerWithDir("/tmp/blob-test", dir).Load(paths[1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(loaded.Entries()[len(loaded.Entries())-1].Raw(), []byte(data)) {
		t.Fatal("the reloaded image is not the original bytes")
	}

	_ = os.Remove(filepath.Join(dir, sessionblob.DirName, blobs[0].Name()))
	missing, err := NewSessionManagerWithDir("/tmp/blob-test", dir).Load(paths[0])
	if err != nil {
		t.Fatalf("a session with a missing blob must still load: %v", err)
	}
	if !strings.Contains(string(missing.Entries()[len(missing.Entries())-1].Raw()), "[image missing: ") {
		t.Fatal("a missing blob didn't become the placeholder")
	}

	legacy := filepath.Join(dir, "legacy.jsonl")
	_ = os.WriteFile(legacy, []byte(`{"type":"session","version":3,"id":"legacy","timestamp":"2026-09-01T00:00:00.000Z","cwd":"/tmp"}`+"\n"+
		`{"type":"message","id":"m1","timestamp":"2026-09-01T00:00:01.000Z","message":{"role":"toolResult","toolCallId":"c1","toolName":"read","content":[{"type":"image","data":"`+data+`","mimeType":"image/png"}],"timestamp":1}}`+"\n"), 0o644)
	old, err := NewSessionManagerWithDir("/tmp/blob-test", dir).Load(legacy)
	if err != nil || !bytes.Contains(old.Entries()[0].Raw(), []byte(data)) {
		t.Fatalf("a legacy inline session didn't load its image: %v", err)
	}
}
