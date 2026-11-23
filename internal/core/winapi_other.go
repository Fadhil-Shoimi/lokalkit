//go:build !windows

package core

import "errors"

func addUserRootCert(der []byte) error {
	return errors.New("automatic certificate trust is only available on Windows")
}

func userRootHasCert(der []byte) bool { return false }

func flushDNS() {}
