package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// fakeAce is a tiny AdonisJS stand-in: "node ace serve" starts a real HTTP
// server on PORT that echoes the Host and X-Forwarded-Proto it received.
const fakeAce = `
const fs = require('fs');
const cmd = process.argv[2];
fs.appendFileSync('ace-calls.txt', process.argv.slice(2).join(' ') + '\n');
if (cmd === 'serve') {
  const port = +process.env.PORT;
  require('http').createServer((req, res) => {
    res.end('adonis ok host=' + req.headers.host + ' proto=' + (req.headers['x-forwarded-proto'] || ''));
  }).listen(port, '127.0.0.1', () => console.log('Server started on port ' + port));
} else {
  console.log('ace ' + cmd + ' done');
}
`

// fakeNpm imitates "npm exec -- create-adonisjs@latest . --kit=..." and "npm install".
const fakeNpm = `
const fs = require('fs');
const args = process.argv.slice(2);
fs.appendFileSync('../npm-calls.txt', args.join(' ') + '\n');
if (args.includes('create-adonisjs@latest')) {
  if (fs.readdirSync('.').length) { console.error('Directory is not empty'); process.exit(1); }
  fs.writeFileSync('ace.js', ACE);
  fs.writeFileSync('package.json', '{"name":"app","private":true}');
  fs.mkdirSync('node_modules');
  fs.writeFileSync('.env', 'TZ=UTC\nPORT=3333\nHOST=localhost\nLOG_LEVEL=info\nAPP_KEY=abc\nNODE_ENV=development\n');
  console.log('Installing dependencies...');
}
if (args[0] === 'install') fs.mkdirSync('node_modules', { recursive: true });
`

func setupFakeNode(t *testing.T, m *Manager) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses symlinks")
	}
	realNode, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	dir := filepath.Join(m.P.Bin, "nodejs", "node-v26-test")
	os.MkdirAll(filepath.Join(dir, "node_modules", "npm", "bin"), 0o755)
	os.Symlink(realNode, filepath.Join(dir, "node"))
	npm := strings.Replace(fakeNpm, "ACE", strconv.Quote(fakeAce), 1)
	os.WriteFile(filepath.Join(dir, "node_modules", "npm", "bin", "npm-cli.js"), []byte(npm), 0o644)
}

func TestBuildProxyVhost(t *testing.T) {
	p := Project{Name: "api", Host: "api.localhost", ProxyPort: 3335}
	plain := BuildProxyVhost(p, 80, nil)
	for _, want := range []string{"<VirtualHost *:80>", "ServerName api.localhost", "ProxyPreserveHost On",
		"ProxyPass / http://127.0.0.1:3335/ upgrade=websocket", "X-Forwarded-Proto \"http\"", "ErrorDocument 503"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q:\n%s", want, plain)
		}
	}
	tls := BuildProxyVhost(p, 80, &SSLFiles{Cert: "/c.crt", Key: "/c.key", Port: 443})
	if !strings.Contains(tls, "<VirtualHost *:443>") || !strings.Contains(tls, "SSLEngine on") || !strings.Contains(tls, "X-Forwarded-Proto \"https\"") {
		t.Errorf("ssl vhost:\n%s", tls)
	}
}

func TestCreateAdonisProject(t *testing.T) {
	m := newTestManager(t)
	setupFakeNode(t, m)
	t.Cleanup(m.stopAllDev)

	// Another AdonisJS project already uses the default port.
	other := filepath.Join(m.P.WWW, "first")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "ace.js"), nil, 0o644)
	os.WriteFile(filepath.Join(other, ".env"), []byte("PORT=3333\n"), 0o644)

	if _, err := m.CreateProject("shop", "adonis-react", DBChoice{Mode: "sqlite"}); err != nil {
		t.Fatal(err)
	}
	waitTask(t, m, "shop")
	p := m.findProject("shop")
	if p.Kind != "adonis" || p.Error != "" || p.NeedsDeps {
		t.Fatalf("project: %+v", p)
	}
	if p.ProxyPort == 3333 || p.ProxyPort == 0 {
		t.Errorf("should get its own port, got %d", p.ProxyPort)
	}
	calls, _ := os.ReadFile(filepath.Join(m.P.WWW, "npm-calls.txt"))
	if !strings.Contains(string(calls), "create-adonisjs@latest . --kit=react --pkg=npm") {
		t.Errorf("create call: %s", calls)
	}
	env, _ := os.ReadFile(filepath.Join(p.Path, ".env"))
	if !strings.Contains(string(env), "HOST=127.0.0.1") || !strings.Contains(string(env), "APP_URL=https://shop.test") {
		t.Errorf(".env:\n%s", env)
	}
	ace, _ := os.ReadFile(filepath.Join(p.Path, "ace-calls.txt"))
	if !strings.Contains(string(ace), "migration:run --force") || !strings.Contains(string(ace), "serve --hmr") {
		t.Errorf("ace calls: %s", ace)
	}
	// The dev server was started after creation and answers on its port.
	if !m.DevRunning("shop") || PortFree(p.ProxyPort) {
		t.Fatal("dev server should be running")
	}
	conf, _ := os.ReadFile(filepath.Join(m.P.Etc, "apache", "sites-enabled", "auto.shop.test.conf"))
	if !strings.Contains(string(conf), "ProxyPass / http://127.0.0.1:"+strconv.Itoa(p.ProxyPort)+"/") {
		t.Errorf("proxy vhost:\n%s", conf)
	}
	if err := m.StopDev("shop"); err != nil || m.DevRunning("shop") {
		t.Fatalf("stop: %v", err)
	}
}

func TestCreateAdonisWithMySQLConfigures(t *testing.T) {
	m := newTestManager(t)
	setupFakeNode(t, m)
	t.Cleanup(m.stopAllDev)
	// MySQL isn't installed here, so the MySQL choice must fail up front.
	if _, err := m.CreateProject("api", "adonis-hypermedia", DBChoice{Mode: "mysql"}); err == nil {
		t.Fatal("expected MySQL error")
	}
	// Without a database the kit is created with --skip-migrations.
	if _, err := m.CreateProject("nodb", "adonis-vue", DBChoice{Mode: "none"}); err != nil {
		t.Fatal(err)
	}
	waitTask(t, m, "nodb")
	calls, _ := os.ReadFile(filepath.Join(m.P.WWW, "npm-calls.txt"))
	if !strings.Contains(string(calls), "--kit=vue --pkg=npm --skip-migrations") {
		t.Errorf("calls: %s", calls)
	}
	ace, _ := os.ReadFile(filepath.Join(m.P.WWW, "nodb", "ace-calls.txt"))
	if strings.Contains(string(ace), "migration:run") {
		t.Error("no migrations without a database")
	}
}

func TestAdonisNeedsNode(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.CreateProject("x", "adonis-react", DBChoice{}); err == nil || !strings.Contains(err.Error(), "Node.js") {
		t.Fatalf("got %v", err)
	}
}
