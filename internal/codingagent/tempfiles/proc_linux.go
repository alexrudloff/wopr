package tempfiles

import (
	"os"
	"strconv"
	"strings"
)

// processStart is pid's start time in clock ticks since boot (field 22 of
// /proc/<pid>/stat), or ok false when no such process runs.
func processStart(pid int) (string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", false
	}
	// The command name may hold spaces; fields resume after its ")".
	_, rest, ok := strings.Cut(string(data), ") ")
	fields := strings.Fields(rest)
	if !ok || len(fields) < 20 {
		return "", false
	}
	return fields[19], true
}
