//go:build windows

package core

import (
	"bytes"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// These use the Windows API directly instead of running certutil, taskkill
// or ipconfig. The command-line tools work too, but antivirus heuristics
// treat "certutil -addstore Root" and "taskkill /F" in an unsigned program
// as malware patterns.

func openUserRootStore() (windows.Handle, error) {
	name, _ := windows.UTF16PtrFromString("ROOT")
	return windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM, 0, 0,
		windows.CERT_SYSTEM_STORE_CURRENT_USER, uintptr(unsafe.Pointer(name)))
}

// addUserRootCert adds a certificate to the current user's trusted roots.
// Windows itself shows a confirmation dialog for this; no admin needed.
func addUserRootCert(der []byte) error {
	store, err := openUserRootStore()
	if err != nil {
		return fmt.Errorf("cannot open the certificate store: %w", err)
	}
	defer windows.CertCloseStore(store, 0)
	ctx, err := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, &der[0], uint32(len(der)))
	if err != nil {
		return err
	}
	defer windows.CertFreeCertificateContext(ctx)
	if err := windows.CertAddCertificateContextToStore(store, ctx, windows.CERT_STORE_ADD_REPLACE_EXISTING, nil); err != nil {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return errors.New("the Windows prompt was cancelled")
		}
		return err
	}
	return nil
}

// userRootHasCert reports whether the current user's trusted roots contain der.
func userRootHasCert(der []byte) bool {
	store, err := openUserRootStore()
	if err != nil {
		return false
	}
	defer windows.CertCloseStore(store, 0)
	var ctx *windows.CertContext
	for {
		ctx, err = windows.CertEnumCertificatesInStore(store, ctx)
		if err != nil || ctx == nil {
			return false
		}
		raw := unsafe.Slice(ctx.EncodedCert, ctx.Length)
		if bytes.Equal(raw, der) {
			windows.CertFreeCertificateContext(ctx)
			return true
		}
	}
}

var procDnsFlushResolverCache = windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache")

// flushDNS clears Windows' DNS cache (what "ipconfig /flushdns" does).
func flushDNS() {
	if procDnsFlushResolverCache.Find() == nil {
		procDnsFlushResolverCache.Call()
	}
}

// killTree terminates a process and all its descendants (Apache's worker).
func killTree(pid int) {
	children := map[uint32][]uint32{}
	if snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0); err == nil {
		var e windows.ProcessEntry32
		e.Size = uint32(unsafe.Sizeof(e))
		for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
			children[e.ParentProcessID] = append(children[e.ParentProcessID], e.ProcessID)
		}
		windows.CloseHandle(snap)
	}
	var kill func(id uint32, depth int)
	kill = func(id uint32, depth int) {
		if depth > 16 {
			return
		}
		for _, c := range children[id] {
			if c != id {
				kill(c, depth+1)
			}
		}
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, id); err == nil {
			windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
	kill(uint32(pid), 0)
}
