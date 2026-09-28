package codingagent

import (
	"time"

	"github.com/gofrs/flock"
)

// Settings and trust files take a sync lock with bounded retries while
// another process holds it.
const (
	syncLockMaxAttempts = 10
	syncLockDelay       = 20 * time.Millisecond
)

// acquireSyncLockWithRetry tries lock up to
// syncLockMaxAttempts times with syncLockDelay between attempts, and reports
// false when the lock stays held.
func acquireSyncLockWithRetry(lock *flock.Flock) (bool, error) {
	for attempt := 1; ; attempt++ {
		locked, err := lock.TryLock()
		if err != nil || locked {
			return locked, err
		}
		if attempt == syncLockMaxAttempts {
			return false, nil
		}
		time.Sleep(syncLockDelay)
	}
}
