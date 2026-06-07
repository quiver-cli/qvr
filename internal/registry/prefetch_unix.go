//go:build !windows

package registry

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the child in its own session (setsid) so it survives the
// parent CLI exiting and isn't in the parent's process group — a Ctrl-C in the
// foreground shell won't kill the background refresh.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
