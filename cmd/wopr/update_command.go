package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/alexrudloff/wopr/internal/codingagent"
)

const updateUsage = "wopr update [self|wopr] [--force]"

// isSelfUpdateTarget reports whether an update target names wopr itself:
// "self" or the app name "wopr". Bare `wopr update` means the same.
func isSelfUpdateTarget(source string) bool {
	switch strings.TrimSpace(strings.ToLower(source)) {
	case "self", codingagent.AppName:
		return true
	default:
		return false
	}
}

// runUpdateCommand handles `wopr update`, which self-updates the wopr binary.
// It reports -1 for other commands.
func runUpdateCommand(args []string) int {
	if len(args) == 0 || args[0] != "update" {
		return -1
	}
	force := false
	for _, arg := range args[1:] {
		switch {
		case arg == "-h" || arg == "--help":
			fmt.Print("Usage:\n  " + updateUsage + "\n\nUpdate the wopr binary.\n\nOptions:\n  --force    Reinstall wopr even when its version is current\n")
			return 0
		case arg == "--force":
			force = true
		case arg == "--self" || isSelfUpdateTarget(arg):
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "Unknown option %s for \"update\".\n", arg)
			fmt.Fprintf(os.Stderr, "Usage: %s\n", updateUsage)
			return 1
		default:
			fmt.Fprintf(os.Stderr, "Unknown update target %q.\n", arg)
			fmt.Fprintf(os.Stderr, "Usage: %s\n", updateUsage)
			return 1
		}
	}
	return runSelfUpdate(force)
}

// IsOfflineModeEnabled returns true when the WOPR_OFFLINE env var is set to a
// truthy value.
func IsOfflineModeEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("WOPR_OFFLINE")))
	return v == "1" || v == "true" || v == "yes"
}
