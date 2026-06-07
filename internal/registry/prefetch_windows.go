//go:build windows

package registry

import (
	"os/exec"
	"syscall"
)

// Windows process-creation flags (not exported by the syscall package in all Go
// versions). DETACHED_PROCESS detaches the child from the parent's console;
// CREATE_NEW_PROCESS_GROUP keeps a Ctrl-C in the parent console from reaching it.
const (
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

// detachProcess starts the child detached from the parent console and process
// group so it survives the foreground CLI exiting.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
	}
}
