//go:build unix

package tools

import (
	"testing"
	"time"
)

// Stopping a background job signals its own process group, child and
// grandchild alike, and never a group that could be wopr's or everyone's.
func TestBackgroundShellStopTargetsOnlyItsGroup(t *testing.T) {
	for _, pgid := range []int{0, 1, -5, ownProcessGroup()} {
		if validJobGroup(pgid) {
			t.Fatalf("process group %d accepted as a job group", pgid)
		}
	}
	shells := NewBackgroundShells()
	if _, ok := shells.Adopt("x", ownProcessGroup()); ok {
		t.Fatal("adopted wopr's own process group")
	}
	job, err := shells.Start("sleep 30 & echo ready; sleep 30", t.TempDir(), nil, ShellConfig{Path: "/bin/sh", Args: []string{"-c"}})
	if err != nil {
		t.Fatal(err)
	}
	// Stop only once the shell has forked: before that, killing just the
	// shell would look like killing the group.
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		if out, _ := shells.Tail(job.ID, 1024); out == "ready\n" {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("job never started")
		}
	}
	if job.PGID == ownProcessGroup() || !processGroupAlive(job.PGID) {
		t.Fatalf("job group %d: own %d, alive %v", job.PGID, ownProcessGroup(), processGroupAlive(job.PGID))
	}
	if !shells.Stop(job.ID) {
		t.Fatal("Stop reported the job not running")
	}
	deadline := time.Now().Add(stopGrace + 3*time.Second)
	for processGroupAlive(job.PGID) {
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still alive after Stop", job.PGID)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got, _ := shells.Get(job.ID); got.State != ShellStopped {
		t.Fatalf("state %q, want %q", got.State, ShellStopped)
	}
}
