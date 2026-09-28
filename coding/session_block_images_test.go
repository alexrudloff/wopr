package coding

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"reflect"
	"testing"

	"github.com/alexrudloff/wopr/ai"
	icodingagent "github.com/alexrudloff/wopr/internal/codingagent"
)

// With images.blockImages on, every image in a
// user or toolResult message becomes the text "Image reading is disabled.",
// consecutive placeholders collapse to one, and the setting is read per
// request so a mid-session change applies.
func TestSessionBlockImagesReplacesImagesInProviderRequest(t *testing.T) {
	svcs := newTestServices(t)
	provider := &transcriptCaptureProvider{}
	sess, err := NewSession(svcs, SessionOptions{Model: fakeModelWithProvider(provider)})
	if err != nil {
		t.Fatal(err)
	}
	defer func(s *Session) { _ = s.Close() }(sess)
	image := sessionImageFixture(t)
	content := ai.UserContentBlocks{ai.TextContent{Text: "look"}, image, image}

	if _, err := sess.SendContent(context.Background(), []ai.UserContentBlock(content)); err != nil {
		t.Fatal(err)
	}
	block := true
	if err := svcs.SettingsManager().UpdateGlobal(func(s *icodingagent.Settings) { s.Images = &icodingagent.ImageSettings{BlockImages: &block} }); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Send(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}

	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(provider.requests))
	}
	first := firstUserContent(t, provider.requests[0])
	if images := countImages(first); images != 2 {
		t.Fatalf("blockImages off: request images = %d, want 2 (%#v)", images, first)
	}
	blocked := firstUserContent(t, provider.requests[1])
	want := ai.UserContentBlocks{ai.TextContent{Text: "look"}, ai.TextContent{Text: "Image reading is disabled."}}
	if !reflect.DeepEqual(blocked, want) {
		t.Fatalf("blockImages on: user content = %#v, want %#v", blocked, want)
	}
}

func firstUserContent(t *testing.T, transcript ai.TranscriptContext) ai.UserContentBlocks {
	t.Helper()
	for _, message := range transcript.Messages() {
		if user, ok := message.(ai.UserMessage); ok {
			blocks, _ := user.Content.(ai.UserContentBlocks)
			return blocks
		}
	}
	t.Fatal("no user message in request")
	return nil
}

func countImages(blocks ai.UserContentBlocks) int {
	count := 0
	for _, block := range blocks {
		if _, ok := block.(ai.ImageContent); ok {
			count++
		}
	}
	return count
}

func sessionImageFixture(t *testing.T) ai.ImageContent {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 80, 40))); err != nil {
		t.Fatal(err)
	}
	return ai.ImageContent{Data: base64.StdEncoding.EncodeToString(encoded.Bytes()), MimeType: "image/png"}
}

type transcriptCaptureProvider struct {
	fakeProvider
	requests []ai.TranscriptContext
}

func (p *transcriptCaptureProvider) Stream(_ context.Context, transcript ai.TranscriptContext, _ ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	p.requests = append(p.requests, transcript)
	message := sessionTestMessage("fake", "done", ai.StopReasonStop, "")
	return newSessionTestStream(ai.StartEvent{Partial: message}, ai.DoneEvent{Reason: ai.StopReasonStop, Message: message}), nil
}
