package main

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/internal/codingagent"
	"github.com/alexrudloff/wopr/tui"
)

// printModelDiagnostic writes a model-resolution warning to stderr as a
// yellow "Warning: <msg>" line. Color is applied only when stderr is a
// terminal, so piped/redirected output stays plain.
func printModelDiagnostic(msg string) {
	text := "Warning: " + msg
	if term.IsTerminal(int(os.Stderr.Fd())) {
		text = "\x1b[33m" + text + "\x1b[39m"
	}
	fmt.Fprintln(os.Stderr, text)
}

// formatTokenCount formats a token count as human-readable (e.g., 200000 → "200K").
func formatTokenCount(count int) string {
	if count >= 1_000_000 {
		m := float64(count) / 1_000_000
		if m == float64(int(m)) {
			return fmt.Sprintf("%dM", int(m))
		}
		return fmt.Sprintf("%.1fM", m)
	}
	if count >= 1_000 {
		k := float64(count) / 1_000
		if k == float64(int(k)) {
			return fmt.Sprintf("%dK", int(k))
		}
		return fmt.Sprintf("%.1fK", k)
	}
	return fmt.Sprintf("%d", count)
}

// printModelList enumerates the models available in the given registry (already
// populated with runtime-registered providers such as llama.cpp) plus built-in
// models with configured auth, then prints the catalog. The caller owns
// process exit.
func printModelList(registry *codingagent.ModelRegistry, agentDir, search string) {
	if loadErr := registry.LoadError(); loadErr != "" {
		fmt.Fprintf(os.Stderr, "Warning: errors loading models.json:\n%s\n", loadErr)
	}

	// Auth-filtered models.
	entries := registry.GetAvailable()

	// Also include built-in models with configured auth (env keys, auth.json).
	authed := codingagent.AuthenticatedProviders(agentDir)
	for _, m := range ai.ListModels("") {
		if !authed[m.Provider] {
			continue
		}
		// Skip duplicates already covered by registry entries.
		dup := false
		for _, e := range entries {
			if e.ProviderID == m.Provider && e.ModelID == m.ID {
				dup = true
				break
			}
		}
		if !dup {
			entries = append(entries, codingagent.ModelEntry{
				ProviderID:    m.Provider,
				ModelID:       m.ID,
				DisplayName:   m.DisplayName,
				Reasoning:     m.Reasoning,
				Input:         m.Capabilities,
				ContextWindow: m.ContextWindow,
				MaxTokens:     m.MaxOutputTokens,
			})
		}
	}

	if len(entries) == 0 {
		fmt.Println("No models available. Set API keys in environment variables.")
		return
	}

	// Apply fuzzy filter over "provider id" if search pattern provided.
	if search != "" {
		entries = tui.FuzzyFilter(entries, search, func(e codingagent.ModelEntry) string {
			return e.ProviderID + " " + e.ModelID
		})
	}

	if len(entries) == 0 {
		fmt.Printf("No models matching %q\n", search)
		return
	}

	// Sort by provider, then by model ID.
	slices.SortFunc(entries, func(a, b codingagent.ModelEntry) int {
		if c := strings.Compare(a.ProviderID, b.ProviderID); c != 0 {
			return c
		}
		return strings.Compare(a.ModelID, b.ModelID)
	})

	// Build rows.
	type row struct {
		provider, model, context, maxOut, thinking, images string
	}
	rows := make([]row, len(entries))
	for i, e := range entries {
		thinking := "no"
		if e.Reasoning {
			thinking = "yes"
		}
		images := "no"
		if slices.Contains(e.Input, "image") {
			images = "yes"
		}
		rows[i] = row{
			provider: e.ProviderID,
			model:    e.ModelID,
			context:  formatTokenCount(e.ContextWindow),
			maxOut:   formatTokenCount(e.MaxTokens),
			thinking: thinking,
			images:   images,
		}
	}

	// Calculate column widths.
	headers := row{"provider", "model", "context", "max-out", "thinking", "images"}
	widths := [6]int{
		len(headers.provider), len(headers.model), len(headers.context),
		len(headers.maxOut), len(headers.thinking), len(headers.images),
	}
	for _, r := range rows {
		widths[0] = max(widths[0], len(r.provider))
		widths[1] = max(widths[1], len(r.model))
		widths[2] = max(widths[2], len(r.context))
		widths[3] = max(widths[3], len(r.maxOut))
		widths[4] = max(widths[4], len(r.thinking))
		widths[5] = max(widths[5], len(r.images))
	}

	// Print header.
	fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s\n",
		widths[0], headers.provider,
		widths[1], headers.model,
		widths[2], headers.context,
		widths[3], headers.maxOut,
		widths[4], headers.thinking,
		widths[5], headers.images,
	)

	// Print rows.
	for _, r := range rows {
		fmt.Printf("%-*s  %-*s  %-*s  %-*s  %-*s  %-*s\n",
			widths[0], r.provider,
			widths[1], r.model,
			widths[2], r.context,
			widths[3], r.maxOut,
			widths[4], r.thinking,
			widths[5], r.images,
		)
	}
}
