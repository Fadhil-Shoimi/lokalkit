package core

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// LokalKit makes its own local certificate authority (CA) once, asks Windows
// to trust it, and signs one server certificate covering every project host.
// No external tool (mkcert/openssl) is needed.

const (
	caFile     = "lokalkit-ca.crt"
	caKeyFile  = "lokalkit-ca.key"
	srvFile    = "server.crt"
	srvKeyFile = "server.key"
	// Apple and some browsers reject leaf certificates valid > 825 days even
	// from private CAs; stay under the public 398-day limit to be safe.
	leafValidity = 397 * 24 * time.Hour
	renewBefore  = 30 * 24 * time.Hour
)

type SSLFiles struct {
	Cert string
	Key  string
	Port int
}

type SSLStatus struct {
	Enabled   bool   `json:"enabled"`
	CAExists  bool   `json:"caExists"`
	Trusted   bool   `json:"trusted"`
	Dir       string `json:"dir"`
	Supported bool   `json:"supported"` // trusting is automated on Windows only
}

func (m *Manager) sslDir() string { return filepath.Join(m.P.Etc, "ssl") }

// EnsureCA loads the LokalKit CA, creating it if missing.
func EnsureCA(dir string) (*x509.Certificate, crypto.Signer, bool, error) {
	certPath, keyPath := filepath.Join(dir, caFile), filepath.Join(dir, caKeyFile)
	if exists(certPath) && exists(keyPath) {
		cert, err := readCert(certPath)
		if err == nil {
			key, kerr := readKey(keyPath)
			if kerr == nil && time.Until(cert.NotAfter) > renewBefore {
				return cert, key, false, nil
			}
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, false, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, false, err
	}
	host, _ := os.Hostname()
	user := os.Getenv("USERNAME")
	if user == "" {
		user = os.Getenv("USER")
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject: pkix.Name{
			CommonName:         "LokalKit CA " + strings.TrimSpace(user+"@"+host),
			Organization:       []string{"LokalKit (Codeslab) - development CA"},
			OrganizationalUnit: []string{strings.TrimSpace(user + "@" + host)},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
		SubjectKeyId:          keyID(&key.PublicKey),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, false, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return nil, nil, false, err
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	if err := writePEM(keyPath, "PRIVATE KEY", keyDER, 0o600); err != nil {
		return nil, nil, false, err
	}
	// A new CA invalidates the old server certificate.
	_ = os.Remove(filepath.Join(dir, srvFile))
	cert, _ := x509.ParseCertificate(der)
	return cert, key, true, nil
}

// ServerNames returns the names the server certificate must cover.
func ServerNames(hosts []string) []string {
	names := []string{"localhost"}
	for _, h := range hosts {
		names = append(names, h, "*."+h)
	}
	sort.Strings(names[1:])
	return names
}

// EnsureServerCert (re)creates the server certificate when it is missing,
// expiring, signed by another CA, or does not cover exactly these names.
func EnsureServerCert(dir string, ca *x509.Certificate, caKey crypto.Signer, names []string) (bool, error) {
	certPath, keyPath := filepath.Join(dir, srvFile), filepath.Join(dir, srvKeyFile)
	if cur, err := readCert(certPath); err == nil && exists(keyPath) &&
		time.Until(cur.NotAfter) > renewBefore &&
		cur.CheckSignatureFrom(ca) == nil &&
		sameStrings(cur.DNSNames, names) {
		return false, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: "LokalKit development", Organization: []string{"LokalKit (Codeslab)"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     names,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		SubjectKeyId: keyID(&key.PublicKey),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return false, err
	}
	// Full chain (leaf + CA) so clients that don't have the CA cached still verify.
	chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw})...)
	if err := os.WriteFile(certPath, chain, 0o644); err != nil {
		return false, err
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	return true, writePEM(keyPath, "PRIVATE KEY", keyDER, 0o600)
}

// prepareSSL makes sure certificates exist for the current projects and that
// Apache can serve HTTPS. Errors mean "run without HTTPS this time".
func (m *Manager) prepareSSL(apacheDir string) (*SSLFiles, error) {
	cfg := m.config()
	if !exists(filepath.Join(apacheDir, "modules", "mod_ssl.so")) {
		return nil, errors.New("mod_ssl.so is missing from Apache")
	}
	if !waitPortFree(cfg.SSLPort, 8*time.Second) {
		return nil, fmt.Errorf("port %d is in use (Skype/VMware/IIS?). Change the HTTPS port in Settings", cfg.SSLPort)
	}
	dir := m.sslDir()
	ca, caKey, created, err := EnsureCA(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to create CA: %w", err)
	}
	if created {
		m.Logf("info", "LokalKit CA created: %s", filepath.Join(dir, caFile))
	}
	changed, err := EnsureServerCert(dir, ca, caKey, ServerNames(m.hostNames()))
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate: %w", err)
	}
	if changed {
		m.Logf("info", "HTTPS certificate issued for %d project(s)", len(m.hostNames()))
	}
	if !m.trustAsked {
		m.trustAsked = true
		go m.trustIfNeeded()
	}
	return &SSLFiles{Cert: filepath.Join(dir, srvFile), Key: filepath.Join(dir, srvKeyFile), Port: cfg.SSLPort}, nil
}

func (m *Manager) trustIfNeeded() {
	if runtime.GOOS != "windows" || m.CATrusted() {
		return
	}
	m.Logf("info", "Windows will ask you to trust the LokalKit certificate. Click \"Yes\" so https:// shows no warnings.")
	if err := m.TrustCA(); err != nil {
		m.Logf("error", "%v", err)
	}
}

// TrustCA adds the LokalKit CA to the current user's trusted root store.
// Windows shows its own confirmation dialog; no admin rights are needed.
func (m *Manager) TrustCA() error {
	if runtime.GOOS != "windows" {
		return errors.New("automatic certificate trust is only available on Windows")
	}
	if _, _, _, err := EnsureCA(m.sslDir()); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "certutil", "-user", "-addstore", "Root", filepath.Join(m.sslDir(), caFile))
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("certificate not trusted (cancelled?): %s", lastLines(string(out), 2))
	}
	if !m.CATrusted() {
		return errors.New("certificate is not trusted yet. Try again from Settings → Trust certificate")
	}
	m.Logf("ok", "Windows now trusts the LokalKit certificate. Restart your browser if needed.")
	m.emitServices()
	return nil
}

