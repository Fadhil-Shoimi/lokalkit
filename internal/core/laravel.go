package core

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Composer/Laravel work runs inside LokalKit (not in a separate console), so
// the app knows when it finishes or fails and can finish the setup itself.

type projectTasks struct {
	kits   map[string]string // project name -> kit it was created with
	mu     sync.Mutex
	state  map[string]string // project name -> status text while busy
	errors map[string]string // project name -> last setup error
}

func (t *projectTasks) setError(name, msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.errors == nil {
		t.errors = map[string]string{}
	}
	if msg == "" {
		delete(t.errors, name)
	} else {
		t.errors[name] = msg
	}
}

func (t *projectTasks) rememberKit(name, kit string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.kits == nil {
		t.kits = map[string]string{}
	}
	t.kits[name] = kit
}

// kitFor returns the kit a project was created with (default: plain Laravel).
func (t *projectTasks) kitFor(name string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if k := t.kits[name]; k != "" {
		return k
	}
	return "laravel"
}

func (t *projectTasks) getError(name string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.errors[name]
}

func (t *projectTasks) set(name, status string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == nil {
		t.state = map[string]string{}
	}
	if status == "" {
		delete(t.state, name)
	} else {
		t.state[name] = status
	}
}

func (t *projectTasks) get(name string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.state[name]
	return s, ok
}

// annotate adds live task status and "dependencies missing" flags.
func (m *Manager) annotate(ps []Project) []Project {
	for i := range ps {
		ps[i].Task, _ = m.tasks.get(ps[i].Name)
		ps[i].Error = m.tasks.getError(ps[i].Name)
		ps[i].NeedsAssets = needsAssets(ps[i].Path)
		ps[i].NeedsDeps = exists(filepath.Join(ps[i].Path, "composer.json")) &&
			!exists(filepath.Join(ps[i].Path, "vendor", "autoload.php"))
	}
	return ps
}

func (m *Manager) composerCmd(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	php, ok := m.CurrentPHP()
	if !ok {
		return nil, errors.New("PHP not found. Install it from the Install tab")
	}
	if err := m.EnsurePHPIni(php); err != nil {
		return nil, err
	}
	phar := filepath.Join(m.P.Bin, "composer", "composer.phar")
	if !exists(phar) {
		return nil, errors.New("Composer not found. Install it from the Install tab")
	}
	cmd := exec.CommandContext(ctx, filepath.Join(php.Dir, exeName("php")), append([]string{phar}, args...)...)
	cmd.Dir = dir
	cmd.Env = envWithPath(m.toolDirs(), "COMPOSER_NO_INTERACTION=1", "PHPRC="+php.Dir)
	hideWindow(cmd)
	return cmd, nil
}

func (m *Manager) artisanCmd(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	php, ok := m.CurrentPHP()
	if !ok {
		return nil, errors.New("PHP not found")
	}
	cmd := exec.CommandContext(ctx, filepath.Join(php.Dir, exeName("php")), append([]string{"artisan"}, args...)...)
	cmd.Dir = dir
	cmd.Env = envWithPath(m.toolDirs(), "PHPRC="+php.Dir)
	hideWindow(cmd)
	return cmd, nil
}

var progressRe = regexp.MustCompile(`(?i)(installing|downloading|extracting|generating|updating|locking|package operations)`)

// runLogged runs cmd, appending output to logFile and showing short progress
// on the project row. It returns the last lines of output on failure.
func (m *Manager) runLogged(name string, cmd *exec.Cmd, logFile string) error {
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(f, "\n$ %s\n", strings.Join(cmd.Args[1:], " "))
	pr, pw := io.Pipe()
	cmd.Stdout, cmd.Stderr = pw, pw
	var tail []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sc.Split(scanLinesOrCR)
		last := time.Time{}
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			fmt.Fprintln(f, line)
			tail = append(tail, line)
			if len(tail) > 8 {
				tail = tail[1:]
			}
			if progressRe.MatchString(line) && time.Since(last) > 400*time.Millisecond {
				last = time.Now()
				m.tasks.set(name, trimTo(line, 90))
				m.emit("projects", m.Projects())
			}
		}
	}()
	err = cmd.Start()
	if err == nil {
		err = cmd.Wait()
	}
	pw.Close()
	<-done
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.Join(tail, " | "))
	}
	return nil
}

