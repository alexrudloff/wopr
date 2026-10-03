package tools

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Background shells: long jobs the bash tool leaves running. A command run
// with run_in_background starts in its own process group with its output in
// a log file, and the call returns at once. A plain command whose process
// group outlives the shell (`cmd &`, `nohup cmd &`) is adopted too, so the
// user sees it and can stop it, though its output isn't captured.

// Background shell states.
const (
	ShellRunning = "running"
	ShellDone    = "done"
	ShellFailed  = "failed"
	ShellStopped = "stopped"
	// ShellExited is an adopted job that ended; its exit code is unknown.
	ShellExited = "exited"
)

// BackgroundShell is one background job's snapshot.
type BackgroundShell struct {
	ID      string
	Command string
	// PGID is the job's process group; Stop signals all of it.
	PGID int
	// LogPath holds the job's output; "" for an adopted job.
	LogPath  string
	State    string
	ExitCode *int
	Started  time.Time
	Finished time.Time
	// Adopted reports a job found still running after a plain command.
	Adopted bool
}

// Running reports whether the job has not ended.
func (b BackgroundShell) Running() bool { return b.State == ShellRunning }

// Elapsed is how long the job ran, or has run so far.
func (b BackgroundShell) Elapsed(now time.Time) time.Duration {
	if !b.Finished.IsZero() {
		return b.Finished.Sub(b.Started)
	}
	return now.Sub(b.Started)
}

// BackgroundShells tracks a session's background jobs.
type BackgroundShells struct {
	mu       sync.Mutex
	entries  []*BackgroundShell
	next     int
	onChange atomic.Pointer[func()]
}

// OnChange sets what runs after a job starts or ends, outside the lock;
// nil removes it.
func (b *BackgroundShells) OnChange(fn func()) {
	if fn == nil {
		b.onChange.Store(nil)
		return
	}
	b.onChange.Store(&fn)
}

// NewBackgroundShells returns an empty registry.
func NewBackgroundShells() *BackgroundShells { return &BackgroundShells{} }

func (b *BackgroundShells) changed() {
	if fn := b.onChange.Load(); fn != nil {
		(*fn)()
	}
}

// add registers a job under the next id.
func (b *BackgroundShells) add(job BackgroundShell) BackgroundShell {
	b.mu.Lock()
	b.next++
	job.ID = fmt.Sprintf("sh_%d", b.next)
	b.entries = append(b.entries, &job)
	b.mu.Unlock()
	b.changed()
	return job
}

// Start runs command in the background through shell: its own process
// group, stdin from /dev/null, stdout and stderr into a new log file.
func (b *BackgroundShells) Start(command, cwd string, env []string, shell ShellConfig) (BackgroundShell, error) {
	logPath := defaultTempFilePath("wopr-bg")
	log, err := os.Create(logPath)
	if err != nil {
		return BackgroundShell{}, err
	}
	args := append([]string{}, shell.Args...)
	if shell.CommandTransport != "stdin" {
		args = append(args, command)
	}
	cmd := exec.Command(shell.Path, args...)
	cmd.Dir = cwd
	cmd.Env = env
	if shell.CommandTransport == "stdin" {
		cmd.Stdin = strings.NewReader(command)
	}
	cmd.Stdout = log
	cmd.Stderr = log
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		_ = os.Remove(logPath)
		return BackgroundShell{}, err
	}
	_ = log.Close()
	job := b.add(BackgroundShell{Command: command, PGID: cmd.Process.Pid, LogPath: logPath, State: ShellRunning, Started: time.Now()})
	go func() {
		_ = cmd.Wait()
		code := shellExitCode(cmd.ProcessState)
		b.mu.Lock()
		if e := b.find(job.ID); e != nil {
			e.ExitCode = &code
			e.Finished = time.Now()
			if e.State == ShellRunning {
				e.State = ShellDone
				if code != 0 {
					e.State = ShellFailed
				}
			}
		}
		b.mu.Unlock()
		b.changed()
	}()
	return job, nil
}