// CATrusted reports whether Windows trusts the LokalKit CA.
func (m *Manager) CATrusted() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	ca, err := readCert(filepath.Join(m.sslDir(), caFile))
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	thumb := sha1.Sum(ca.Raw)
	cmd := exec.CommandContext(ctx, "certutil", "-user", "-verifystore", "Root", fmt.Sprintf("%x", thumb))
	hideWindow(cmd)
	return cmd.Run() == nil
}

func (m *Manager) SSLStatus() SSLStatus {
	return SSLStatus{
		Enabled:   m.config().SSL,
		CAExists:  exists(filepath.Join(m.sslDir(), caFile)),
		Trusted:   m.CATrusted(),
		Dir:       m.sslDir(),
		Supported: runtime.GOOS == "windows",
	}
}

func readCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("not PEM")
	}
	return x509.ParseCertificate(blk.Bytes)
}

func readKey(path string) (crypto.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	s, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("unsupported key")
	}
	return s, nil
}

func writePEM(path, typ string, der []byte, mode os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der}), mode)
}

func randomSerial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	return n.Add(n, big.NewInt(1))
}

func keyID(pub *ecdsa.PublicKey) []byte {
	b, _ := x509.MarshalPKIXPublicKey(pub)
	h := sha1.Sum(b)
	return h[:]
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
