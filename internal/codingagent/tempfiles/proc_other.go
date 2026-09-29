//go:build !darwin && !linux

package tempfiles

// processStart can't be read here, so no owner ever counts as gone and the
// startup sweep leaves entries alone; exit and session cleanup still run.
func processStart(int) (string, bool) { return "", true }
