package main

import (
	"fmt"
	"os"
)

// stopProfiles writes the WOPR_PROFILE profiles that main started. It is a no-op
// when WOPR_PROFILE is unset.
var stopProfiles = func() {}

// exitProcess writes profiles before exiting. os.Exit skips deferred calls, so
// main uses this on every exit path.
func exitProcess(code int) {
	stopProfiles()
	os.Exit(code)
}

// fatalf prints a line to stderr and exits with status 1.
func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	exitProcess(1)
}

// failf prints a line to stderr and returns exit status 1.
func failf(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	return 1
}
