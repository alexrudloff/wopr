package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

// A custom models.json provider speaks the api it declares when --provider
// and --model select it at startup, not always Chat Completions.
func TestResolveModelCustomProviderSpeaksDeclaredAPI(t *testing.T) {
	for api, wantPath := range map[string]string{
		"openai-completions": "/v1/chat/completions",
		"openai-responses":   "/v1/responses",
		"anthropic-messages": "/v1/messages",
	} {
		t.Run(api, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				http.Error(w, "fixture", http.StatusTeapot)
			}))
			defer server.Close()
			dir := t.TempDir()
			baseURL := server.URL + "/v1"
			if api == "anthropic-messages" {
				baseURL = server.URL
			}
			models := fmt.Sprintf(`{"providers":{"fixture":{"baseUrl":%q,"apiKey":"fixture-key","api":%q,"models":[{"id":"fixture-1"}]}}}`, baseURL, api)
			if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(models), 0o600); err != nil {
				t.Fatal(err)
			}
			services, err := coding.NewServices(coding.ServicesOptions{CWD: dir, AgentDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			selected, err := selectStartupModel(startupModelOptions{CLIProvider: "fixture", CLIModel: "fixture-1"}, codingagent.Settings{}, services)
			if err != nil {
				t.Fatal(err)
			}
			model := selected.Model
			stream, err := model.Provider.Stream(t.Context(), ai.NormalizeContext(ai.Context{Messages: []ai.Message{ai.UserMessage{Content: ai.UserText("hello")}}}), ai.StreamOptions{})
			if err == nil {
				stream.Result()
			}
			mu.Lock()
			defer mu.Unlock()
			if len(paths) == 0 || !strings.HasSuffix(paths[0], wantPath) {
				t.Fatalf("request paths = %v, want %s", paths, wantPath)
			}
		})
	}
}
