package core

import (
	"bufio"
	"bytes"
	"context"
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
	mu    sync.Mutex
	state map[string]string // project name -> status text while busy
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

// installLaravel runs "composer create-project" (fresh) or "composer install"
// (repair), then prepares .env for LokalKit's MySQL and Mailpit.
func (m *Manager) installLaravel(name, dir string, fresh bool) {
	logFile := filepath.Join(m.P.Logs, "composer-"+name+".log")
	finish := func(err error) {
		m.tasks.set(name, "")
		if err != nil {
			m.Logf("error", "Laravel setup for %s failed: %v. Full output: %s", name, err, logFile)
		}
		m.emit("projects", m.Projects())
		if err == nil {
			_ = m.Reload() // the document root is now public/
		}
	}
	m.tasks.set(name, "Starting Composer…")
	m.emit("projects", m.Projects())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	args := []string{"install", "--no-progress"}
	if fresh {
		m.Logf("info", "Installing Laravel into %s (this takes a few minutes)...", name)
		args = []string{"create-project", "laravel/laravel", ".", "--prefer-dist", "--no-progress"}
	} else {
		m.Logf("info", "Installing Composer packages for %s...", name)
	}
	cmd, err := m.composerCmd(ctx, dir, args...)
	if err != nil {
		finish(err)
		return
	}
	if err := m.runLogged(name, cmd, logFile); err != nil {
		finish(err)
		return
	}
	if err := m.setupLaravelEnv(ctx, name, dir, logFile); err != nil {
		finish(err)
		return
	}
	finish(nil)
	p := m.findProject(name)
	m.Logf("ok", "Laravel is ready: %s", firstNonEmpty(p.HTTPS, p.URL))
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
	go m.installLaravel(name, p.Path, false)
	return nil
}
