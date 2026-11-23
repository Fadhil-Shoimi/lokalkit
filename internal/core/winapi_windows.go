//go:build windows

package core

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
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

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procIsIconic            = user32.NewProc("IsIconic")
)

// runningPIDs returns the process IDs of running programs named like exe
// (e.g. dbeaver.exe).
func runningPIDs(exe string) []uint32 {
	name := strings.ToLower(filepath.Base(exe))
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var pids []uint32
	for err := windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.ToLower(windows.UTF16ToString(e.ExeFile[:])) == name {
			pids = append(pids, e.ProcessID)
		}
	}
	return pids
}

// focusProcess brings a visible window of one of pids to the front,
// restoring it if minimised. Returns false when none was found.
func focusProcess(pids []uint32) bool {
	want := map[uint32]bool{}
	for _, p := range pids {
		want[p] = true
	}
	var found windows.HWND
	cb := syscall.NewCallback(func(h windows.HWND, _ uintptr) uintptr {
		var pid uint32
		windows.GetWindowThreadProcessId(h, &pid)
		if want[pid] && windows.IsWindowVisible(h) {
			found = h
			return 0 // stop
		}
		return 1
	})
	_ = windows.EnumWindows(cb, nil)
	if found == 0 {
		return false
	}
	const swRestore = 9
	if r, _, _ := procIsIconic.Call(uintptr(found)); r != 0 {
		procShowWindow.Call(uintptr(found), swRestore)
	}
	procSetForegroundWindow.Call(uintptr(found))
	return true
}
