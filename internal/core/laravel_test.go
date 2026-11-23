package core

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakePHP installs a shell script posing as php.exe that imitates
// "composer create-project", "composer install" and "artisan key:generate".
func fakePHP(t *testing.T, m *Manager, fail bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	dir := filepath.Join(m.P.Bin, "php", "php-8.3.0")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "php.ini"), []byte(";test\n"), 0o644)
	os.MkdirAll(filepath.Join(m.P.Bin, "composer"), 0o755)
	os.WriteFile(filepath.Join(m.P.Bin, "composer", "composer.phar"), []byte("phar"), 0o644)
	failLine := ""
	if fail {
		failLine = `echo "Your requirements could not be resolved to an installable set of packages."; exit 2`
	}
	script := `#!/bin/sh
case "$1" in
  *composer.phar)
    shift
    ` + failLine + `
    echo "$@" >> ../composer-args.txt
    if [ "$1" = create-project ]; then
      [ -z "$(ls -A .)" ] || { echo "Project directory is not empty"; exit 1; }
      case "$2" in *starter-kit)
        printf '{"name":"%s","extra":{"laravel":{"installer":{"post-create-project":["@php artisan install:features --ansi","npm run something"]}}}}' "$2" > composer.json
        echo '{"private":true}' > package.json ;;
      esac
      echo "Creating a \"laravel/laravel\" project"
      mkdir -p public vendor; echo '<?php' > public/index.php; touch artisan; [ -f composer.json ] || echo '{"name":"laravel/laravel"}' > composer.json
      printf 'APP_NAME=Laravel\nAPP_KEY=\nAPP_URL=http://localhost\n\nDB_CONNECTION=sqlite\n# DB_HOST=127.0.0.1\n# DB_DATABASE=laravel\n\nMAIL_MAILER=log\nMAIL_HOST=127.0.0.1\n' > .env.example
      printf "  - Installing laravel/framework (v12.0.0): Extracting archive\r  - Installing foo/bar (1.0)\n"
      echo '<?php' > vendor/autoload.php
    fi
    if [ "$1" = install ]; then mkdir -p vendor; echo '<?php' > vendor/autoload.php; echo "Generating optimized autoload files"; fi ;;
  artisan)
    if [ "$2" = install:features ]; then echo "$@" > features-installed; fi
    if [ "$2" = key:generate ]; then sed -i 's/^APP_KEY=.*/APP_KEY=base64:dGVzdA==/' .env; echo "Application key set successfully."; fi ;;
esac
`
	os.WriteFile(filepath.Join(dir, "php"), []byte(script), 0o755)
}

