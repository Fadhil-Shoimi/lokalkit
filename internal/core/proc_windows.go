//go:build windows

package core

import (
	"os/exec"
	"syscall"
)

const (
	createNoWindow   = 0x08000000
	createNewConsole = 0x00000010
)

// hideWindow stops a console window flashing up for background processes.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// newConsole gives the process its own visible console window (for terminals).
func newConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole}
}
