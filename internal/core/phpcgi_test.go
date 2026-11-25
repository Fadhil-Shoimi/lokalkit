package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPHPPoolPortsAndNames(t *testing.T) {
	if got := PHPPoolPorts("8.4.15"); got[0] != 9840 || got[len(got)-1] != 9840+phpWorkers-1 {
		t.Errorf("ports 8.4: %v", got)
	}
	if got := PHPPoolPorts("7.4.33"); got[0] != 9740 {
		t.Errorf("ports 7.4: %v", got)
	}
	if b := phpBalancer("8.3.30"); b != "lokalkit-php83" {
		t.Errorf("balancer: %s", b)
	}
	if PHPBranch("8.6.0") != "8.6" || PHPBranch("8") != "8" {
		t.Error("PHPBranch")
	}
}

func TestPHPHandlerInVhost(t *testing.T) {
	php := PHPInstall{Version: "8.3.30"}
	p := Project{Name: "qistas", Host: "qistas.test", DocRoot: `C:\www\qistas\public`}
	conf := BuildVhost(p, 80) + "\n" + BuildSSLVhost(p, SSLFiles{Port: 443, Cert: "c", Key: "k"})
	got := withPHPHandler(conf, php, true)
	if n := strings.Count(got, `SetHandler "proxy:balancer://lokalkit-php83"`); n != 2 {
		t.Errorf("want handler in both vhosts, got %d:\n%s", n, got)
	}
	if strings.Count(got, "CGIPassAuth On") != 2 || strings.Count(got, "ProxyFCGIBackendType GENERIC") != 2 {
		t.Errorf("missing CGIPassAuth/backend type:\n%s", got)
	}
	if direct := withPHPHandler(conf, php, false); !strings.Contains(direct, `proxy:fcgi://127.0.0.1:9830`) {
		t.Errorf("no-balancer handler:\n%s", direct)
	}
	pool := BuildPHPPoolConf([]PHPInstall{php}, true)
	for _, want := range []string{`<Proxy "balancer://lokalkit-php83">`, `BalancerMember "fcgi://127.0.0.1:9830"`, `BalancerMember "fcgi://127.0.0.1:9833"`} {
		if !strings.Contains(pool, want) {
			t.Errorf("pool conf missing %q:\n%s", want, pool)
		}
	}
	if BuildPHPPoolConf(nil, true) != "" {
		t.Error("no pools should give no conf")
	}
}

func TestProjectPHPChoice(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, cgi bool) {
		d := filepath.Join(root, "bin", "php", name)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, exeName("php")), nil, 0o755)
		if cgi {
			os.WriteFile(filepath.Join(d, exeName("php-cgi")), nil, 0o755)
		}
	}
	mk("php-8.6.0", true)
	mk("php-8.3.20", true)
	mk("php-8.3.30", true)
	mk("php-8.2.10", false) // no php-cgi: cannot be pinned
	os.MkdirAll(filepath.Join(root, "www", "qistas"), 0o755)
	os.MkdirAll(filepath.Join(root, "www", "shop"), 0o755)
	m, err := NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.Config()
	cfg.ProjectPHP = map[string]string{"qistas": "8.3", "shop": "8.2"}
	m.SaveConfig(cfg)

	php, override, missing, _ := m.ProjectPHPChoice("qistas")
	if !override || missing || php.Version != "8.3.30" {
		t.Errorf("qistas: %v %v %v", php.Version, override, missing)
	}
	if php, override, missing, _ := m.ProjectPHPChoice("shop"); override || !missing || php.Version != "8.6.0" {
		t.Errorf("shop should fall back to default: %v %v %v", php.Version, override, missing)
	}
	if _, override, _, _ := m.ProjectPHPChoice("other"); override {
		t.Error("unpinned project should use the default")
	}
	if got, _ := m.phpForDir(filepath.Join(root, "www", "qistas", "app")); got.Version != "8.3.30" {
		t.Errorf("phpForDir inside qistas: %s", got.Version)
	}
	if got, _ := m.phpForDir(filepath.Join(root, "www")); got.Version != "8.6.0" {
		t.Errorf("phpForDir www: %s", got.Version)
	}
	if pins := m.pinnedPHP(m.Projects()); len(pins) != 1 || pins[0].Version != "8.3.30" {
		t.Errorf("pinned: %+v", pins)
	}
	for _, p := range m.Projects() {
		if p.Name == "shop" && (!p.PHPMissing || p.PHP != "8.2") {
			t.Errorf("shop annotation: %+v", p)
		}
	}
	// Pinning to the default branch removes the pin; unknown branch is refused.
	if err := m.SetProjectPHP("qistas", "8.6"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Config().ProjectPHP["qistas"]; ok {
		t.Error("pin to default branch should be dropped")
	}
	if err := m.SetProjectPHP("qistas", "7.4"); err == nil {
		t.Error("pin to missing PHP should fail")
	}
}