// scanLinesOrCR splits on \n or \r (Composer redraws progress with \r).
func scanLinesOrCR(data []byte, atEOF bool) (int, []byte, error) {
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		return i + 1, data[:i], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func trimTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// LaravelKits maps the "new project" choices to Composer packages. Starter
// kits are taken from their main branch, like the official Laravel installer
// does: their tagged releases lag far behind.
var LaravelKits = map[string]struct{ Package, Version, Label string }{
	"laravel":          {"laravel/laravel", "", "Laravel"},
	"laravel-react":    {"laravel/react-starter-kit", "dev-main", "Laravel + React starter kit"},
	"laravel-vue":      {"laravel/vue-starter-kit", "dev-main", "Laravel + Vue starter kit"},
	"laravel-livewire": {"laravel/livewire-starter-kit", "dev-main", "Laravel + Livewire starter kit"},
}

// installLaravel creates a project from kit ("laravel", "laravel-react"...)
// or, with kit == "", repairs an existing one. Then it runs the kit's own
// post-create steps, prepares .env/MySQL/Mailpit and builds the front-end.
func (m *Manager) installLaravel(name, dir, kit string) {
	logFile := filepath.Join(m.P.Logs, "composer-"+name+".log")
	finish := func(err error) {
		m.tasks.set(name, "")
		if err != nil {
			m.tasks.setError(name, trimTo(err.Error(), 300))
			m.Logf("error", "Setup for %s failed: %v. Full output: %s", name, err, logFile)
		} else {
			m.tasks.setError(name, "")
		}
		m.emit("projects", m.Projects())
		_ = m.Reload() // the document root is now public/
	}
	m.tasks.setError(name, "")
	m.tasks.set(name, "Starting Composer…")
	m.emit("projects", m.Projects())
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	var args []string
	if kit != "" {
		k, ok := LaravelKits[kit]
		if !ok {
			finish(fmt.Errorf("unknown project type %q", kit))
			return
		}
		m.Logf("info", "Creating %s in %s (this takes a few minutes)...", k.Label, name)
		args = []string{"create-project", k.Package, "."}
		if k.Version != "" {
			args = append(args, k.Version)
		}
		args = append(args, "--prefer-dist", "--no-progress")
	} else if !exists(filepath.Join(dir, "vendor", "autoload.php")) {
		m.Logf("info", "Installing Composer packages for %s...", name)
		args = []string{"install", "--no-progress"}
	}
	if args != nil {
		cmd, err := m.composerCmd(ctx, dir, args...)
		if err != nil {
			finish(err)
			return
		}
		if err := m.runLogged(name, cmd, logFile); err != nil {
			finish(err)
			return
		}
	}
	if kit != "" {
		m.runInstallerSteps(ctx, name, dir, logFile)
	}
	if err := m.setupLaravelEnv(ctx, name, dir, logFile); err != nil {
		finish(err)
		return
	}
	if err := m.buildAssets(ctx, name, dir, logFile); err != nil {
		finish(err)
		return
	}
	finish(nil)
	p := m.findProject(name)
	m.Logf("ok", "Ready: %s", firstNonEmpty(p.HTTPS, p.URL))
}

// runInstallerSteps runs the commands a starter kit asks the Laravel
// installer to run after creation (composer.json extra.laravel.installer
// post-create-project), e.g. "@php artisan install:features".
func (m *Manager) runInstallerSteps(ctx context.Context, name, dir, logFile string) {
	b, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return
	}
	var cj struct {
		Extra struct {
			Laravel struct {
				Installer struct {
					PostCreate []string `json:"post-create-project"`
				} `json:"installer"`
			} `json:"laravel"`
		} `json:"extra"`
	}
	if json.Unmarshal(b, &cj) != nil {
		return
	}
	for _, step := range cj.Extra.Laravel.Installer.PostCreate {
		f := strings.Fields(step)
		if len(f) < 3 || f[0] != "@php" || f[1] != "artisan" {
			m.Logf("info", "Skipped starter kit step %q (run it yourself in the Terminal if needed)", step)
			continue
		}
		m.tasks.set(name, "Running "+strings.Join(f[1:], " ")+"…")
		m.emit("projects", m.Projects())
		args := append(f[2:], "--no-interaction")
		cmd, err := m.artisanCmd(ctx, dir, args...)
		if err == nil {
			err = m.runLogged(name, cmd, logFile)
		}
		if err != nil {
			m.Logf("error", "Starter kit step %q failed: %v", step, err)
		}
	}
}

