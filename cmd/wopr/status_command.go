package main

// A side-effect-free aggregate status surface.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding/resources"
	"github.com/alexrudloff/wopr/internal/codingagent"
)

type statusOutput struct {
	Healthy   bool            `json:"healthy"`
	Paths     statusPaths     `json:"paths"`
	Resources statusResources `json:"resources"`
	// Auth lists the providers with stored credentials, sorted by provider.
	Auth []statusCredential `json:"auth"`
	// DefaultModel is the saved default, "provider/model" or "" when unset.
	DefaultModel string   `json:"defaultModel"`
	Errors       []string `json:"errors"`
}

type statusCredential struct {
	Provider string `json:"provider"`
	Type     string `json:"type"`
}

type statusPaths struct {
	Home    string `json:"home"`
	Agent   string `json:"agent"`
	Project string `json:"project"`
}

type statusResources struct {
	Total    int                  `json:"total"`
	Enabled  int                  `json:"enabled"`
	Disabled int                  `json:"disabled"`
	ByKind   map[string]int       `json:"byKind"`
	Items    []statusResourceItem `json:"items"`
}

type statusResourceItem struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Scope   string `json:"scope"`
	Origin  string `json:"origin"`
	Source  string `json:"source"`
	Enabled bool   `json:"enabled"`
}

func runStatusCommand(args []string) int {
	if len(args) == 0 || args[0] != "status" {
		return -1
	}
	jsonMode := false
	for _, arg := range args[1:] {
		switch arg {
		case "--json":
			jsonMode = true
		case "--no-input":
		case "-h", "--help":
			fmt.Println("Usage: wopr status [--json] [--no-input]")
			return 0
		default:
			fmt.Fprintln(os.Stderr, "Usage: wopr status [--json] [--no-input]")
			return 2
		}
	}
	status := collectStatus()
	if jsonMode {
		data, err := json.Marshal(status)
		if err != nil {
			return failf("wopr status: encode JSON: %v", err)
		}
		fmt.Println(string(data))
	} else {
		renderStatus(status)
	}
	if !status.Healthy {
		return 1
	}
	return 0
}

func collectStatus() statusOutput {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	agentDir := codingagent.AgentDir()
	projectPath := ""
	if projectRoot, ok := projectResourceRoot(cwd); ok {
		projectPath = canonicalStatusPath(projectRoot)
	}
	status := statusOutput{
		Paths: statusPaths{
			Home:    canonicalStatusPath(codingagent.ConfigRoot()),
			Agent:   canonicalStatusPath(agentDir),
			Project: projectPath,
		},
		Resources: statusResources{ByKind: map[string]int{}},
		Errors:    []string{},
	}
	settings := codingagent.NewSettingsManager(cwd, agentDir)
	for _, settingsError := range settings.DrainErrors() {
		status.Errors = append(status.Errors, fmt.Sprintf("%s settings: %v", settingsError.Scope, settingsError.Error))
	}
	global := settings.GetGlobalSettings()
	if global.DefaultModel != "" {
		status.DefaultModel = strings.TrimPrefix(global.DefaultProvider+"/", "/") + global.DefaultModel
	}
	status.Auth = []statusCredential{}
	if infos, err := ai.NewReadOnlyAuthStorage(filepath.Join(agentDir, "auth.json")).List(context.Background()); err != nil {
		status.Errors = append(status.Errors, "auth.json: "+err.Error())
	} else {
		for _, info := range infos {
			status.Auth = append(status.Auth, statusCredential{Provider: info.ProviderID, Type: string(info.Type)})
		}
	}
	if items, err := collectConfigResourceItems(cwd, agentDir, settings); err != nil {
		status.Errors = append(status.Errors, "resource inventory: "+err.Error())
	} else {
		status.Resources.Total = len(items)
		seenIdentities := make(map[string]string, len(items))
		for _, resource := range items {
			kind := resources.Kind(resource.ResourceType)
			name := resource.DisplayName
			if resolvedName, err := resources.Name(kind, resource.Path); err != nil {
				status.Errors = append(status.Errors, fmt.Sprintf("%s resource %s: %v", kind, resource.Path, err))
			} else {
				name = resolvedName
			}
			if name == "" {
				name = filepath.Base(resource.Path)
			}
			identity := string(resource.ResourceType) + "\x00" + resource.Scope + "\x00" + name
			if previous, exists := seenIdentities[identity]; exists {
				status.Errors = append(status.Errors, fmt.Sprintf("duplicate %s resource %q in %s scope: %s and %s", kind, name, resource.Scope, previous, resource.Path))
			} else {
				seenIdentities[identity] = resource.Path
			}
			status.Resources.Items = append(status.Resources.Items, statusResourceItem{
				Kind: string(resource.ResourceType), Name: name, Path: canonicalStatusPath(resource.Path),
				Scope: resource.Scope, Origin: resource.Origin, Source: resource.Source,
				Enabled: resource.Enabled,
			})
			status.Resources.ByKind[string(resource.ResourceType)]++
			if resource.Enabled {
				status.Resources.Enabled++
			} else {
				status.Resources.Disabled++
			}
		}
		slices.SortFunc(status.Resources.Items, func(a, b statusResourceItem) int {
			for _, value := range []int{strings.Compare(a.Kind, b.Kind), strings.Compare(a.Name, b.Name), strings.Compare(a.Scope, b.Scope), strings.Compare(a.Path, b.Path)} {
				if value != 0 {
					return value
				}
			}
			return 0
		})
	}
	status.Errors = deduplicateStatusErrors(status.Errors)
	status.Healthy = len(status.Errors) == 0
	return status
}

func renderStatus(status statusOutput) {
	state := "healthy"
	if !status.Healthy {
		state = "invalid"
	}
	fmt.Printf("WOPR status: %s\n", state)
	fmt.Printf("  paths: home=%s agent=%s project=%s\n", status.Paths.Home, status.Paths.Agent, status.Paths.Project)
	fmt.Printf("  resources: %d (enabled=%d disabled=%d)\n", status.Resources.Total, status.Resources.Enabled, status.Resources.Disabled)
	auth := make([]string, len(status.Auth))
	for i, credential := range status.Auth {
		auth[i] = credential.Provider + " (" + credential.Type + ")"
	}
	fmt.Printf("  auth: %s\n", cmp.Or(strings.Join(auth, ", "), "none"))
	fmt.Printf("  default model: %s\n", cmp.Or(status.DefaultModel, "none"))
	for _, message := range status.Errors {
		fmt.Fprintln(os.Stderr, "error:", message)
	}
}

func canonicalStatusPath(path string) string {
	absolute, err := filepath.Abs(path)
	if err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func deduplicateStatusErrors(errors []string) []string {
	slices.Sort(errors)
	return slices.Compact(errors)
}