// Adopt tracks a process group a plain command left running, unless it
// is already tracked. ok is false when nothing of the group is alive.
func (b *BackgroundShells) Adopt(command string, pgid int) (BackgroundShell, bool) {
	if !validJobGroup(pgid) || !processGroupAlive(pgid) {
		return BackgroundShell{}, false
	}
	b.mu.Lock()
	for _, e := range b.entries {
		if e.PGID == pgid && e.Running() {
			job := *e
			b.mu.Unlock()
			return job, true
		}
	}
	b.mu.Unlock()
	return b.add(BackgroundShell{Command: command, PGID: pgid, State: ShellRunning, Started: time.Now(), Adopted: true}), true
}

func (b *BackgroundShells) find(id string) *BackgroundShell {
	for _, e := range b.entries {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// refresh marks adopted jobs whose process group is gone as exited, and
// reports whether any changed. Callers hold b.mu.
func (b *BackgroundShells) refresh(now time.Time) bool {
	changed := false
	for _, e := range b.entries {
		if e.Adopted && e.Running() && !processGroupAlive(e.PGID) {
			e.State, e.Finished = ShellExited, now
			changed = true
		}
	}
	return changed
}

// List returns every job, oldest first, after noticing adopted jobs that
// ended.
func (b *BackgroundShells) List() []BackgroundShell {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	changed := b.refresh(time.Now())
	out := make([]BackgroundShell, 0, len(b.entries))
	for _, e := range b.entries {
		out = append(out, *e)
	}
	b.mu.Unlock()
	if changed {
		b.changed()
	}
	return out
}

// Get returns one job.
func (b *BackgroundShells) Get(id string) (BackgroundShell, bool) {
	jobs := b.List()
	i := slices.IndexFunc(jobs, func(j BackgroundShell) bool { return j.ID == id })
	if i < 0 {
		return BackgroundShell{}, false
	}
	return jobs[i], true
}

// stopGrace is how long a stopped job has to exit after SIGTERM before it
// is killed.
const stopGrace = 2 * time.Second

// Stop ends a running job: SIGTERM to its process group, then SIGKILL if it
// is still alive after stopGrace. It reports whether the job was running.
func (b *BackgroundShells) Stop(id string) bool {
	b.mu.Lock()
	e := b.find(id)
	if e == nil || !e.Running() || !validJobGroup(e.PGID) {
		b.mu.Unlock()
		return false
	}
	pgid := e.PGID
	e.State = ShellStopped
	if e.Adopted {
		e.Finished = time.Now()
	}
	b.mu.Unlock()
	_ = terminateProcessGroup(pgid)
	go func() {
		time.Sleep(stopGrace)
		if processGroupAlive(pgid) {
			_ = killProcessGroupID(pgid)
		}
	}()
	b.changed()
	return true
}

// Tail returns about the last maxBytes of a job's log, starting at a line.
func (b *BackgroundShells) Tail(id string, maxBytes int64) (string, error) {
	job, ok := b.Get(id)
	if !ok {
		return "", fmt.Errorf("no background job %s", id)
	}
	if job.LogPath == "" {
		return "", errNoLog
	}
	f, err := os.Open(job.LogPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	offset := max(0, info.Size()-maxBytes)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	text := string(data)
	if offset > 0 {
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return SanitizeBinaryOutput(string(StripANSI([]byte(strings.ReplaceAll(text, "\r", ""))))), nil
}

// errNoLog reports an adopted job, whose output wopr never saw.
var errNoLog = errors.New("output isn't captured: the job was started with & rather than run_in_background")

// validJobGroup reports whether pgid can be a job's own process group:
// never 0 or 1 (signalling those reaches the caller's group or every
// process), and never wopr's own group.
func validJobGroup(pgid int) bool {
	return pgid > 1 && pgid != ownProcessGroup()
}

// setCommand records the command as the model wrote it, without the
// settings prefix the shell ran.
func (b *BackgroundShells) setCommand(id, command string) {
	b.mu.Lock()
	if e := b.find(id); e != nil {
		e.Command = command
	}
	b.mu.Unlock()
}
