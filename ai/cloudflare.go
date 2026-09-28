package ai

import "strings"

const (
	CloudflareWorkersAIBaseURL          = "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1"
	CloudflareAIGatewayCompatBaseURL    = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat"
	CloudflareAIGatewayOpenAIBaseURL    = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai"
	CloudflareAIGatewayAnthropicBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic"
)

func isCloudflareProvider(providerID string) bool {
	return providerID == "cloudflare-workers-ai" || providerID == "cloudflare-ai-gateway"
}

// ResolveCloudflareBaseURL substitutes account and gateway placeholders using only explicit provider environment values. Missing values retain their placeholders; empty values replace them with empty strings.
func ResolveCloudflareBaseURL(providerID, baseURL string, env ProviderEnv) (string, error) {
	if !isCloudflareProvider(providerID) || env == nil {
		return baseURL, nil
	}
	for _, name := range []string{cloudflareAccountID, cloudflareGatewayID} {
		if value, exists := env[name]; exists {
			baseURL = strings.ReplaceAll(baseURL, "{"+name+"}", value)
		}
	}
	return baseURL, nil
}
