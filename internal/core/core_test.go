package core

import (
	"crypto/x509"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestConfigValidate(t *testing.T) {
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	c := DefaultConfig()
	c.MySQLPort = c.ApachePort
	if c.Validate() == nil {
		t.Error("duplicate ports should fail")
	}
	c = DefaultConfig()
	c.TLD = "dev"
	if c.Validate() == nil {
		t.Error(".dev should be rejected")
	}
	c.TLD = "Test"
	if c.Validate() == nil {
		t.Error("uppercase TLD should be rejected")
	}
}

func TestLoadConfigCreatesDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lokalkit.json")
	cfg, err := LoadConfig(p)
	if err != nil || cfg != DefaultConfig() || !exists(p) {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestHostLabel(t *testing.T) {
	cases := map[string]string{
		"kedai": "kedai", "My_App": "my-app", "projek baru": "projek-baru",
		"-bad": "", "a.b": "a-b", "ümlaut": "",
	}
	for in, want := range cases {
		if got := HostLabel(in); got != want {
			t.Errorf("HostLabel(%q)=%q want %q", in, got, want)
		}
	}
}

func TestVersionSorting(t *testing.T) {
	dirs := t.TempDir()
	for _, d := range []string{"php-8.1.2", "php-8.10.0", "php-8.3.30"} {
		os.MkdirAll(filepath.Join(dirs, d), 0o755)
	}
	got := subDirs(dirs)
	if strings.Join(got, ",") != "php-8.10.0,php-8.3.30,php-8.1.2" {
		t.Errorf("order: %v", got)
	}
}

func TestFindUnder(t *testing.T) {
	base := t.TempDir()
	bin := filepath.Join(base, "httpd-2.4.66", "Apache24", "bin", "httpd.exe")
	os.MkdirAll(filepath.Dir(bin), 0o755)
	os.WriteFile(bin, nil, 0o644)
	if got := findUnder(base, filepath.Join("bin", "httpd.exe")); got != bin {
		t.Errorf("got %q", got)
	}
}

func TestBuildHostsFile(t *testing.T) {
	orig := "127.0.0.1 localhost\r\n::1 localhost\r\n"
	out := BuildHostsFile(orig, []string{"a.test", "b.test"})
	if !strings.Contains(out, "\r\n127.0.0.1 a.test\r\n127.0.0.1 b.test\r\n") {
		t.Errorf("missing entries / CRLF:\n%q", out)
	}
	// Idempotent, and the block is replaced rather than duplicated.
	if again := BuildHostsFile(out, []string{"a.test", "b.test"}); again != out {
		t.Errorf("not idempotent:\n%q\n%q", out, again)
	}
	out2 := BuildHostsFile(out, []string{"c.test"})
	if strings.Contains(out2, "a.test") || strings.Count(out2, hostsBegin) != 1 {
		t.Errorf("block not replaced:\n%q", out2)
	}
	// Removing all hosts restores the original file.
	if back := BuildHostsFile(out2, nil); back != orig {
		t.Errorf("not restored:\n%q", back)
	}
}

func TestScanProjectsAndVhost(t *testing.T) {
	www := t.TempDir()
	os.MkdirAll(filepath.Join(www, "kedai", "public"), 0o755)
	os.WriteFile(filepath.Join(www, "kedai", "artisan"), nil, 0o644)
	os.MkdirAll(filepath.Join(www, "blog"), 0o755)
	os.MkdirAll(filepath.Join(www, ".git"), 0o755)
	cfg := DefaultConfig()
	ps := ScanProjects(www, cfg)
	if len(ps) != 2 {
		t.Fatalf("got %d projects: %+v", len(ps), ps)
	}
	var kedai Project
	for _, p := range ps {
		if p.Name == "kedai" {
			kedai = p
		}
	}
	if kedai.Kind != "laravel" || !strings.HasSuffix(kedai.DocRoot, "public") || kedai.URL != "http://kedai.test" {
		t.Errorf("bad project %+v", kedai)
	}
	v := BuildVhost(kedai, 80)
	if !strings.Contains(v, "ServerName kedai.test") || !strings.Contains(v, "<VirtualHost *:80>") {
		t.Error(v)
	}
	cfg.ApachePort = 8080
	if ScanProjects(www, cfg)[0].URL != "http://blog.test:8080" {
		t.Error("port missing from URL")
	}
}

func TestBuildPHPIni(t *testing.T) {
	src := "; comment\n;extension_dir = \"ext\"\n;extension=curl\n;extension=mbstring\n;extension=zip\nmemory_limit = 128M\n;date.timezone =\nSMTP = localhost\nsmtp_port = 25\n"
	out := BuildPHPIni(src, `C:\lk\bin\php\php-8.3`, 1025, func(e string) bool { return e != "zip" })
	for _, want := range []string{
		"extension_dir = \"C:/lk/bin/php/php-8.3/ext\"", "\nextension=curl\n", "\nextension=mbstring\n",
		";extension=zip", "memory_limit = 512M", "date.timezone = Asia/Kuala_Lumpur",
		"SMTP = 127.0.0.1", "smtp_port = 1025", "\nextension=pdo_mysql\n",
	} {
		if !strings.Contains(strings.ReplaceAll(out, `\`, "/"), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestBuildHttpdConf(t *testing.T) {
	p := NewPaths(`C:\LokalKit`)
	conf := BuildHttpdConf(HttpdParams{
		ApacheDir: `C:\LokalKit\bin\apache\httpd-2.4.66\Apache24`, Paths: p, Port: 80,
		PHP:     PHPInstall{Version: "8.3.30", Dir: `C:\LokalKit\bin\php\php-8.3.30`, ApacheDLL: `C:\LokalKit\bin\php\php-8.3.30\php8apache2_4.dll`, Module: "php_module"},
		Modules: []string{"rewrite", "dir"}, Windows: true,
	})
	for _, want := range []string{"Listen 80", "LoadModule rewrite_module modules/mod_rewrite.so", "LoadModule php_module", "AcceptFilter http none", "sites-enabled/*.conf", `%h %l`} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestEnvWithPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	env := envWithPath([]string{"/opt/php"}, "PHPRC=/opt/php")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "PATH=/opt/php"+string(os.PathListSeparator)+"/usr/bin") || !strings.Contains(joined, "PHPRC=/opt/php") {
		t.Error(joined)
	}
}

// TestProcLifecycle starts a real child process listening on a port,
// waits for it, then stops it.
func TestProcLifecycle(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	exits := make(chan bool, 1)
	p := NewProc("test", func(_ string, expected bool, _ error) { exits <- expected })
	log := filepath.Join(t.TempDir(), "p.log")
	if err := p.Start(py, []string{"-m", "http.server", "--bind", "127.0.0.1", strconv.Itoa(port)}, t.TempDir(), os.Environ(), log); err != nil {
		t.Fatal(err)
	}
	if err := waitPort(port, p.Exited(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if PortFree(port) {
		t.Error("port should be busy")
	}
	if err := p.Stop(nil, 0); err != nil {
		t.Fatal(err)
	}
	if p.Running() || !<-exits {
		t.Error("should be stopped with expected=true")
	}
}

func TestCertificates(t *testing.T) {
	dir := t.TempDir()
	ca, key, created, err := EnsureCA(dir)
	if err != nil || !created || !ca.IsCA {
		t.Fatalf("create CA: %v %v", created, err)
	}
	ca2, _, created2, err := EnsureCA(dir)
	if err != nil || created2 || !ca2.Equal(ca) {
		t.Fatal("CA should be reused")
	}
	names := ServerNames([]string{"kedai.test", "blog.test"})
	if names[0] != "localhost" || len(names) != 5 {
		t.Fatalf("names %v", names)
	}
	if changed, err := EnsureServerCert(dir, ca, key, names); err != nil || !changed {
		t.Fatalf("first cert: %v %v", changed, err)
	}
	if changed, _ := EnsureServerCert(dir, ca, key, names); changed {
		t.Error("same names should not re-issue")
	}
	if changed, _ := EnsureServerCert(dir, ca, key, ServerNames([]string{"kedai.test"})); !changed {
		t.Error("new names should re-issue")
	}
	leaf, err := readCert(filepath.Join(dir, srvFile))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for _, host := range []string{"kedai.test", "api.kedai.test", "localhost"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Roots: pool}); err != nil {
			t.Errorf("verify %s: %v", host, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "blog.test", Roots: pool}); err == nil {
		t.Error("blog.test should no longer be covered")
	}
	if leaf.NotAfter.Sub(leaf.NotBefore) > 398*24*time.Hour {
		t.Error("leaf validity too long for browsers")
	}
}

func TestStartAllNothingInstalled(t *testing.T) {
	m, err := NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.StartAll(); err != ErrNothingInstalled {
		t.Fatalf("got %v", err)
	}
}
