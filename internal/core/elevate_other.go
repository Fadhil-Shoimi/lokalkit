//go:build !windows

package core

import "errors"

func runElevated(exe string, args []string) error {
	return errors.New("elevation is only supported on Windows")
}
