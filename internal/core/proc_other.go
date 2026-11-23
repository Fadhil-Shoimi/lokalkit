//go:build !windows

package core

import (
	"os/exec"
	"syscall"
)

// hideWindow puts the child in its own process group so killTree can stop
// it together with any workers it forks (e.g. Apache prefork children).
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func newConsole(cmd *exec.Cmd) {}

func terminalCommand(dir, command string) *exec.Cmd { return exec.Command("sh", "-c", command) }

func killTree(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
