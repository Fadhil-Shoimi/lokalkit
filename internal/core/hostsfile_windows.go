//go:build windows

package core

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// writeHostsFile rewrites the hosts file in place. It avoids two Windows
// traps that make a plain os.WriteFile fail with "Access is denied" even for
// administrators:
//   - CREATE_ALWAYS (used by os.WriteFile) refuses to open a file that has the
//     Hidden or System attribute;
//   - the Read-only attribute blocks writing.
//
// So: clear the attributes, open the existing file without re-creating it,
// truncate, write, then put the original attributes back.
func writeHostsFile(path string, data []byte) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs, attrErr := windows.GetFileAttributes(p)
	if attrErr == nil {
		const clear = windows.FILE_ATTRIBUTE_READONLY | windows.FILE_ATTRIBUTE_HIDDEN | windows.FILE_ATTRIBUTE_SYSTEM
		if attrs&clear != 0 {
			_ = windows.SetFileAttributes(p, attrs&^clear|windows.FILE_ATTRIBUTE_NORMAL)
			defer windows.SetFileAttributes(p, attrs)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if os.IsNotExist(err) {
		return os.WriteFile(path, data, 0o644)
	}
	if err != nil {
		return err
	}
	if err := f.Truncate(0); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// processElevated reports whether LokalKit runs with admin rights.
func processElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

func describeHostsWriteError(err error) string {
	who := "not elevated"
	if processElevated() {
		who = "running as administrator"
	}
	return fmt.Sprintf("%v (%s). An antivirus \"hosts file protection\" feature may be blocking it", err, who)
}
