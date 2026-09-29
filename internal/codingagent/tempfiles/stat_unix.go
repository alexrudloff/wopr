//go:build unix

package tempfiles

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// owned reports that path (not followed) belongs to the current user.
func owned(path string) bool {
	var st unix.Stat_t
	return unix.Lstat(path, &st) == nil && int(st.Uid) == os.Getuid()
}

// accessTime is path's last access time, or zero.
func accessTime(path string) time.Time {
	var st unix.Stat_t
	if unix.Lstat(path, &st) != nil {
		return time.Time{}
	}
	return time.Unix(st.Atim.Unix())
}
