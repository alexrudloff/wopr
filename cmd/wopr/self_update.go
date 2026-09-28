package main

import (
	"context"
	"errors"
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
	ctx, cancel := context.WithTimeout(context.Background(), selfUpdateTimeout)
	defer cancel()
	result, err := codingagent.UpdateSelf(ctx, newReleaseSource(), Version, force, func(stage, version string) {
		if stage == codingagent.UpdateStageDownload {
			fmt.Fprintf(os.Stderr, "Updating %s %s → %s...\n", codingagent.AppName, Version, version)
		}
	})
	var refused *codingagent.SelfUpdateRefused
	switch {
	case errors.As(err, &refused):
		return failf("%s", refused.Message)
	case err != nil:
		return failf("Update failed: %v", err)
	case result.UpToDate:
		fmt.Fprintf(os.Stderr, "%s %s is up to date.\n", codingagent.AppName, Version)
	default:
		fmt.Fprintf(os.Stderr, "Updated to %s %s.\n", codingagent.AppName, result.Latest)
	}
	return 0
}
