package main

import "os"

// setupCli runs before the CLI and RPC entry points. It sets the process
// markers child processes inherit. The HTTP transport is configured from
// settings in main.
func setupCli() {
	_ = os.Setenv("WOPR_CODING_AGENT", "true") // Setenv fails only for invalid names.
	_ = os.Setenv("AI_AGENT", "wopr")
}
