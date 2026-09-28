package llama

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/alexrudloff/wopr/ai"
)

func modelIDs(models []Model) []string {
	ids := []string{}
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func publishInto(cached **ai.ModelsStoreEntry) func(ModelsPublication) (bool, error) {
	return func(publication ModelsPublication) (bool, error) {
		if publication.Persist != nil {
			clone := *publication.Persist
			*cached = &clone
		}
		if publication.Update != nil {
			publication.Update()
		}
		return true, nil
	}
}

func apiKeyCredential(key, url string) *ai.Credential {
	return &ai.Credential{Type: ai.CredentialAPIKey, Key: key, Env: map[string]string{"LLAMA_BASE_URL": url}}
}

func TestSetCatalogExposesLoadedAndSleepingModelsWithRouterMetadata(t *testing.T) {
	controller := CreateLlamaProvider()
	nCtx, nCtxTrain := 65536.0, 131072.0
	controller.SetCatalog([]LlamaModelInfo{
		{
			ID:           "loaded",
			Status:       LlamaModelInfoStatus{Value: LlamaModelStatusLoaded, Args: []string{"llama-server", "--n-gpu-layers", "999"}},
			Architecture: &LlamaModelArchitecture{InputModalities: []string{"text", "image"}},
			Meta:         &LlamaModelMeta{NCtx: &nCtx, NCtxTrain: &nCtxTrain},
		},
		{ID: "sleeping", Status: LlamaModelInfoStatus{Value: LlamaModelStatusSleeping}},
		{ID: "unloaded", Status: LlamaModelInfoStatus{Value: LlamaModelStatusUnloaded}},
		{ID: "loading", Status: LlamaModelInfoStatus{Value: LlamaModelStatusLoading}},
	}, "http://localhost:8080", false)

	models := controller.GetModels()
	if got := modelIDs(models); !reflect.DeepEqual(got, []string{"loaded", "sleeping"}) {
		t.Fatalf("models = %v", got)
	}
	loaded := models[0]
	if loaded.BaseURL != "http://localhost:8080/v1" || loaded.ContextWindow != 65536 || loaded.MaxTokens != 65536 ||
		!reflect.DeepEqual(loaded.Input, []string{"text", "image"}) {
		t.Fatalf("loaded model = %+v", loaded)
	}
	if sleeping := models[1]; sleeping.BaseURL != "http://localhost:8080/v1" || sleeping.ContextWindow != ai.DefaultContextWindow {
		t.Fatalf("sleeping model = %+v", sleeping)
	}
}

func TestRefreshModelsPersistsAndRestoresSelectableModelsForCacheOnlyStartup(t *testing.T) {
	url := listen(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.RequestURI() {
		case "/models":
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "loaded", "status": map[string]any{"value": "loaded"}, "meta": map[string]any{"n_ctx": 32768}},
				map[string]any{"id": "sleeping", "status": map[string]any{"value": "sleeping"}, "meta": map[string]any{"n_ctx": 32768}},
				map[string]any{"id": "unloaded", "status": map[string]any{"value": "unloaded"}},
			}})
		case "/props?model=loaded&autoload=false":
			writeJSON(w, map[string]any{})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	var cached *ai.ModelsStoreEntry
	first := CreateLlamaProvider()
	if err := first.RefreshModels(RefreshModelsContext{
		Ctx: context.Background(), Credential: apiKeyCredential("local", url), Stored: cached,
		Publish: publishInto(&cached), AllowNetwork: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := modelIDs(first.GetModels()); !reflect.DeepEqual(got, []string{"loaded", "sleeping"}) {
		t.Fatalf("first models = %v", got)
	}
	if cached == nil || len(cached.Models) != 2 || cached.CheckedAt == nil {
		t.Fatalf("cached entry = %+v", cached)
	}
	var persisted Model
	if err := json.Unmarshal(cached.Models[0], &persisted); err != nil || persisted.ID != "loaded" {
		t.Fatalf("persisted model = %+v, %v", persisted, err)
	}

	second := CreateLlamaProvider()
	if err := second.RefreshModels(RefreshModelsContext{
		Ctx: context.Background(), Credential: apiKeyCredential("local", url), Stored: cached,
		Publish: publishInto(&cached), AllowNetwork: false,
	}); err != nil {
		t.Fatal(err)
	}
	models := second.GetModels()
	if got := modelIDs(models); !reflect.DeepEqual(got, []string{"loaded", "sleeping"}) {
		t.Fatalf("restored models = %v", got)
	}
	for _, model := range models {
		if model.BaseURL != url+"/v1" || model.ContextWindow != 32768 {
			t.Fatalf("restored model = %+v", model)
		}
	}
}

func TestProviderStaysDormantUntilConfiguredAndStoresURLPlusOptionalKey(t *testing.T) {
	t.Setenv("LLAMA_BASE_URL", "")
	empty := AuthContext{Env: func(string) (string, bool) { return "", false }}
	ctx := context.Background()
	if result, err := check(ctx, empty, nil); result != nil || err != nil {
		t.Fatalf("Check(unconfigured) = %+v, %v", result, err)
	}
	if result, err := resolve(ctx, empty, nil); result != nil || err != nil {
		t.Fatalf("Resolve(unconfigured) = %+v, %v", result, err)
	}

	url := listen(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, map[string]any{"data": []any{}})
	})
	answers := []string{url, "secret"}
	var prompts []AuthPrompt
	credential, err := login(AuthInteraction{Ctx: ctx, Prompt: func(prompt AuthPrompt) (string, error) {
		prompts = append(prompts, prompt)
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	wantPrompts := []AuthPrompt{
		{Type: "text", Message: "llama.cpp server URL"},
		{Type: "secret", Message: "API key (optional)"},
	}
	if !reflect.DeepEqual(prompts, wantPrompts) {
		t.Fatalf("prompts = %+v, want %+v (LLAMA_BASE_URL set to empty keeps an empty placeholder)", prompts, wantPrompts)
	}
	want := ai.Credential{Type: ai.CredentialAPIKey, Key: "secret", Env: map[string]string{"LLAMA_BASE_URL": url}}
	if !reflect.DeepEqual(credential, want) {
		t.Fatalf("credential = %+v, want %+v", credential, want)
	}
	result, err := resolve(ctx, empty, &credential)
	if err != nil {
		t.Fatal(err)
	}
	wantResult := &AuthResult{Auth: ModelAuth{APIKey: "secret", BaseURL: url + "/v1"}, Env: map[string]string{"LLAMA_BASE_URL": url}, Source: "stored credential"}
	if !reflect.DeepEqual(result, wantResult) {
		t.Fatalf("resolve = %+v, want %+v", result, wantResult)
	}
}

func TestResolveFallsBackToEnvironmentServerAndKey(t *testing.T) {
	env := map[string]string{"LLAMA_BASE_URL": " http://env-host:9000/v1 ", "LLAMA_API_KEY": "env-key"}
	authContext := AuthContext{Env: func(name string) (string, bool) { value, ok := env[name]; return value, ok }}
	result, err := resolve(context.Background(), authContext, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := &AuthResult{Auth: ModelAuth{APIKey: "env-key", BaseURL: "http://env-host:9000/v1"}, Env: map[string]string{"LLAMA_BASE_URL": "http://env-host:9000"}, Source: "LLAMA_BASE_URL"}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("resolve = %+v, want %+v", result, want)
	}
	check, err := check(context.Background(), authContext, nil)
	if err != nil || check == nil || check.Source != "LLAMA_BASE_URL" || check.Type != "api_key" {
		t.Fatalf("check = %+v, %v", check, err)
	}
	delete(env, "LLAMA_API_KEY")
	if result, _ := resolve(context.Background(), authContext, nil); result.Auth.APIKey != "local" {
		t.Fatalf("default key = %q, want local", result.Auth.APIKey)
	}
}
