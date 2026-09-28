package ai

import (
	"slices"
	"strings"
	"testing"
)

func objectSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"payload": map[string]any{"type": "string"}},
		"required":             []any{"payload"},
		"additionalProperties": false,
	}
}

func sampleGrammarTool(cfg *ConstrainedSamplingConfig) ToolSchema {
	return ToolSchema{Name: "sample_tool", Description: "Sample tool", Parameters: objectSchema(), ConstrainedSampling: cfg}
}

func TestResolveJSONSchemaStrictSampling(t *testing.T) {
	// json_schema + supportsStrictMode → strict true.
	got, err := resolveJSONSchemaStrictSampling(sampleGrammarTool(&ConstrainedSamplingConfig{Type: "json_schema", Strict: "prefer"}), true)
	if err != nil || got == nil || *got != true {
		t.Fatalf("prefer+supported: got %v err %v, want &true", got, err)
	}
	// prefer + !supportsStrictMode → nil (fall back silently).
	got, err = resolveJSONSchemaStrictSampling(sampleGrammarTool(&ConstrainedSamplingConfig{Type: "json_schema", Strict: "prefer"}), false)
	if err != nil || got != nil {
		t.Fatalf("prefer+unsupported: got %v err %v, want nil", got, err)
	}
	// require + !supportsStrictMode → error.
	_, err = resolveJSONSchemaStrictSampling(sampleGrammarTool(&ConstrainedSamplingConfig{Type: "json_schema", Strict: "require"}), false)
	if err == nil || !strings.Contains(err.Error(), `Tool "sample_tool" requires JSON-schema constrained sampling`) {
		t.Fatalf("require+unsupported err = %v, want the strict-required message", err)
	}
	// no config → nil.
	got, err = resolveJSONSchemaStrictSampling(sampleGrammarTool(nil), true)
	if err != nil || got != nil {
		t.Fatalf("no config: got %v err %v, want nil", got, err)
	}
}

// TestMakeStrictJSONSchema covers a nested optional property. The source
// schema must remain unchanged.
func TestMakeStrictJSONSchema(t *testing.T) {
	parameters := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":     map[string]any{"type": "string"},
			"offset":   map[string]any{"type": "number"},
			"metadata": map[string]any{"type": "object", "properties": map[string]any{"enabled": map[string]any{"type": "boolean"}}},
			"nullable": map[string]any{"anyOf": []any{map[string]any{"type": "string"}, map[string]any{"type": "null"}}},
		},
		"required": []any{"path", "metadata"},
	}

	strict, err := makeStrictJSONSchema(parameters)
	if err != nil {
		t.Fatalf("makeStrictJSONSchema: %v", err)
	}
	if _, exists := parameters["additionalProperties"]; exists {
		t.Fatal("makeStrictJSONSchema mutated its input")
	}
	properties := strict["properties"].(map[string]any)
	offset := properties["offset"].(map[string]any)
	if _, ok := offset["anyOf"]; !ok {
		t.Fatalf("optional offset = %#v, want nullable anyOf", offset)
	}
	metadata := properties["metadata"].(map[string]any)
	if metadata["additionalProperties"] != false {
		t.Fatalf("nested additionalProperties = %#v, want false", metadata["additionalProperties"])
	}
	enabled := metadata["properties"].(map[string]any)["enabled"].(map[string]any)
	if _, ok := enabled["anyOf"]; !ok {
		t.Fatalf("optional nested property = %#v, want nullable anyOf", enabled)
	}
	required := toStringSlice(strict["required"])
	for _, name := range []string{"path", "offset", "metadata", "nullable"} {
		if !slices.Contains(required, name) {
			t.Fatalf("strict required = %v, missing %q", required, name)
		}
	}
}
