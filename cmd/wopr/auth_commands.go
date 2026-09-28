package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

type loginCommand string

const (
	authLogin  loginCommand = "login"
	authLogout loginCommand = "logout"
)

type loginCLIOptions struct {
	command  loginCommand
	provider string
	help     bool
	list     bool
	json     bool
	noInput  bool
	invalid  string
}

func parseLoginCommand(args []string) (*loginCLIOptions, bool) {
	if len(args) == 0 {
		return nil, false
	}
	switch args[0] {
	case "login", "logout":
	default:
		return nil, false
	}
	opts := &loginCLIOptions{command: loginCommand(args[0])}
	for _, arg := range args[1:] {
		switch arg {
		case "-h", "--help":
			opts.help = true
		case "--list":
			opts.list = true
		case "--json":
			opts.json = true
		case "--no-input":
			opts.noInput = true
		default:
			if strings.HasPrefix(arg, "-") || opts.provider != "" {
				opts.invalid = arg
				continue
			}
			opts.provider = arg
		}
	}
	return opts, true
}

func runLoginCommand(args []string) int {
	opts, ok := parseLoginCommand(args)
	if !ok {
		return -1
	}
	if opts.invalid != "" || (opts.list && opts.command != authLogin) || (opts.list && opts.provider != "") {
		fmt.Fprintf(os.Stderr, "Usage: wopr %s [provider] [--list] [--json] [--no-input]\n", opts.command)
		return 2
	}
	if opts.help {
		printLoginCommandHelp(opts.command)
		return 0
	}
	if opts.command == authLogin && opts.list {
		return listAuthTargets(opts.json)
	}
	if opts.noInput && opts.provider == "" {
		fmt.Fprintf(os.Stderr, "wopr %s: --no-input requires an explicit provider; use `wopr login --list --json` to discover targets\n", opts.command)
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		return failf("error: %v", err)
	}
	agentDir := codingagent.AgentDir()
	authPath := filepath.Join(agentDir, "auth.json")
	store, err := ai.NewAuthStorage(authPath)
	if err != nil {
		return failf("error: %v", err)
	}
	_ = cwd

	switch opts.command {
	case authLogin:
		provider, err := resolveLoginProvider(opts.provider)
		if err != nil {
			return failf("%v", err)
		}
		cred, err := runOAuthProviderLogin(provider, opts.noInput)
		if err != nil {
			return failf("Error: %v", err)
		}
		credentialPath := "auth.json"
		if credentialStore, ok := provider.(ai.OAuthCredentialStore); ok {
			credentialPath, err = credentialStore.StoreOAuthCredentials(cred)
		} else {
			err = store.Set(provider.ID(), ai.Credential{Type: ai.CredentialOAuth, Refresh: cred.Refresh, Access: cred.Access, Expires: cred.Expires, ProjectID: cred.ProjectID})
		}
		if err != nil {
			return failf("Error: %v", err)
		}
		fmt.Printf("\nCredentials saved to %s\n", credentialPath)
		return 0
	case authLogout:
		providerID, err := resolveLogoutProvider(store, opts.provider)
		if err != nil {
			return failf("%v", err)
		}
		removedFromStore := false
		if provider, ok := ai.GetOAuthProvider(providerID); ok {
			if credentialStore, ok := provider.(ai.OAuthCredentialStore); ok {
				removedFromStore, err = credentialStore.DeleteOAuthCredentials()
				if err != nil {
					return failf("Error: %v", err)
				}
			}
		}
		if err := store.Delete(providerID); err != nil {
			return failf("Error: %v", err)
		}
		if removedFromStore {
			fmt.Printf("Logged out from %s. Credentials removed from provider store\n", providerID)
		} else {
			fmt.Printf("Logged out from %s. Credentials removed from auth.json\n", providerID)
		}
		return 0
	default:
		return -1
	}
}

type authTargetListOutput struct {
	Targets []authTargetItem `json:"targets"`
}

type authTargetItem struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	CallbackServer bool   `json:"callbackServer"`
}

// listAuthTargets prints the registered OAuth providers.
func listAuthTargets(jsonMode bool) int {
	targets := registeredAuthTargetItems()
	if jsonMode {
		data, err := json.Marshal(authTargetListOutput{Targets: targets})
		if err != nil {
			return failf("wopr login --list: encode JSON: %v", err)
		}
		fmt.Println(string(data))
		return 0
	}
	if len(targets) == 0 {
		fmt.Println("No authentication targets registered.")
		return 0
	}
	fmt.Println("Authentication targets:")
	for _, target := range targets {
		fmt.Printf("  %s\t%s\n", target.ID, target.Name)
	}
	return 0
}

func printLoginCommandHelp(cmd loginCommand) {
	switch cmd {
	case authLogin:
		fmt.Print("Usage:\n  wopr login [provider] [--no-input]\n  wopr login --list [--json]\n\nLogin to an OAuth provider or list registered auth targets.\n\nExamples:\n  wopr login\n  wopr login github-copilot\n  wopr login --list --json\n")
	case authLogout:
		fmt.Print("Usage:\n  wopr logout [provider] [--no-input]\n\nRemove stored OAuth credentials from auth.json.\n\nExamples:\n  wopr logout\n  wopr logout github-copilot\n")
	}
}

func resolveLoginProvider(input string) (ai.OAuthProviderInterface, error) {
	if input != "" {
		return oauthProvider(input)
	}
	targets := registeredAuthTargetItems()
	fmt.Println("Select a provider:")
	fmt.Println()
	for i, target := range targets {
		fmt.Printf("  %d. %s\n", i+1, target.Name)
	}
	fmt.Println()
	fmt.Printf("Enter number (1-%d): ", len(targets))
	line, err := readLine(os.Stdin)
	if err != nil {
		return nil, err
	}
	idx, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || idx < 1 || idx > len(targets) {
		return nil, fmt.Errorf("Invalid selection")
	}
	return oauthProvider(targets[idx-1].ID)
}

