package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/alexrudloff/wopr/internal/codingagent"
)

type projectTrustResolutionOptions struct {
	CWD      string
	Store    *codingagent.ProjectTrustStore
	Override *bool
	Default  string
	// UI prompts for a trust decision; nil means no prompt (untrusted).
	UI trustPrompter
}

// trustPrompter asks the user to pick one of options.
type trustPrompter interface {
	Select(ctx context.Context, title string, options []string) (string, error)
}

func resolveProjectTrusted(ctx context.Context, opts projectTrustResolutionOptions) (bool, error) {
	if opts.Override != nil {
		return *opts.Override, nil
	}
	hasTrustResources := codingagent.HasTrustRequiringProjectResources(opts.CWD)
	if !hasTrustResources {
		return true, nil
	}
	decision, err := opts.Store.Get(opts.CWD)
	if err != nil {
		return false, err
	}
	if decision != nil {
		return *decision, nil
	}
	switch opts.Default {
	case "always":
		return true, nil
	case "never":
		return false, nil
	}
	if opts.UI == nil {
		return false, nil
	}

	options := codingagent.GetProjectTrustOptions(opts.CWD, true)
	labels := make([]string, 0, len(options))
	for _, option := range options {
		labels = append(labels, option.Label)
	}
	selected, err := opts.UI.Select(ctx, formatProjectTrustPrompt(opts.CWD), labels)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return false, nil
		}
		return false, err
	}
	for _, option := range options {
		if option.Label != selected {
			continue
		}
		if len(option.Updates) > 0 {
			if err := opts.Store.SetMany(option.Updates); err != nil {
				return false, err
			}
		}
		return option.Trusted, nil
	}
	return false, nil
}

func formatProjectTrustPrompt(cwd string) string {
	return fmt.Sprintf("Trust project folder?\n%s\n\nThis allows wopr to load project settings, resources, and instructions, and install missing project packages.", cwd)
}

type startupTrustUI struct {
	opts codingagent.StartupUIOptions
}

func newStartupTrustUI(opts codingagent.StartupUIOptions) trustPrompter {
	return &startupTrustUI{opts: opts}
}

func (ui *startupTrustUI) Select(ctx context.Context, title string, options []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	selected, ok, err := codingagent.ShowStartupSelector(title, options, ui.opts)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", context.Canceled
	}
	return options[selected], nil
}
