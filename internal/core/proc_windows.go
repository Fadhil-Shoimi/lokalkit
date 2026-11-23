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

// newConsole gives the process its own visible console window.
func newConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewConsole}
}

// terminalCommand opens a visible, interactive cmd.exe in dir.
//
// LokalKit is a GUI app without a console, and os/exec then wires a child's
// stdin/stdout to NUL: a cmd.exe started directly reads end-of-file at once
// and closes before you can see it. "start" creates a fresh console with
// its own input/output, which is what a terminal needs. The outer cmd.exe
// that runs "start" is hidden.
func terminalCommand(dir, command string) *exec.Cmd {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
		CmdLine:       `cmd.exe /C start "LokalKit Terminal" /D "` + dir + `" cmd.exe /K "` + command + `"`,
	}
	return cmd
}
