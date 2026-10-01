package coding

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/imageprocessing"
)

// A models.json endpoint's "input" decides whether images reach it: a vision
// model gets a screenshot resized within the 32-pixel tile budget, a
// text-only model gets the placeholder instead of the bytes.
func TestModelsJSONInputDecidesImageDelivery(t *testing.T) {
	t.Setenv("WOPR_HOME", t.TempDir())
	var mu sync.Mutex
	bodies := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		data, _ := io.ReadAll(request.Body)
		mu.Lock()
		for _, id := range []string{"vision", "text"} {
			if strings.Contains(string(data), `"model":"`+id+`"`) {
				bodies[id] = string(data)
			}
		}
		mu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, `data: {"id":"c1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	agentDir := t.TempDir()
	models := fmt.Sprintf(`{"providers":{"local":{"baseUrl":%q,"api":"openai-completions","authHeader":false,"models":[{"id":"vision","input":["text","image"]},{"id":"text"}]}}}`, server.URL+"/v1")
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), []byte(models), 0o600); err != nil {
		t.Fatal(err)
	}
	services, err := NewServices(ServicesOptions{CWD: t.TempDir(), AgentDir: agentDir})
	if err != nil {
		t.Fatal(err)
	}

	screenshot := image.NewRGBA(image.Rect(0, 0, 2560, 1600))
	for i := range screenshot.Pix {
		screenshot.Pix[i] = byte(i * 7)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, screenshot); err != nil {
		t.Fatal(err)
	}
	data, mime, _, err := imageprocessing.PrepareCLIImageAttachment(encoded.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	prompt := ai.Context{Messages: []ai.Message{ai.UserMessage{Content: ai.UserContentBlocks{
		ai.TextContent{Text: "what is this?"},
		ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: mime},
	}}}}

	for _, id := range []string{"vision", "text"} {
		model, err := BuildModel("local/"+id, services)
		if err != nil {
			t.Fatal(err)
		}
		if result := services.ModelRuntime().Complete(context.Background(), model, prompt, ai.StreamOptions{}); result.StopReason != ai.StopReasonStop {
			t.Fatalf("%s: stop %q: %s", id, result.StopReason, result.ErrorMessage)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	vision, text := bodies["vision"], bodies["text"]
	if !strings.Contains(vision, `"image_url"`) || strings.Contains(vision, ai.ImageOmittedText) {
		t.Fatalf("vision request lacks the image: %.300s", vision)
	}
	if strings.Contains(text, `"image_url"`) || !strings.Contains(text, ai.ImageOmittedText) {
		t.Fatalf("text-only request is missing the placeholder: %.300s", text)
	}
	resized, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tiles := ((resized.Width + 31) / 32) * ((resized.Height + 31) / 32)
	if resized.Width > 2048 || resized.Height > 2048 || tiles > 2500 {
		t.Fatalf("screenshot sent at %dx%d (%d tiles), want within 2048px and 2500 tiles", resized.Width, resized.Height, tiles)
	}
}
