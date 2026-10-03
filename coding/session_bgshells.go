package coding

import (
	"fmt"
	"strings"
	"time"

	"github.com/alexrudloff/wopr/agent"
	"github.com/alexrudloff/wopr/internal/codingagent/efficiency"
	"github.com/alexrudloff/wopr/internal/codingagent/tools"
)

// Background shells: the session's bash tools share one registry of the jobs
// they leave running, which the TUI lists below the prompt.

// withBackgroundShells points every bash tool in list at registry.
func withBackgroundShells(list []agent.AgentTool, registry *tools.BackgroundShells) {
	for _, tool := range list {
		if bash, ok := tool.(*tools.BashTool); ok {
			bash.Background = registry
		}
	}
}

// BackgroundShells returns the session's background jobs.
func (s *Session) BackgroundShells() *tools.BackgroundShells { return s.bgShells }

// bgExitTail is how much of a finished job's log its exit note carries.
const bgExitTail = 2000

// backgroundShellEnded tells the model a background job ended, the way a
// background task's result arrives, so it never has to sleep-poll the job:
// interactively it wakes an idle session or follows the current turn;
// without a frontend it waits for the run's next turn. A job the user
// stopped is noted beside the next prompt instead, so stopping one never
// starts a run.
func (s *Session) backgroundShellEnded(job tools.BackgroundShell) {
	command := job.Command
	if len(command) > 120 {
		command = command[:117] + "..."
	}
	elapsed := job.Elapsed(time.Now()).Round(time.Second)
	var head string
	switch {
	case job.State == tools.ShellStopped:
		head = fmt.Sprintf("Background job %s (`%s`) was stopped by the user after %s.", job.ID, command, elapsed)
	case job.Adopted:
		head = fmt.Sprintf("Background job %s (`%s`) ended after %s. It was started with & or nohup, so its exit code and output weren't captured; check the files it writes.", job.ID, command, elapsed)
	default:
		code := 0
		if job.ExitCode != nil {
			code = *job.ExitCode
		}
		head = fmt.Sprintf("Background job %s (`%s`) exited %d after %s. Full output: %s", job.ID, command, code, elapsed, job.LogPath)
	}
	content := head
	if !job.Adopted && job.State != tools.ShellStopped {
		if tail, err := s.bgShells.Tail(job.ID, bgExitTail); err == nil && strings.TrimSpace(tail) != "" {
			content += "\nLast output:\n" + strings.TrimRight(tail, "\n")
		}
	}
	label := fmt.Sprintf("Background job %s %s after %s", job.ID, jobOutcome(job), elapsed)
	msg := agent.AgentMessage{Custom: map[string]any{
		"role": agent.RoleCustom, "customType": efficiency.BackgroundExitMessageType,
		"content": content, "label": label, "display": false,
		"timestamp": time.Now().UnixMilli(),
	}}
	if job.State == tools.ShellStopped {
		s.agent.QueueNextTurn(msg)
		return
	}
	if deliver := s.tasks.deliver.Load(); deliver != nil {
		(*deliver)(msg)
		return
	}
	s.tasks.headless.push(msg)
}

// jobOutcome is a job's end in a few words.
func jobOutcome(job tools.BackgroundShell) string {
	switch {
	case job.State == tools.ShellStopped:
		return "stopped"
	case job.Adopted:
		return "ended"
	case job.ExitCode != nil:
		return fmt.Sprintf("exited %d", *job.ExitCode)
	}
	return "ended"
}