// nodeBin returns bin/nodejs/.../node.exe when Node.js is installed.
func (m *Manager) nodeBin() string {
	return findUnder(filepath.Join(m.P.Bin, "nodejs"), exeName("node"))
}

func composerName(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return ""
	}
	var cj struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(b, &cj)
	return cj.Name
}

// needsAssets: a starter kit (any Vite app other than the plain Laravel
// skeleton, whose welcome page works without a build) with no built assets.
func needsAssets(dir string) bool {
	if !exists(filepath.Join(dir, "package.json")) || !exists(filepath.Join(dir, "artisan")) {
		return false
	}
	if exists(filepath.Join(dir, "public", "build", "manifest.json")) || exists(filepath.Join(dir, "public", "hot")) {
		return false
	}
	return composerName(dir) != "laravel/laravel"
}

// buildAssets runs "npm install" and "npm run build" with LokalKit's Node.js.
func (m *Manager) buildAssets(ctx context.Context, name, dir, logFile string) error {
	if !exists(filepath.Join(dir, "package.json")) || exists(filepath.Join(dir, "public", "build", "manifest.json")) {
		return nil
	}
	node := m.nodeBin()
	if node == "" {
		if needsAssets(dir) {
			return errors.New("the front-end needs Node.js to build. Install Node.js from the Install tab, then click Retry")
		}
		m.Logf("info", "Tip: install Node.js from the Install tab to build %s's front-end (npm run build)", name)
		return nil
	}
	npm := filepath.Join(filepath.Dir(node), "node_modules", "npm", "bin", "npm-cli.js")
	if !exists(npm) {
		return fmt.Errorf("npm not found next to %s; reinstall Node.js from the Install tab", node)
	}
	for _, step := range [][]string{{"install", "--no-audit", "--no-fund"}, {"run", "build"}} {
		m.tasks.set(name, "npm "+strings.Join(step, " ")+"…")
		m.emit("projects", m.Projects())
		cmd := exec.CommandContext(ctx, node, append([]string{npm}, step...)...)
		cmd.Dir = dir
		cmd.Env = envWithPath(m.toolDirs(), "npm_config_update_notifier=false")
		hideWindow(cmd)
		if err := m.runLogged(name, cmd, logFile); err != nil {
			return fmt.Errorf("npm %s failed: %w", step[0], err)
		}
	}
	return nil
}

