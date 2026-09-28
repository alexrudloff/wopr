package main

import (
	"os"

	"golang.org/x/term"
)

// appMode is the run mode selected at startup.
type appMode string

const (
	appModeInteractive appMode = "interactive"
	appModePrint       appMode = "print"
	appModeJSON        appMode = "json"
	appModeRPC         appMode = "rpc"
)

// resolveAppMode selects the run mode: --mode rpc and
// --mode json select those modes; --print, a stdin that is not a terminal, or
// a stdout that is not a terminal select print mode; anything else is
// interactive. `wopr "q" > file` therefore prints the answer instead of drawing
// the TUI into the file.
func resolveAppMode(mode string, print, stdinIsTTY, stdoutIsTTY bool) appMode {
	switch {
	case mode == "rpc":
		return appModeRPC
	case mode == "json":
		return appModeJSON
	case print || !stdinIsTTY || !stdoutIsTTY:
		return appModePrint
	default:
		return appModeInteractive
	}
}

// processAppMode resolves the app mode for this process's flags and standard
// streams.
func processAppMode(flags CLIFlags) appMode {
	return resolveAppMode(flags.Mode, flags.Print != "",
		term.IsTerminal(int(os.Stdin.Fd())), term.IsTerminal(int(os.Stdout.Fd())))
}
