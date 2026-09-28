package main

import "errors"

// restartInto is unsupported: Windows installs are never upgraded in place.
func restartInto(string) error {
	return errors.New("restarting in place is not supported on Windows")
}
