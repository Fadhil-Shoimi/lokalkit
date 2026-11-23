//go:build windows

package updater

import (
	"time"

	"golang.org/x/sys/windows"
)

// WaitForExit waits until process pid has exited (the old LokalKit after an
// update), so the new one doesn't hit the single-instance lock.
func WaitForExit(pid int, timeout time.Duration) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	windows.WaitForSingleObject(h, uint32(timeout.Milliseconds()))
}
