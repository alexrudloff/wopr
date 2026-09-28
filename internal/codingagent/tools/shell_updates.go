package tools

import (
	"sync"
	"time"

	"github.com/alexrudloff/wopr/agent"
)

// bashUpdateThrottle is the minimum time between streaming updates.
const bashUpdateThrottle = 100 * time.Millisecond

func bashUpdateDelay(lastUpdateAt, now time.Time) time.Duration {
	if lastUpdateAt.IsZero() {
		return 0
	}
	return bashUpdateThrottle - now.Sub(lastUpdateAt)
}

// shellUpdateScheduler throttles streaming updates: when the throttle window has passed
// the update goes out at once, otherwise one deferred update is pending, and
// nothing is emitted unless output arrived since the last update.
//
// One emitter goroutine sends the updates, so a slow consumer never stalls
// the output reader (the command would block on write) and updates stay
// serialised.
type shellUpdateScheduler struct {
	output   *OutputAccumulator
	onUpdate agent.ToolUpdateCallback

	mu    sync.Mutex
	dirty bool

	wake         chan struct{}
	stop         chan struct{}
	done         chan struct{}
	lastUpdateAt time.Time // emitter goroutine only, then finish
}

func newShellUpdateScheduler(output *OutputAccumulator, onUpdate agent.ToolUpdateCallback) *shellUpdateScheduler {
	u := &shellUpdateScheduler{
		output:   output,
		onUpdate: onUpdate,
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go u.run()
	return u
}

// schedule sends an update now or defers one to the end of the throttle window.
func (u *shellUpdateScheduler) schedule() {
	u.mu.Lock()
	u.dirty = true
	u.mu.Unlock()
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

func (u *shellUpdateScheduler) run() {
	defer close(u.done)
	for {
		select {
		case <-u.stop:
			return
		case <-u.wake:
		}
		if delay := bashUpdateDelay(u.lastUpdateAt, time.Now()); delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-u.stop:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		u.emit()
	}
}

// emit sends the current output as an update.
func (u *shellUpdateScheduler) emit() {
	u.mu.Lock()
	if !u.dirty {
		u.mu.Unlock()
		return
	}
	u.dirty = false
	u.mu.Unlock()
	u.lastUpdateAt = time.Now()
	snapshot := u.output.Snapshot(true)
	var details any
	if snapshot.Truncation.Truncated || snapshot.FullOutputPath != "" {
		bd := &BashDetails{FullOutputPath: snapshot.FullOutputPath}
		if snapshot.Truncation.Truncated {
			tr := snapshot.Truncation
			bd.Truncation = &tr
		}
		details = bd
	}
	u.onUpdate(snapshot.Content, details)
}

// finish drops a
// pending deferred update, wait for an in-flight one, then emit the final
// output if it changed.
func (u *shellUpdateScheduler) finish() {
	close(u.stop)
	<-u.done
	u.emit()
}
