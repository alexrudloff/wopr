package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/alexrudloff/wopr/internal/codingagent"
)

// selfUpdateTimeout bounds the whole self-update (release lookup, signature,
// and archive download). Generous because a release archive can be tens of
// megabytes.
const selfUpdateTimeout = 5 * time.Minute

// newReleaseSource returns where `wopr update` reads releases. Tests replace it
// with an httptest server and a test signing key.
var newReleaseSource = func() codingagent.ReleaseSource {
	return codingagent.DefaultReleaseSource(&http.Client{Timeout: selfUpdateTimeout})
}

// runSelfUpdate resolves the latest GitHub release and, when it is newer (or
// force is set), replaces a standalone binary with the verified release.
func runSelfUpdate(force bool) int {
	source := newReleaseSource()
	ctx, cancel := context.WithTimeout(context.Background(), selfUpdateTimeout)
	defer cancel()

	// The version check runs before the installation tier, so an installation
	// that is already current exits 0 even when wopr cannot update it.
	latest, err := source.LatestVersion(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Update failed: could not resolve the latest %s release: %v\n%s\n",
			codingagent.AppName, err, codingagent.SelfUpdateFallback())
		return 1
	}
	if !force && codingagent.CompareVersions(Version, latest) >= 0 {
		fmt.Fprintf(os.Stderr, "%s %s is up to date.\n", codingagent.AppName, Version)
		return 0
	}

	result := codingagent.ApplySelfUpdateTier(func(exePath string) error {
		return applyStandaloneUpdate(ctx, source, latest, exePath)
	})
	switch result.Action {
	case "standalone-updated":
		return 0
	case "refused":
		// Immutable/container/unsupported: the tier refused mutation and the
		// message is the exact remediation.
		return failf("%s", result.Message)
	default:
		return failf("Update failed: %s", result.Message)
	}
}

// applyStandaloneUpdate is the standalone-binary tier: verify the release's
// signed SHA256SUMS, download and hash-check the platform archive, and
// atomically replace the executable. Its failures surface with no fallback.
func applyStandaloneUpdate(ctx context.Context, source codingagent.ReleaseSource, version, exePath string) error {
	archive, err := source.ResolveArchive(ctx, version)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, codingagent.SelfUpdateFallback())
	}
	fmt.Fprintf(os.Stderr, "Updating %s %s → %s...\n", codingagent.AppName, Version, version)
	if err := source.InstallArchive(ctx, archive, exePath); err != nil {
		return fmt.Errorf("%w\n%s", err, codingagent.SelfUpdateFallback())
	}
	fmt.Fprintf(os.Stderr, "Updated to %s %s.\n", codingagent.AppName, version)
	return nil
}
