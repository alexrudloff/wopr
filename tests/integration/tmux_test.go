//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

var tmuxSocket = fmt.Sprintf("wopr-test-%d-%s", os.Getpid(), randID())
var tmuxHomeRoot string

// Every command, including cleanup, addresses only this process's server.
// Each new pane starts with isolated home and trust state; tests may supply
// their own fixture homes when launching the program within that pane.
func tmuxCommand(args ...string) *exec.Cmd {
	if len(args) > 0 && args[0] == "new-session" {
		home, err := os.MkdirTemp(tmuxHomeRoot, "pane-home-")
		if err != nil {
			cmd := exec.Command("tmux")
			cmd.Err = err
			return cmd
		}
		args = append([]string{
			"new-session", "-e", "HOME=" + home, "-e", "WOPR_HOME=" + filepath.Join(home, ".wopr"),
			"-e", "WOPR_CODING_AGENT_DIR=", "-e", "WOPR_CODING_AGENT_SESSION_DIR=",
			"-e", "WOPR_CODING_AGENT_DIR=", "-e", "WOPR_CODING_AGENT_SESSION_DIR=",
			"-e", "HERDR_ENV=", "-e", "HERDR_KITTY_GRAPHICS=",
		}, args[1:]...)
	}
	return exec.Command("tmux", append([]string{"-L", tmuxSocket, "-f", "/dev/null"}, args...)...)
}