func (m *Manager) findProject(name string) Project {
	for _, p := range m.Projects() {
		if p.Name == name {
			return p
		}
	}
	return Project{Name: name}
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// setupLaravelEnv makes sure .env exists with an app key, points it at
// LokalKit's MySQL (when running) and Mailpit, then runs migrations.
func (m *Manager) setupLaravelEnv(ctx context.Context, name, dir, logFile string) error {
	if !exists(filepath.Join(dir, "artisan")) {
		return nil // not a Laravel app; composer install was enough
	}
	envPath := filepath.Join(dir, ".env")
	if !exists(envPath) {
		if b, err := os.ReadFile(filepath.Join(dir, ".env.example")); err == nil {
			if err := os.WriteFile(envPath, b, 0o644); err != nil {
				return err
			}
		}
	}
	b, _ := os.ReadFile(envPath)
	env := string(b)
	cfg := m.config()
	p := m.findProject(name)
	set := map[string]string{
		"APP_URL":       firstNonEmpty(p.HTTPS, p.URL),
		"MAIL_MAILER":   "smtp",
		"MAIL_HOST":     "127.0.0.1",
		"MAIL_PORT":     fmt.Sprint(cfg.MailpitSMTPPort),
		"MAIL_SCHEME":   "null",
		"MAIL_USERNAME": "null",
		"MAIL_PASSWORD": "null",
	}
	mysqlUp := false
	if s, _ := m.service("mysql"); s.Running() {
		db := filepathSafeDB(name)
		if err := s.(*MySQL).Exec("CREATE DATABASE IF NOT EXISTS `" + db + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
			m.Logf("error", "Could not create database %s: %v (keeping SQLite)", db, err)
		} else {
			mysqlUp = true
			for k, v := range map[string]string{
				"DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_PORT": fmt.Sprint(cfg.MySQLPort),
				"DB_DATABASE": db, "DB_USERNAME": "root", "DB_PASSWORD": "",
			} {
				set[k] = v
			}
		}
	}
	env = SetEnvValues(env, set)
	if err := os.WriteFile(envPath, []byte(env), 0o644); err != nil {
		return err
	}
	if !regexp.MustCompile(`(?m)^APP_KEY=base64:`).MatchString(env) {
		m.tasks.set(name, "Generating app key…")
		m.emit("projects", m.Projects())
		cmd, err := m.artisanCmd(ctx, dir, "key:generate", "--force", "--no-interaction")
		if err != nil {
			return err
		}
		if err := m.runLogged(name, cmd, logFile); err != nil {
			return err
		}
	}
	if mysqlUp {
		m.tasks.set(name, "Running migrations…")
		m.emit("projects", m.Projects())
		cmd, err := m.artisanCmd(ctx, dir, "migrate", "--force", "--no-interaction")
		if err != nil {
			return err
		}
		if err := m.runLogged(name, cmd, logFile); err != nil {
			m.Logf("error", "Migrations failed for %s: %v", name, err)
		} else {
			m.Logf("ok", "Database %s ready (MySQL)", filepathSafeDB(name))
		}
	} else {
		m.Logf("info", "MySQL is not running, so %s keeps Laravel's default SQLite database. Start MySQL and click Repair to switch.", name)
	}
	return nil
}

// SetEnvValues sets KEY=value lines in a .env file, uncommenting or
// appending as needed.
func SetEnvValues(env string, values map[string]string) string {
	nl := "\n"
	if strings.Contains(env, "\r\n") {
		nl = "\r\n"
	}
	for k, v := range values {
		if strings.ContainsAny(v, " #\"") {
			v = `"` + strings.ReplaceAll(v, `"`, `\"`) + `"`
		}
		re := regexp.MustCompile(`(?m)^#?\s*` + regexp.QuoteMeta(k) + `=.*$`)
		line := k + "=" + v
		if loc := re.FindStringIndex(env); loc != nil {
			end := loc[1]
			if end > loc[0] && env[end-1] == '\r' {
				end--
			}
			env = env[:loc[0]] + line + env[end:]
		} else {
			if env != "" && !strings.HasSuffix(env, "\n") {
				env += nl
			}
			env += line + nl
		}
	}
	return env
}

// RepairProject installs missing Composer packages (and Laravel setup) for
// an existing project, in the background.
func (m *Manager) RepairProject(name string) error {
	p := m.findProject(name)
	if p.Path == "" {
		return fmt.Errorf("project %s not found", name)
	}
	if _, busy := m.tasks.get(name); busy {
		return errors.New("this project is already being set up")
	}
	// An empty folder means "composer create-project" never got going: start over.
	kit := ""
	if entries, _ := os.ReadDir(p.Path); len(entries) == 0 {
		kit = m.tasks.kitFor(name)
	}
	go m.installLaravel(name, p.Path, kit)
	return nil
}
