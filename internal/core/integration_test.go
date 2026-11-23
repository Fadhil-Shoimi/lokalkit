package core

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestApacheIntegration runs the real Apache start path (config, vhosts,
// CA + certificate, PHP) against a Linux Apache, then fetches pages over
// HTTP and HTTPS. Run with: LOKALKIT_IT=1 go test -run Integration ./internal/core
func TestApacheIntegration(t *testing.T) {
	for _, tld := range []string{"test", "localhost"} {
		t.Run(tld, func(t *testing.T) { runApacheIntegration(t, tld) })
	}
}

func runApacheIntegration(t *testing.T, tld string) {
	if os.Getenv("LOKALKIT_IT") == "" || runtime.GOOS != "linux" {
		t.Skip("set LOKALKIT_IT=1 on Linux with apache2 + libapache2-mod-php")
	}
	httpd, err := exec.LookPath("apache2")
	if err != nil {
		t.Skip("apache2 not installed")
	}
	php := firstGlob("/usr/lib/apache2/modules/libphp*.so")
	if php == "" {
		t.Skip("libapache2-mod-php not installed")
	}

	root := t.TempDir()
	os.Chmod(root, 0o755)
	os.Chmod(filepath.Dir(root), 0o755) // let Apache's www-data user read the test folders
	p := NewPaths(root)
	p.Ensure()

	// Lay out bin/ the way LokalKit expects, pointing at the system Apache/PHP.
	apacheDir := filepath.Join(p.Bin, "apache", "httpd-test")
	os.MkdirAll(filepath.Join(apacheDir, "bin"), 0o755)
	os.MkdirAll(filepath.Join(apacheDir, "conf"), 0o755)
	os.Symlink(httpd, filepath.Join(apacheDir, "bin", "httpd"))
	os.Symlink("/usr/lib/apache2/modules", filepath.Join(apacheDir, "modules"))
	os.Symlink("/etc/mime.types", filepath.Join(apacheDir, "conf", "mime.types"))
	phpDir := filepath.Join(p.Bin, "php", "php-8.3.0")
	os.MkdirAll(phpDir, 0o755)
	os.WriteFile(filepath.Join(phpDir, "php"), nil, 0o755)
	os.Symlink(php, filepath.Join(phpDir, "php8apache2_4.dll"))
	os.WriteFile(filepath.Join(phpDir, "php.ini"), []byte("; test\n"), 0o644)
	// Linux-only bits Windows Apache does not need.
	os.WriteFile(filepath.Join(p.Etc, "apache", "custom.conf"), []byte(
		"LoadModule mpm_prefork_module modules/mod_mpm_prefork.so\nUser www-data\nGroup www-data\n"), 0o644)

	os.MkdirAll(filepath.Join(p.WWW, "kedai", "public"), 0o755)
	os.WriteFile(filepath.Join(p.WWW, "kedai", "public", "index.php"), []byte(`<?php echo "kedai ok ", PHP_MAJOR_VERSION, " ", ($_SERVER['HTTPS'] ?? 'off');`), 0o644)

	cfg := DefaultConfig()
	cfg.ApachePort, cfg.SSLPort = freePort(t), freePort(t)
	cfg.TLD = tld
	SaveConfig(filepath.Join(root, "lokalkit.json"), cfg)

	m, err := NewManager(root, func(ev string, d any) {
		if e, ok := d.(LogEntry); ok {
			t.Logf("[%s] %s", e.Level, e.Msg)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.StopAll()
		SyncHostsFile(nil) // remove the test block from /etc/hosts
	})

	if err := m.Start("apache"); err != nil {
		b, _ := os.ReadFile(filepath.Join(p.Logs, "apache_error.log"))
		t.Fatalf("start: %v\n%s", err, b)
	}
	if !strings.Contains(m.Services()[0].Ports, "/") {
		t.Errorf("ports should list HTTPS: %q", m.Services()[0].Ports)
	}

	ca, err := readCert(filepath.Join(p.Etc, "ssl", caFile))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	// Resolve *.test to 127.0.0.1 without relying on /etc/hosts.
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, _ := net.SplitHostPort(addr)
		return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+port)
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		DialContext:     dial,
		TLSClientConfig: &tls.Config{RootCAs: pool}, // strict verification with our CA
	}}

	get := func(url string) string {
		t.Helper()
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	prj := m.Projects()[0]
	if body := get(prj.URL); !strings.HasPrefix(body, "kedai ok 8 off") {
		t.Errorf("http body: %q", body)
	}
	if body := get(prj.HTTPS); !strings.HasPrefix(body, "kedai ok 8 on") {
		t.Errorf("https body: %q", body)
	}
	if body := get(strings.Replace(prj.HTTPS, "kedai.", "api.kedai.", 1)); !strings.HasPrefix(body, "kedai ok") {
		t.Errorf("wildcard subdomain body: %q", body)
	}

	// Adding a project and reloading must re-issue the certificate.
	os.MkdirAll(filepath.Join(p.WWW, "blog"), 0o755)
	os.WriteFile(filepath.Join(p.WWW, "blog", "index.php"), []byte(`<?php echo "blog ok";`), 0o644)
	if err := m.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, pr := range m.Projects() {
		if pr.Name == "blog" {
			if body := get(pr.HTTPS); body != "blog ok" {
				t.Errorf("blog https body: %q", body)
			}
		}
	}
}

func firstGlob(pattern string) string {
	m, _ := filepath.Glob(pattern)
	if len(m) == 0 {
		return ""
	}
	return m[0]
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
