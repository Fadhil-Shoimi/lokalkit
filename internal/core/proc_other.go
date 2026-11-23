//go:build !windows

package core

import (
	"os"
	"os/exec"
)

func hideWindow(cmd *exec.Cmd) {}

func newConsole(cmd *exec.Cmd) {}

func killTree(pid int) {
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}
