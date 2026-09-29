//go:build !unix

package tempfiles

import "time"

// owned: temp directories are per-user off unix.
func owned(string) bool { return true }

// accessTime is not tracked off unix; modification time decides.
func accessTime(string) time.Time { return time.Time{} }