func waitTask(t *testing.T, m *Manager, name string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	time.Sleep(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, busy := m.tasks.get(name); !busy {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("task did not finish")
}

func newTestManager(t *testing.T) *Manager {
	hostsPathOverride = filepath.Join(t.TempDir(), "hosts")
	t.Cleanup(func() { hostsPathOverride = "" })
	m, err := NewManager(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCreateLaravelProject(t *testing.T) {
	m := newTestManager(t)
	fakePHP(t, m, false)
	if _, err := m.CreateProject("shop", "laravel"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, m, "shop")
	p := m.findProject("shop")
	if p.Kind != "laravel" || p.NeedsDeps || !strings.HasSuffix(p.DocRoot, "public") {
		t.Fatalf("project: %+v", p)
	}
	env, _ := os.ReadFile(filepath.Join(p.Path, ".env"))
	for _, want := range []string{"APP_KEY=base64:dGVzdA==", "APP_URL=https://shop.test", "MAIL_MAILER=smtp", "MAIL_PORT=1025", "DB_CONNECTION=sqlite"} {
		if !strings.Contains(string(env), want) {
			t.Errorf(".env missing %q:\n%s", want, env)
		}
	}
	logb, _ := os.ReadFile(filepath.Join(m.P.Logs, "composer-shop.log"))
	if !strings.Contains(string(logb), "Installing laravel/framework") {
		t.Errorf("composer log: %s", logb)
	}

	// Repair: an interrupted install (no vendor) is detected and fixed.
	os.RemoveAll(filepath.Join(p.Path, "vendor"))
	if !m.findProject("shop").NeedsDeps {
		t.Fatal("missing vendor should be flagged")
	}
	if err := m.RepairProject("shop"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, m, "shop")
	if m.findProject("shop").NeedsDeps {
		t.Fatal("repair did not install packages")
	}
}

func TestCreateLaravelFailureIsReported(t *testing.T) {
	m := newTestManager(t)
	fakePHP(t, m, true)
	var logs []string
	m.emit = func(ev string, d any) {
		if e, ok := d.(LogEntry); ok && e.Level == "error" {
			logs = append(logs, e.Msg)
		}
	}
	if _, err := m.CreateProject("broken", "laravel"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, m, "broken")
	if len(logs) == 0 || !strings.Contains(strings.Join(logs, "\n"), "could not be resolved") {
		t.Fatalf("failure not reported with Composer's message: %v", logs)
	}
}

func TestSetEnvValues(t *testing.T) {
	in := "APP_URL=http://localhost\r\n# DB_HOST=127.0.0.1\r\nDB_CONNECTION=sqlite\r\n"
	out := SetEnvValues(in, map[string]string{"APP_URL": "https://a.test", "DB_HOST": "127.0.0.1", "DB_PASSWORD": "", "APP_NAME": "My App"})
	for _, want := range []string{"APP_URL=https://a.test\r\n", "DB_HOST=127.0.0.1\r\n", "DB_CONNECTION=sqlite\r\n", "DB_PASSWORD=\r\n", "APP_NAME=\"My App\"\r\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "# DB_HOST") {
		t.Error("commented key should be replaced")
	}
}

func TestRetryAfterFailedCreate(t *testing.T) {
	m := newTestManager(t)
	fakePHP(t, m, true)
	m.CreateProject("again", "laravel")
	waitTask(t, m, "again")
	p := m.findProject("again")
	if p.Error == "" {
		t.Fatal("failure should be shown on the project")
	}
	fakePHP(t, m, false) // e.g. extensions fixed
	if err := m.RepairProject("again"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, m, "again")
	p = m.findProject("again")
	if p.Error != "" || p.NeedsDeps || p.Kind != "laravel" {
		t.Fatalf("retry should create the project from scratch: %+v", p)
	}
}

func fakeNode(t *testing.T, m *Manager) {
	dir := filepath.Join(m.P.Bin, "nodejs", "node-v26.13.1-win-x64", "node-v26.13.1-win-x64")
	os.MkdirAll(filepath.Join(dir, "node_modules", "npm", "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "node_modules", "npm", "bin", "npm-cli.js"), []byte("//"), 0o644)
	os.WriteFile(filepath.Join(dir, "node"), []byte(`#!/bin/sh
shift # npm-cli.js
echo "npm $@" >> npm-calls.txt
if [ "$1" = install ]; then mkdir -p node_modules; echo "added 300 packages"; fi
if [ "$1" = run ] && [ "$2" = build ]; then mkdir -p public/build; echo '{}' > public/build/manifest.json; echo "vite build done"; fi
`), 0o755)
}

func TestCreateReactStarterKit(t *testing.T) {
	m := newTestManager(t)
	fakePHP(t, m, false)

	// Without Node.js: created, but the project says the front-end needs it.
	m.CreateProject("app", "laravel-react")
	waitTask(t, m, "app")
	p := m.findProject("app")
	if !p.NeedsAssets || !strings.Contains(p.Error, "Node.js") {
		t.Fatalf("expected a Node.js hint: %+v", p)
	}
	args, _ := os.ReadFile(filepath.Join(m.P.WWW, "composer-args.txt"))
	if !strings.Contains(string(args), "create-project laravel/react-starter-kit . dev-main") {
		t.Errorf("starter kit should come from dev-main: %s", args)
	}
	feat, _ := os.ReadFile(filepath.Join(p.Path, "features-installed"))
	if !strings.Contains(string(feat), "install:features --ansi --no-interaction") {
		t.Errorf("installer step not run: %q", feat)
	}

	// Install Node.js, click Retry: npm install + build run, warnings clear.
	fakeNode(t, m)
	if err := m.RepairProject("app"); err != nil {
		t.Fatal(err)
	}
	waitTask(t, m, "app")
	p = m.findProject("app")
	if p.NeedsAssets || p.Error != "" {
		t.Fatalf("assets should be built: %+v", p)
	}
	calls, _ := os.ReadFile(filepath.Join(p.Path, "npm-calls.txt"))
	if !strings.Contains(string(calls), "npm install") || !strings.Contains(string(calls), "npm run build") {
		t.Errorf("npm calls: %s", calls)
	}
}

func TestPlainLaravelDoesNotRequireNode(t *testing.T) {
	m := newTestManager(t)
	fakePHP(t, m, false)
	m.CreateProject("plain", "laravel")
	waitTask(t, m, "plain")
	if p := m.findProject("plain"); p.Error != "" || p.NeedsAssets {
		t.Fatalf("plain Laravel should be fine without Node.js: %+v", p)
	}
}
