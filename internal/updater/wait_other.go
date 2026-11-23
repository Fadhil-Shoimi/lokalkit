//go:build !windows

package updater

import (
	"os"
	"syscall"
	"time"
)

func WaitForExit(pid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p, err := os.FindProcess(pid)
		if err != nil || p.Signal(syscall.Signal(0)) != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
