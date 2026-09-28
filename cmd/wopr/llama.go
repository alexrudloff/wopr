package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/alexrudloff/wopr/ai"
	"github.com/alexrudloff/wopr/coding"
	"github.com/alexrudloff/wopr/internal/codingagent/llama"
)

// llamaSourcePath is the source path RPC reports for the built-in llama.cpp
// command.
const llamaSourcePath = "<inline:llama.cpp>"

// catalogRefreshTimeout bounds background model catalog refreshes.
const catalogRefreshTimeout = 15 * time.Second

// startBuiltInLlama registers the built-in llama.cpp provider and restores
// its stored catalog through the cache-only refresh every mode runs at
// startup.
func startBuiltInLlama(ctx context.Context, services *coding.Services) *llama.Host {
	store := ai.NewFileModelsStore(filepath.Join(services.AgentDir(), "models-store.json"))
	host := llama.NewHost(services.Registry().ModelRegistry, services.Auth(), store)
	host.Refresh(ctx, false)
	return host
}

// refreshCatalogsInBackground runs the RPC-mode background catalog refresh:
// skipped offline and abandoned after 15 s.
func refreshCatalogsInBackground(ctx context.Context, host *llama.Host) {
	if IsOfflineModeEnabled() {
		return
	}
	go func() {
		refreshCtx, cancel := context.WithTimeout(ctx, catalogRefreshTimeout)
		defer cancel()
		host.Refresh(refreshCtx, true)
	}()
}
