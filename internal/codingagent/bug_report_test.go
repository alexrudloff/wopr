package codingagent

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alexrudloff/wopr/ai"
)

// Bug report URLs lose credentials and secret query parameters.
func TestRedactBugReportURL(t *testing.T) {
	for input, want := range map[string]string{
		"https://user:pass@proxy.example.com:8080/":   "https://proxy.example.com:8080/",
		"git:https://pat@github.com/org/repo":         "git:https://github.com/org/repo",
		"https://api.example/v1?api-key=abc&model=x":  "https://api.example/v1?api-key=%3Credacted%3E&model=x",
		"https://example.com/path?q=1":                "https://example.com/path?q=1",
		"sk-123":                                      "sk-123",
		"https://user@host.example":                   "https://host.example/",
		"https://x.example/?token=a&token=b&keep=c d": "https://x.example/?token=%3Credacted%3E&keep=c+d",
	} {
		if got := RedactBugReportURL(input); got != want {
			t.Errorf("RedactBugReportURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBugReportMetadataRedactsSettingsAndModel(t *testing.T) {
	model := &ai.Model{ID: "m", DisplayName: "M", ProviderMeta: ai.ProviderMetadata{ProviderID: "custom", API: "openai-completions", BaseURL: "https://u:p@llm.example/v1?api_key=k", Headers: map[string]string{"X-B": "1", "Authorization": "secret"}}}
	metadata, err := CollectBugReportMetadata(BugReportInputs{Model: model, GlobalSettings: Settings{DefaultProvider: "custom"}}, BugReportOptions{}, false, fixedBugReportTime())
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Model.BaseURL != "https://llm.example/v1?api_key=%3Credacted%3E" || !reflect.DeepEqual(metadata.Model.HeaderNames, []string{"Authorization", "X-B"}) {
		t.Fatalf("model = %+v", metadata.Model)
	}
	encoded, _ := marshalJSONIndent(metadata)
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), `\u003c`) {
		t.Fatalf("report leaks a header value or escapes HTML:\n%s", encoded)
	}
	if metadata.CreatedAt != "2026-01-02T03:04:05.006Z" || metadata.SchemaVersion != 1 {
		t.Fatalf("createdAt = %s", metadata.CreatedAt)
	}
}

func fixedBugReportTime() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.UTC) }
