package ai

import "testing"

// TestGetProviderEnvValue verifies provider-scoped overrides take precedence
// over the process environment, with fallback when absent or empty.
func TestGetProviderEnvValue(t *testing.T) {
	t.Setenv("WOPR_PE_VAR", "from-process")

	if got := getProviderEnvValue("WOPR_PE_VAR", ProviderEnv{"WOPR_PE_VAR": "from-scope"}); got != "from-scope" {
		t.Errorf("scoped env should win: got %q want from-scope", got)
	}
	if got := getProviderEnvValue("WOPR_PE_VAR", nil); got != "from-process" {
		t.Errorf("nil scope should use process env: got %q want from-process", got)
	}
	if got := getProviderEnvValue("WOPR_PE_VAR", ProviderEnv{"WOPR_PE_VAR": ""}); got != "from-process" {
		t.Errorf("empty scoped value should fall back to process env: got %q want from-process", got)
	}
	if got := getProviderEnvValue("WOPR_PE_UNSET", ProviderEnv{"OTHER": "x"}); got != "" {
		t.Errorf("unset var should return empty: got %q", got)
	}
}
