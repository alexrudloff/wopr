package codingagent

const unknownProvider = "unknown"

// ProviderLoginHelp returns the hint shown when no provider is set up.
func ProviderLoginHelp() string {
	return "Run /setup to connect a subscription, API key, or endpoint."
}

func FormatNoModelsAvailableMessage() string {
	return "No models available. " + ProviderLoginHelp()
}

func FormatNoModelSelectedMessage() string {
	return "No model selected. " + ProviderLoginHelp() + " Then pick one with /model."
}

func FormatNoAPIKeyFoundMessage(provider string) string {
	providerDisplay := provider
	if providerDisplay == "" || providerDisplay == unknownProvider {
		providerDisplay = "the selected model"
	}
	return "No API key found for " + providerDisplay + ".\n\n" + ProviderLoginHelp()
}