func oauthProvider(id string) (ai.OAuthProviderInterface, error) {
	if provider, ok := ai.GetOAuthProvider(id); ok {
		return provider, nil
	}
	return nil, fmt.Errorf("Unknown provider: %s", id)
}

func registeredAuthTargetItems() []authTargetItem {
	providers := sortedOAuthProviders()
	items := make([]authTargetItem, 0, len(providers))
	for _, provider := range providers {
		items = append(items, authTargetItem{ID: provider.ID(), Name: provider.Name(), CallbackServer: provider.UsesCallbackServer()})
	}
	return items
}

func resolveLogoutProvider(store *ai.AuthStorage, input string) (string, error) {
	if input != "" {
		return input, nil
	}
	creds, err := store.Load()
	if err != nil {
		return "", err
	}
	var providers []string
	providersByID := make(map[string]struct{})
	for id, cred := range creds {
		if cred.Type == ai.CredentialOAuth {
			providers = append(providers, id)
			providersByID[id] = struct{}{}
		}
	}
	for _, provider := range ai.GetOAuthProviders() {
		if _, exists := providersByID[provider.ID()]; exists {
			continue
		}
		credentialStore, ok := provider.(ai.OAuthCredentialStore)
		if !ok {
			continue
		}
		if _, present := credentialStore.OAuthCredentialStatus(); present {
			providers = append(providers, provider.ID())
			providersByID[provider.ID()] = struct{}{}
		}
	}
	slices.Sort(providers)
	if len(providers) == 0 {
		return "", fmt.Errorf("No OAuth providers logged in.")
	}
	if len(providers) == 1 {
		return providers[0], nil
	}
	fmt.Println("Select a provider to logout:")
	fmt.Println()
	for i, provider := range providers {
		fmt.Printf("  %d. %s\n", i+1, provider)
	}
	fmt.Println()
	fmt.Printf("Enter number (1-%d): ", len(providers))
	line, err := readLine(os.Stdin)
	if err != nil {
		return "", err
	}
	idx, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || idx < 1 || idx > len(providers) {
		return "", fmt.Errorf("Invalid selection")
	}
	return providers[idx-1], nil
}

func runOAuthProviderLogin(provider ai.OAuthProviderInterface, noInput bool) (ai.OAuthCredentials, error) {
	callbacks := ai.OAuthLoginCallbacks{
		OnAuth: func(info ai.OAuthAuthInfo) {
			fmt.Printf("\nOpen this URL in your browser:\n%s\n", info.URL)
			if info.Instructions != "" {
				fmt.Println(info.Instructions)
			}
			fmt.Println()
		},
		OnPrompt: func(prompt ai.OAuthPrompt) (string, error) {
			if noInput {
				return "", fmt.Errorf("provider %s requires interactive input; rerun without --no-input", provider.ID())
			}
			question := prompt.Message
			if prompt.AllowEmpty && provider.ID() == "github-copilot" {
				question += " (blank for github.com)"
			}
			if prompt.Placeholder != "" {
				question += fmt.Sprintf(" (%s)", prompt.Placeholder)
			}
			question += ": "
			fmt.Print(question)
			return readLine(os.Stdin)
		},
		OnManualCodeInput: func() (string, error) {
			if noInput {
				return "", fmt.Errorf("provider %s requires a callback code; rerun without --no-input", provider.ID())
			}
			fmt.Print("Enter code or callback URL: ")
			return readLine(os.Stdin)
		},
		OnProgress: func(message string) {
			fmt.Println(message)
		},
		OnDeviceCode: func(info ai.OAuthDeviceCodeInfo) {
			fmt.Printf("\nOpen this URL in your browser:\n%s\n", info.VerificationURI)
			fmt.Printf("Enter code: %s\n", info.UserCode)
		},
		OnSelect: func(prompt ai.OAuthSelectPrompt) (string, error) {
			if noInput {
				return "", fmt.Errorf("provider %s requires a selection; rerun without --no-input", provider.ID())
			}
			fmt.Println(prompt.Message)
			for i, opt := range prompt.Options {
				fmt.Printf("  %d) %s\n", i+1, opt.Label)
			}
			fmt.Print("Choice [1]: ")
			line, err := readLine(os.Stdin)
			if err != nil {
				return "", err
			}
			line = strings.TrimSpace(line)
			if line == "" {
				return prompt.Options[0].ID, nil
			}
			n, convErr := strconv.Atoi(line)
			if convErr != nil || n < 1 || n > len(prompt.Options) {
				return "", fmt.Errorf("invalid choice %q", line)
			}
			return prompt.Options[n-1].ID, nil
		},
	}
	return provider.Login(context.Background(), callbacks)
}

func sortedOAuthProviders() []ai.OAuthProviderInterface {
	providers := ai.GetOAuthProviders()
	order := map[string]int{
		"anthropic":      0,
		"github-copilot": 1,
		"openai-codex":   2,
	}
	slices.SortFunc(providers, func(a, b ai.OAuthProviderInterface) int {
		aiOrder, aok := order[a.ID()]
		biOrder, bok := order[b.ID()]
		switch {
		case aok && bok && aiOrder != biOrder:
			return aiOrder - biOrder
		case aok && !bok:
			return -1
		case !aok && bok:
			return 1
		}
		return strings.Compare(a.Name(), b.Name())
	})
	return providers
}

func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
