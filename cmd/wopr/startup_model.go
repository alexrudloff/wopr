package main

// Startup model selection: the CLI model, then the findInitialModel
// fallback (saved default with auth, then a known provider default, then the
// first available model).

import (
	"errors"
	"os"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

// startupModelRuntime is the ModelRuntime surface startup model selection
// reads: the composed model list and per-provider configured auth.
type startupModelRuntime struct {
	models  []codingagent.RuntimeModel
	hasAuth func(providerID string) bool
	auth    map[string]bool
}

func newStartupModelRuntime(models []codingagent.RuntimeModel, hasAuth func(providerID string) bool) *startupModelRuntime {
	return &startupModelRuntime{models: models, hasAuth: hasAuth, auth: map[string]bool{}}
}

// GetModels returns every composed model, or one provider's.
func (rt *startupModelRuntime) GetModels(providerID string) []codingagent.RuntimeModel {
	if providerID == "" {
		return slices.Clone(rt.models)
	}
	var models []codingagent.RuntimeModel
	for _, model := range rt.models {
		if model.Provider == providerID {
			models = append(models, model)
		}
	}
	return models
}

// HasConfiguredAuth reports the provider's configured auth, read once.
func (rt *startupModelRuntime) HasConfiguredAuth(providerID string) bool {
	configured, ok := rt.auth[providerID]
	if !ok {
		configured = rt.hasAuth(providerID)
		rt.auth[providerID] = configured
	}
	return configured
}

// getModel returns an exact composed model.
func (rt *startupModelRuntime) getModel(providerID, modelID string) *codingagent.RuntimeModel {
	index := slices.IndexFunc(rt.models, func(model codingagent.RuntimeModel) bool {
		return model.Provider == providerID && model.ID == modelID
	})
	if index < 0 {
		return nil
	}
	return &rt.models[index]
}

// getAvailable returns the composed models whose provider has configured auth.
func (rt *startupModelRuntime) getAvailable() []codingagent.RuntimeModel {
	var available []codingagent.RuntimeModel
	for _, model := range rt.models {
		if rt.HasConfiguredAuth(model.Provider) {
			available = append(available, model)
		}
	}
	return available
}

// findInitialModel picks a model from the saved default and the available
// models: the saved default when its provider has auth, then the preferred
// model (coding.PreferredModelID) of the first provider with models
// available, then the first available model.
func findInitialModel(rt *startupModelRuntime, defaultProvider, defaultModelID string) *codingagent.RuntimeModel {
	if defaultProvider != "" && defaultModelID != "" {
		if found := rt.getModel(defaultProvider, defaultModelID); found != nil && rt.HasConfiguredAuth(found.Provider) {
			return found
		}
	}
	available := rt.getAvailable()
	if len(available) == 0 {
		return nil
	}
	preferred, ok := coding.PreferredModelID(available[0].Provider)
	if index := slices.IndexFunc(available, func(model codingagent.RuntimeModel) bool {
		return ok && model.Provider == available[0].Provider && model.ID == preferred
	}); index >= 0 {
		return &available[index]
	}
	return &available[0]
}

// startupModelOptions carries the CLI and session inputs startup model
// selection reads.
type startupModelOptions struct {
	CLIProvider string
	CLIModel    string
	CLIThinking string
	// APIKey is --api-key: a non-persistent key for the CLI model's
	// provider.
	APIKey string
}

// startupModel is the selected model, the thinking level its CLI pattern
// named, and the warnings to report.
type startupModel struct {
	Model    *ai.Model
	Thinking string
	Warnings []string
}

// selectStartupModel picks the session's starting model. A nil Model with a nil error means no
// model is available.
func selectStartupModel(options startupModelOptions, settings codingagent.Settings, services *coding.Services) (startupModel, error) {
	registry := services.Registry()
	rt := newStartupModelRuntime(registry.RuntimeModels(), registry.HasConfiguredAuth)
	var result startupModel
	selected, err := selectSessionOptionModel(rt, options, settings, &result)
	if err != nil {
		return result, err
	}
	if options.APIKey != "" {
		if selected == nil {
			return result, errors.New("--api-key requires a model to be specified via --model or --provider/--model")
		}
		registry.SetRuntimeAPIKey(selected.Provider, options.APIKey)
	}
	if selected == nil {
		if selected = findInitialModel(rt, settings.DefaultProvider, settings.DefaultModel); selected == nil {
			return result, nil
		}
	}
	model, warning, err := coding.BuildModelWithWarning(selected.Provider+"/"+selected.ID, services)
	if warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}
	result.Model = model
	return result, err
}

// selectSessionOptionModel returns the --model resolution, or nil. It records the thinking level and warnings in result.
func selectSessionOptionModel(rt *startupModelRuntime, options startupModelOptions, settings codingagent.Settings, result *startupModel) (*codingagent.RuntimeModel, error) {
	if model, thinking, ok := testFauxCLIModel(options); ok {
		result.Thinking = thinking
		return model, nil
	}
	if options.CLIModel != "" {
		resolved := ResolveCliModel(options.CLIProvider, options.CLIModel, options.CLIThinking, rt)
		if resolved.Warning != "" {
			result.Warnings = append(result.Warnings, resolved.Warning)
		}
		if resolved.Error != "" {
			return nil, errors.New(resolved.Error)
		}
		if resolved.Model != nil {
			result.Thinking = resolved.ThinkingLevel
			return resolved.Model, nil
		}
	}
	return nil, nil
}

// testFauxCLIModel selects the test-only test-faux provider, which has no
// catalog entry, for a --model naming it. It exists only when WOPR_TEST_FAUX=1,
// so normal runs resolve such a --model like any unknown model.
func testFauxCLIModel(options startupModelOptions) (*codingagent.RuntimeModel, string, bool) {
	if os.Getenv("WOPR_TEST_FAUX") != "1" {
		return nil, "", false
	}
	spec := options.CLIModel
	if options.CLIProvider == "test-faux" && !strings.HasPrefix(spec, "test-faux/") {
		spec = "test-faux/" + spec
	}
	modelID, ok := strings.CutPrefix(spec, "test-faux/")
	if !ok || modelID == "" {
		return nil, "", false
	}
	thinking := ""
	if colon := strings.LastIndex(modelID, ":"); colon != -1 && validThinkingLevels[modelID[colon+1:]] {
		modelID, thinking = modelID[:colon], modelID[colon+1:]
	}
	return &codingagent.RuntimeModel{Provider: "test-faux", ID: modelID, Name: "Test Faux"}, thinking, true
}
