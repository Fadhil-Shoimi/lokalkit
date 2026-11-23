package core

import (
	"crypto/x509"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	if err != nil || !reflect.DeepEqual(cfg, DefaultConfig()) || !exists(p) {
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

func TestHostsSyncFlow(t *testing.T) {
	root := t.TempDir()
	hosts := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(hosts, []byte("127.0.0.1 localhost\r\n"), 0o444) // read-only, like some "protected" hosts files
	hostsPathOverride = hosts
	t.Cleanup(func() { hostsPathOverride = "" })

	m, err := NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(m.P.WWW, "lokalkitcodes", "public"), 0o755)
	if got := m.MissingFromHosts(); len(got) != 1 || got[0] != "lokalkitcodes.test" {
		t.Fatalf("missing before sync: %v", got)
	}
	if err := m.SyncHosts(); err != nil {
		t.Fatal(err)
	}
	if got := m.MissingFromHosts(); len(got) != 0 {
		t.Fatalf("still missing: %v", got)
	}
	b, _ := os.ReadFile(hosts)
	if !strings.Contains(string(b), "127.0.0.1 lokalkitcodes.test\r\n") || !strings.HasPrefix(string(b), "127.0.0.1 localhost") {
		t.Errorf("hosts content:\n%q", b)
	}

	// The elevated helper computes the same list from the folder on its own.
	os.MkdirAll(filepath.Join(m.P.WWW, "blog"), 0o755)
	if err := RunElevatedHostsSync(root); err != nil {
		t.Fatal(err)
	}
	if got := m.MissingFromHosts(); len(got) != 0 {
		t.Fatalf("helper did not add: %v", got)
	}
	// Helper errors are written for the main app to show.
	if err := RunElevatedHostsSync(filepath.Join(root, "missing-root")); err == nil {
		os.Remove(filepath.Join(root, "missing-root", "logs", "hosts-helper.log"))
	}
}

func TestWatchProjectsReloads(t *testing.T) {
	root := t.TempDir()
	hostsPathOverride = filepath.Join(t.TempDir(), "hosts")
	t.Cleanup(func() { hostsPathOverride = "" })
	events := make(chan string, 20)
	m, err := NewManager(root, func(ev string, _ any) { events <- ev })
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	defer close(stop)
	go m.WatchProjects(stop)
	time.Sleep(100 * time.Millisecond)
	os.MkdirAll(filepath.Join(m.P.WWW, "newapp"), 0o755)
	deadline := time.After(8 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev == "projects" {
				if got := m.MissingFromHosts(); len(got) != 0 {
					t.Fatalf("watcher did not sync hosts: %v", got)
				}
				if !exists(filepath.Join(m.P.Etc, "apache", "sites-enabled", "auto.newapp.test.conf")) {
					t.Fatal("watcher did not write the vhost")
				}
				return
			}
		case <-deadline:
			t.Fatal("watcher did not notice the new project")
		}
	}
}

func TestLocalhostTLD(t *testing.T) {
	root := t.TempDir()
	hostsPathOverride = filepath.Join(t.TempDir(), "hosts")
	t.Cleanup(func() { hostsPathOverride = "" })
	m, _ := NewManager(root, nil)
	cfg := m.Config()
	cfg.TLD = "localhost"
	if _, err := m.SaveConfig(cfg); err != nil {
		t.Fatalf("localhost TLD should be allowed: %v", err)
	}
	os.MkdirAll(filepath.Join(m.P.WWW, "shop"), 0o755)
	if len(m.MissingFromHosts()) != 0 || len(m.hostNames()) != 0 {
		t.Error(".localhost must not need hosts entries")
	}
	if hs := m.projectHosts(); len(hs) != 1 || hs[0] != "shop.localhost" {
		t.Errorf("project hosts: %v", hs)
	}
	if err := m.SyncHosts(); err != nil || exists(hostsPathOverride) {
		t.Errorf("hosts file should be left alone: %v", err)
	}
	if p := m.Projects()[0]; p.HTTPS != "https://shop.localhost" {
		t.Errorf("url %q", p.HTTPS)
	}
}

func TestFixExtensionDir(t *testing.T) {
	want := "C:/LokalKit/bin/php/php-8.3.37/ext"
	stale := "; ext\r\nextension_dir = \"C:/Users/me/Downloads/LokalKit-windows-amd64/bin/php/php-8.3.37/ext\"\r\nextension=openssl\r\n"
	out, changed := FixExtensionDir(stale, want)
	if !changed || !strings.Contains(out, "extension_dir = \""+want+"\"\r\nextension=openssl") {
		t.Errorf("not fixed: %q", out)
	}
	if _, changed := FixExtensionDir(out, want); changed {
		t.Error("already correct should be untouched")
	}
	if _, changed := FixExtensionDir("extension_dir = \"ext\"\n", want); changed {
		t.Error("relative value should be untouched")
	}
	if _, changed := FixExtensionDir(";extension_dir = \"C:/old/ext\"\n", want); changed {
		t.Error("commented line should be untouched")
	}
	laragon := "extension_dir = \"C:/laragon/bin/php/php-8.3.30-Win32-vs16-x64/ext\"\n"
	if out, changed := FixExtensionDir(laragon, want); !changed || !strings.Contains(out, want) {
		t.Error("Laragon path should be fixed")
	}
}

