package ai

import (
	"maps"
	"os"
)

// ProviderEnv holds provider-scoped environment overrides whose values take
// precedence over the process environment for provider configuration such as
// regional settings, endpoint placeholders, and proxy variables.
type ProviderEnv = map[string]string

// getProviderEnvValue resolves an environment value from provider-scoped
// overrides first, then the process environment.
func mergeProviderEnv(configured, request ProviderEnv) ProviderEnv {
	if len(configured) == 0 && len(request) == 0 {
		return nil
	}
	merged := maps.Clone(configured)
	if merged == nil {
		merged = make(ProviderEnv, len(request))
	}
	maps.Copy(merged, request)
	return merged
}

func getProviderEnvValue(name string, env ProviderEnv) string {
	if v := env[name]; v != "" {
		return v
	}
	return os.Getenv(name)
}
