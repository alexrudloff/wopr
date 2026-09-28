package codingagent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/alexrudloff/wopr/internal/codingagent/router"
)

// Setup edits the user's own config files: everything already in them must
// survive, comments included, while the new models are added.
func TestSetupMergeKeepsExistingConfig(t *testing.T) {
	models := `{
  // Local endpoints.
  "providers": {
    "local": {
      "baseUrl": "http://localhost:8000/v1",
      "api": "openai-completions",
      "authHeader": false,
      "models": [
        { "id": "deepseek-v4-flash", "contextWindow": 65536 } // the 2-bit build
      ]
    },
    "old": { "baseUrl": "http://10.0.0.9:8000/v1", "models": [ { "id": "gone" } ] },
  }
}
`
	yes := true
	measured := &modelMeasure{Effort: true, Accepted: []string{"none", "low", "medium", "xhigh"}, LevelMap: thinkingLevelMapFor([]string{"none", "low", "medium", "xhigh"}), Tools: &yes}
	lan := &setupEndpoint{ID: "lan", Name: "GPU box", BaseURL: "http://192.168.1.50:8000/v1"}
	added := []*setupModel{
		{Kind: setupViaEndpoint, Provider: "local", Model: "deepseek-v4-pro", Endpoint: &setupEndpoint{ID: "local", Existing: true}, Context: 65536, Tier: router.CostFreeLocal, Capability: 0.6, Measure: measured},
		{Kind: setupViaEndpoint, Provider: "local", Model: "deepseek-v4-flash", Endpoint: &setupEndpoint{ID: "local", Existing: true}, Tier: router.CostFreeLocal},
		{Kind: setupViaEndpoint, Provider: "lan", Model: "open-model", Endpoint: lan, Context: 131072, Tier: router.CostFreeRemote, Capability: 0.45, Uncensored: true, Measure: measured},
		{Kind: setupViaAPIKey, Provider: "openrouter", Model: "z-ai/glm-5", Tier: router.CostPaid, Capability: 0.8},
	}
	// Configured before setup: one edited, one removed.
	flash := &setupModel{Existing: true, Defined: true, Edited: true, Provider: "local", Model: "deepseek-v4-flash", Name: "Flash", Context: 32768, Tier: router.CostFreeLocal, Capability: 0.7,
		Ref: &router.ModelRef{Provider: "local", Model: "deepseek-v4-flash", Capability: 0.6, Effort: "low"}, origName: "deepseek-v4-flash", origContext: 65536}
	gone := &setupModel{Existing: true, Defined: true, Removed: true, Provider: "old", Model: "gone", Tier: router.CostFreeRemote}
	out, err := mergeModelsJSON(models, added, flash, gone)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []string{"// Local endpoints.", "// the 2-bit build", `"baseUrl": "http://localhost:8000/v1"`} {
		if !strings.Contains(out, keep) {
			t.Errorf("models.json lost %q:\n%s", keep, out)
		}
	}
	var cfg modelsConfig
	if err := json.Unmarshal([]byte(stripJSONComments(out)), &cfg); err != nil {
		t.Fatalf("merged models.json does not parse: %v\n%s", err, out)
	}
	local := cfg.Providers["local"]
	if ids := []string{local.Models[0].ID, local.Models[len(local.Models)-1].ID}; len(local.Models) != 2 || ids[0] != "deepseek-v4-flash" || ids[1] != "deepseek-v4-pro" {
		t.Errorf("local models = %+v, want the existing one then deepseek-v4-pro", local.Models)
	}
	if got := cfg.Providers["lan"]; got.BaseURL != lan.BaseURL || len(got.Models) != 1 || *got.Models[0].ThinkingLevelMap["high"] != "xhigh" {
		t.Errorf("lan provider = %+v", got)
	}
	if _, ok := cfg.Providers["openrouter"]; ok {
		t.Error("an API-key model was written to models.json")
	}
	if _, ok := cfg.Providers["old"]; ok {
		t.Error("the removed model's provider is still in models.json")
	}
	if got := local.Models[0]; *got.ContextWindow != 32768 || got.Name != "Flash" {
		t.Errorf("edited model = %q, %d; want Flash, 32768", got.Name, *got.ContextWindow)
	}

	routerJSON := `{
  "enabled": false,
  "mode": "speed",
  "slack": 0.1,
  "jev": { "apiKeyProvider": "none", "retries": 3 },
  "tiers": [
    { "name": "local", "cost": "free-local", "models": [ { "provider": "local", "model": "deepseek-v4-flash", "capability": 0.6, "effort": "low" } ] },
    { "name": "old", "cost": "free-remote", "models": [ { "provider": "old", "model": "gone" } ] }
  ],
  "kinds": { "plan": { "minCapability": 0.9 } }
}
`
	jev := router.JevConfig{Endpoint: "http://jev.test/v1/decisions", Model: "jev-test", APIKeyProvider: "none"}
	out, err = mergeRouterJSON(routerJSON, routerPlan{Models: added, Jev: &jev, Existing: []*setupModel{flash, gone}, Enabled: new(true)})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		router.Config
		Kinds map[string]router.KindPolicy `json:"kinds"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("merged router.json does not parse: %v\n%s", err, out)
	}
	if !got.Enabled || strings.Contains(out, `"mode"`) || got.Slack != 0.1 || got.Kinds["plan"].MinCapability != 0.9 {
		t.Errorf("settings lost or not applied: %+v", got.Config)
	}
	if got.Jev.Endpoint != jev.Endpoint || got.Jev.Retries == nil || *got.Jev.Retries != 3 {
		t.Errorf("jev = %+v, want the new endpoint with retries kept", got.Jev)
	}
	specs := map[string]string{}
	for _, tier := range got.Tiers {
		for _, ref := range tier.Models {
			specs[ref.Spec()] = tier.Name + "/" + tier.Cost
		}
	}
	want := map[string]string{
		"local/deepseek-v4-flash": "local/free-local",
		"local/deepseek-v4-pro":   "local/free-local",
		"lan/open-model":          "lan/free-remote",
		"openrouter/z-ai/glm-5":   "paid/paid",
	}
	for spec, tier := range want {
		if specs[spec] != tier {
			t.Errorf("%s in %q, want %q", spec, specs[spec], tier)
		}
	}
	if len(got.Tiers[0].Models) != 2 {
		t.Errorf("the existing local tier has %d models, want 2", len(got.Tiers[0].Models))
	}
	if ref := got.Tiers[0].Models[0]; ref.Capability != 0.7 || ref.Effort != "low" {
		t.Errorf("edited entry = %+v, want capability 0.7 with its effort kept", ref)
	}
	if _, ok := specs["old/gone"]; ok {
		t.Error("the removed model is still routed")
	}
}

func TestInferTier(t *testing.T) {
	for url, want := range map[string]string{
		"http://localhost:8000/v1":       router.CostFreeLocal,
		"http://127.0.0.1:11434/v1":      router.CostFreeLocal,
		"http://192.168.1.20:8000/v1":    router.CostFreeRemote,
		"http://100.101.102.103:8000/v1": router.CostFreeRemote,
		"http://spark.local:8000/v1":     router.CostFreeRemote,
		"https://api.example.com/v1":     router.CostPaid,
	} {
		if got := inferTier(setupViaEndpoint, url, ""); got != want {
			t.Errorf("inferTier(%s) = %s, want %s", url, got, want)
		}
	}
}
