//go:build windows

package codingagent

import (
	"cmp"
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// editorCommand runs the editor through the command shell: the command and
// its arguments are joined with spaces and run as %ComSpec% /d /s /c "<line>"
// verbatim, so .cmd editors and %VAR% references work as they do at a prompt.
func editorCommand(ctx context.Context, name string, args []string) *exec.Cmd {
	shell := cmp.Or(os.Getenv("ComSpec"), "cmd.exe")
	line := strings.Join(append([]string{name}, args...), " ")
	cmd := exec.CommandContext(ctx, shell)
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: shell + ` /d /s /c "` + line + `"`}
	return cmd
}