func TestDBeaverArgs(t *testing.T) {
	args := DBeaverArgs(3307, false)
	if len(args) != 3 || args[1] != "-con" || !strings.Contains(args[2], "port=3307") ||
		!strings.Contains(args[2], "driver=mysql") || !strings.Contains(args[2], "save=true") {
		t.Errorf("create: %q", args)
	}
	reuse := DBeaverArgs(3307, true)
	if !strings.Contains(reuse[2], "create=false") || strings.Contains(reuse[2], "save=true") {
		t.Errorf("reuse: %q", reuse)
	}
	appdata := t.TempDir()
	t.Setenv("APPDATA", appdata)
	if dbeaverHasConnection() {
		t.Fatal("no DBeaver settings yet")
	}
	dir := filepath.Join(appdata, "DBeaverData", "workspace6", "General", ".dbeaver")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "data-sources.json"), []byte(`{"connections":{"lokalkit-mysql":{"name":"LokalKit MySQL"}}}`), 0o644)
	if !dbeaverHasConnection() {
		t.Fatal("saved connection not found")
	}
}

func TestAdoptPHPIni(t *testing.T) {
	m := newTestManager(t)
	for _, v := range []string{"php-8.3.37", "php-8.3.38"} {
		d := filepath.Join(m.P.Bin, "php", v)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, exeName("php")), nil, 0o755)
	}
	old := filepath.Join(m.P.Bin, "php", "php-8.3.37")
	os.WriteFile(filepath.Join(old, "php.ini"), []byte("extension_dir = \""+slash(filepath.Join(old, "ext"))+"\"\nmemory_limit = 2G\n"), 0o644)
	if err := m.AdoptPHPIni("php-8.3.37", "php-8.3.38"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(m.P.Bin, "php", "php-8.3.38", "php.ini"))
	if !strings.Contains(string(b), "memory_limit = 2G") || !strings.Contains(string(b), "php-8.3.38/ext") {
		t.Errorf("new php.ini: %s", b)
	}
}

func TestPHPIniNoDuplicateMysqli(t *testing.T) {
	// Mirrors php.ini-development: an example in the header comments, then the real list.
	src := "; Notes for Windows environments :\n;\n; For example:\n;\n;   extension=mysqli\n;\n[PHP]\n;extension=curl\n;extension=mysqli\n;extension=openssl\n"
	out := BuildPHPIni(src, "C:/php", 1025, func(string) bool { return true })
	if n := strings.Count(out, "\nextension=mysqli"); n != 1 {
		t.Fatalf("mysqli enabled %d times:\n%s", n, out)
	}
	if !strings.Contains(out, ";   extension=mysqli") {
		t.Error("the header example must stay commented")
	}
	broken := "extension=curl\r\nextension=mysqli\r\n;   x\r\nextension=mysqli\r\nextension=php_curl.dll\r\n"
	fixed, n := DedupeExtensions(broken)
	if n != 2 || strings.Count(fixed, "\nextension=mysqli") != 1 || !strings.Contains(fixed, ";extension=mysqli ; duplicate") {
		t.Fatalf("dedupe %d:\n%q", n, fixed)
	}
}

func TestTerminalFiles(t *testing.T) {
	init := BuildTerminalInit([]string{`C:\LokalKit\bin\php\php-8.3.37`}, "8.3.37", "26.13.1", 3306)
	for _, want := range []string{"chcp 65001", `set "PATH=C:\LokalKit\bin\php\php-8.3.37;%PATH%"`, "doskey art=php artisan $*", "PHP 8.3.37", "Node 26.13.1", "prompt $E[38;2;250;204;21m", "\r\n"} {
		if !strings.Contains(init, want) {
			t.Errorf("init missing %q", want)
		}
	}
	frag, err := BuildTerminalFragment(`C:\LokalKit\etc\terminal\lokalkit-init.cmd`, `C:\LokalKit\etc\terminal\lokalkit.ico`, `C:\LokalKit\www`)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Profiles []map[string]any `json:"profiles"`
		Schemes  []map[string]any `json:"schemes"`
	}
	if err := json.Unmarshal(frag, &f); err != nil || len(f.Profiles) != 1 || len(f.Schemes) != 1 {
		t.Fatalf("fragment: %s %v", frag, err)
	}
	p := f.Profiles[0]
	if p["name"] != "LokalKit" || p["colorScheme"] != "LokalKit" || p["guid"] == "" || !strings.Contains(p["commandline"].(string), "lokalkit-init.cmd") {
		t.Errorf("profile: %v", p)
	}
	if f.Schemes[0]["background"] != "#15171C" || f.Schemes[0]["cursorColor"] != "#FACC15" {
		t.Errorf("scheme: %v", f.Schemes[0])
	}
}
