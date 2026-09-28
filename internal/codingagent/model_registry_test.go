package codingagent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexrudloff/wopr/ai"
)

func TestModelRegistryNullableHeadersDeleteProviderDefaults(t *testing.T) {
	dir := t.TempDir()
	config := `{"providers":{"custom":{"baseUrl":"https://example.test","headers":{"Authorization":"Bearer old","X-Trace":"base"},"models":[{"id":"model","headers":{"authorization":null,"x-trace":"override"}}]}}}`
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := NewModelRegistry(dir)
	entry, ok := registry.Resolve("custom", "model")
	if !ok {
		t.Fatal("custom/model was not resolved")
	}
	if len(entry.Headers) != 1 || entry.Headers["x-trace"] != "override" {
		t.Fatalf("resolved headers = %v, want only x-trace=override", entry.Headers)
	}
}

func TestModelRegistry_CustomModelBaseURLOverride(t *testing.T) {
	dir := t.TempDir()
	modelsPath := filepath.Join(dir, "models.json")
	data := `{"providers":{"custom":{"baseUrl":"https://provider.example/v1","models":[{"id":"m1","name":"M1","baseUrl":"https://model.example/v1","reasoning":true,"thinkingLevelMap":{"off":null,"high":"HIGH"}}]}}}`
	if err := os.WriteFile(modelsPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := NewModelRegistry(dir)
	entry, ok := r.Resolve("custom", "m1")
	if !ok {
		t.Fatal("Resolve returned ok=false")
	}
	if entry.BaseURL != "https://model.example/v1" {
		t.Fatalf("BaseURL = %q, want model override", entry.BaseURL)
	}
	if _, ok := entry.ThinkingLevelMap[ai.ThinkingOff]; !ok || entry.ThinkingLevelMap[ai.ThinkingOff] != nil {
		t.Fatalf("off mapping = %v, want explicit nil", entry.ThinkingLevelMap[ai.ThinkingOff])
	}
	if entry.ThinkingLevelMap[ai.ThinkingHigh] == nil || *entry.ThinkingLevelMap[ai.ThinkingHigh] != "HIGH" {
		t.Fatalf("high mapping = %v, want HIGH", entry.ThinkingLevelMap[ai.ThinkingHigh])
	}
}

// TestModelRegistry_GetAvailable_IncludesModelsJSONProviders proves that a
// provider defined purely in models.json (no extension RegisterProvider call)
// surfaces through GetAvailable() the same as a dynamically registered one.
// GetAvailable feeds --list-models, so a models.json-only provider was previously
// resolvable via --model provider/id (Resolve checks r.config.Providers) but
// invisible in every listing surface. Availability filters one list merging
// built-ins, models.json custom overlays, and runtime extension overlays --
// not just extension-registered providers.
func TestModelRegistry_GetAvailable_IncludesModelsJSONProviders(t *testing.T) {
	dir := t.TempDir()
	modelsPath := filepath.Join(dir, "models.json")
	data := `{"providers":{
		"glm-xd670":{"baseUrl":"http://xd670.example/v1","api":"openai-completions","apiKey":"not-needed","models":[{"id":"glm-5.2-fp8","name":"GLM-5.2-FP8"}]},
		"no-auth-prov":{"baseUrl":"https://noauth.example/v1","models":[{"id":"m1","name":"M1"}]}
	}}`
	if err := os.WriteFile(modelsPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r := NewModelRegistry(dir)
	available := r.GetAvailable()

	var found *ModelEntry
	for i := range available {
		if available[i].ProviderID == "glm-xd670" && available[i].ModelID == "glm-5.2-fp8" {
			found = &available[i]
		}
		if available[i].ProviderID == "no-auth-prov" {
			t.Errorf("no-auth-prov has no apiKey/env configured and should not be available, got %+v", available[i])
		}
	}
	if found == nil {
		t.Fatalf("expected glm-xd670/glm-5.2-fp8 in GetAvailable(), got %+v", available)
	}
	if found.BaseURL != "http://xd670.example/v1" {
		t.Errorf("BaseURL = %q, want http://xd670.example/v1", found.BaseURL)
	}
}

// TestModelRegistry_ResolveAPIKeyUsesProviderEnv proves the production wiring:
// a models.json provider whose apiKey is a "$VAR" reference resolves against
// the provider-scoped env stored in auth.json (precedence over process env).
func TestModelRegistry_ResolveAPIKeyUsesProviderEnv(t *testing.T) {
	t.Setenv("MYPROV_KEY", "from-process")
	dir := t.TempDir()
	modelsPath := filepath.Join(dir, "models.json")
	_ = os.WriteFile(modelsPath, []byte(`{"providers":{"myprov":{"baseUrl":"https://a.test","apiKey":"$MYPROV_KEY","models":[{"id":"m1","name":"M1"}]}}}`), 0o644)

	authPath := filepath.Join(dir, "auth.json")
	_ = os.WriteFile(authPath, []byte(`{"myprov":{"type":"api_key","key":"unused","env":{"MYPROV_KEY":"from-scope"}}}`), 0o644)
	auth, err := ai.NewAuthStorage(authPath)
	if err != nil {
		t.Fatal(err)
	}

	r := NewModelRegistry(dir)
	r.SetAuthStorage(auth)

	entry, ok := r.Resolve("myprov", "m1")
	if !ok {
		t.Fatal("Resolve returned ok=false")
	}
	if entry.APIKey != "from-scope" {
		t.Errorf("APIKey should resolve against provider-scoped env: got %q want %q", entry.APIKey, "from-scope")
	}

	// Without authStorage, the same provider falls back to the process env.
	r2 := NewModelRegistry(dir)
	entry2, ok := r2.Resolve("myprov", "m1")
	if !ok || entry2.APIKey != "from-process" {
		t.Errorf("without provider env, APIKey should use process env: ok=%v got %q", ok, entry2.APIKey)
	}
}
