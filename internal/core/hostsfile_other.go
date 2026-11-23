//go:build !windows

package core

import (
	"fmt"
	"os"
)

func writeHostsFile(path string, data []byte) error { return os.WriteFile(path, data, 0o644) }

func processElevated() bool { return os.Geteuid() == 0 }

func describeHostsWriteError(err error) string { return fmt.Sprint(err) }
