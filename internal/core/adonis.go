package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// AdonisJS projects run their own Node.js server ("node ace serve --hmr").
// LokalKit creates them with the official create-adonisjs starter kits,
// gives each one its own port, runs its dev server, and makes Apache proxy
// https://name.localhost to it, so they open like any other project.

// AdonisKits maps "new project" choices to create-adonisjs starter kits.
var AdonisKits = map[string]struct{ Kit, Label string }{
	"adonis-hypermedia": {"hypermedia", "AdonisJS (server-rendered pages)"},
	"adonis-react":      {"react", "AdonisJS + React (Inertia)"},
	"adonis-vue":        {"vue", "AdonisJS + Vue (Inertia)"},
}

const adonisFirstPort = 3333

func isAdonisDir(dir string) bool {
	return exists(filepath.Join(dir, "ace.js")) || exists(filepath.Join(dir, "adonisrc.ts"))
}

// adonisPort reads PORT from the project's .env (AdonisJS default 3333).
func adonisPort(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err == nil {
		if p, err := strconv.Atoi(envValue(string(b), "PORT")); err == nil && p > 0 {
			return p
		}
	}
	return adonisFirstPort
}

// allocAdonisPort picks a port no other AdonisJS project uses and that is free.
func (m *Manager) allocAdonisPort() int {
	used := map[int]bool{}
	cfg := m.config()
	for _, p := range []int{cfg.ApachePort, cfg.SSLPort, cfg.MySQLPort, cfg.MailpitHTTPPort, cfg.MailpitSMTPPort} {
		used[p] = true
	}
	for _, p := range m.Projects() {
		if p.Kind == "adonis" {
			used[p.ProxyPort] = true
		}
	}
	for port := adonisFirstPort; port < adonisFirstPort+500; port++ {
		if !used[port] && PortFree(port) {
			return port
		}
	}
	return adonisFirstPort
}

// ---------- dev servers ----------

type devServers struct {
	mu    sync.Mutex
	procs map[string]*Proc
}

func (d *devServers) get(name string) *Proc {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.procs[name]
}

func (d *devServers) set(name string, p *Proc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.procs == nil {
		d.procs = map[string]*Proc{}
	}
	d.procs[name] = p
}

func (d *devServers) all() []*Proc {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []*Proc
	for _, p := range d.procs {
		out = append(out, p)
	}
	return out
}

// DevRunning reports whether a project's dev server runs.
func (m *Manager) DevRunning(name string) bool {
	p := m.devs.get(name)
	return p != nil && p.Running()
}

// StartDev starts an AdonisJS project's dev server (node ace serve --hmr).
func (m *Manager) StartDev(name string) error {
	p := m.findProject(name)
	if p.Kind != "adonis" {
		return fmt.Errorf("%s is not an AdonisJS project", name)
	}
	if m.DevRunning(name) {
		return nil
	}
	node := m.nodeBin()
	if node == "" {
		return errors.New("Node.js is not installed. Install it from the Install tab")
	}
	if !exists(filepath.Join(p.Path, "node_modules")) {
		return errors.New("packages are not installed yet (node_modules missing). Click Repair")
	}
	port := p.ProxyPort
	if !waitPortFree(port, 3*time.Second) {
		return fmt.Errorf("port %d is already in use; change PORT in %s's .env", port, name)
	}
	proc := NewProc(name+" dev server", func(n string, expected bool, err error) {
		if !expected {
			m.Logf("error", "%s stopped (%v). See logs\\dev-%s.log", n, err, name)
		}
		m.emit("projects", m.Projects())
	})
	m.devs.set(name, proc)
	env := envWithPath(m.toolDirs(), "PORT="+strconv.Itoa(port), "HOST=127.0.0.1", "NODE_ENV=development", "FORCE_COLOR=0")
	m.Logf("info", "Starting %s dev server on port %d...", name, port)
	if err := proc.Start(node, []string{"ace", "serve", "--hmr"}, p.Path, env, filepath.Join(m.P.Logs, "dev-"+name+".log")); err != nil {
		return err
	}
	m.emit("projects", m.Projects())
	if err := waitPort(port, proc.Exited(), 90*time.Second); err != nil {
		_ = proc.Stop(nil, 0)
		b, _ := os.ReadFile(filepath.Join(m.P.Logs, "dev-"+name+".log"))
		return fmt.Errorf("%s dev server didn't start (%v): %s", name, err, lastLines(string(b), 3))
	}
	m.Logf("ok", "%s is running at %s", name, firstNonEmpty(p.HTTPS, p.URL))
	m.emit("projects", m.Projects())
	return nil
}

// StopDev stops an AdonisJS project's dev server.
func (m *Manager) StopDev(name string) error {
	proc := m.devs.get(name)
	if proc == nil || !proc.Running() {
		return nil
	}
	err := proc.Stop(nil, 0)
	m.Logf("ok", "%s dev server stopped", name)
	m.emit("projects", m.Projects())
	return err
}

func (m *Manager) stopAllDev() {
	for _, p := range m.devs.all() {
		if p.Running() {
			_ = p.Stop(nil, 0)
		}
	}
}

// ---------- creating ----------

// npmCmd runs npm with LokalKit's Node.js (node npm-cli.js ...).
func (m *Manager) npmCmd(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	node := m.nodeBin()
	if node == "" {
		return nil, errors.New("Node.js is not installed. Install it from the Install tab")
	}
	npm := filepath.Join(filepath.Dir(node), "node_modules", "npm", "bin", "npm-cli.js")
	if !exists(npm) {
		return nil, fmt.Errorf("npm not found next to %s; reinstall Node.js from the Install tab", node)
	}
	cmd := exec.CommandContext(ctx, node, append([]string{npm}, args...)...)
	cmd.Dir = dir
	cmd.Env = envWithPath(m.toolDirs(), "npm_config_yes=true", "npm_config_update_notifier=false", "npm_config_fund=false", "FORCE_COLOR=0")
	hideWindow(cmd)
	return cmd, nil
}

func (m *Manager) aceCmd(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	node := m.nodeBin()
	if node == "" {
		return nil, errors.New("Node.js is not installed")
	}
	cmd := exec.CommandContext(ctx, node, append([]string{"ace"}, args...)...)
	cmd.Dir = dir
	cmd.Env = envWithPath(m.toolDirs(), "FORCE_COLOR=0")
	hideWindow(cmd)
	return cmd, nil
}

// installAdonis creates (kit != "") or repairs an AdonisJS project.
func (m *Manager) installAdonis(name, dir, kit string) {
	logFile := filepath.Join(m.P.Logs, "setup-"+name+".log")
	finish := func(err error) {
		m.tasks.set(name, "")
		if err != nil {
			m.tasks.setError(name, trimTo(err.Error(), 300))
			m.Logf("error", "Setup for %s failed: %v. Full output: %s", name, err, logFile)
		} else {
			m.tasks.setError(name, "")
		}
		m.emit("projects", m.Projects())
		_ = m.Reload() // the proxy vhost needs the project's port
	}
	m.tasks.setError(name, "")
	m.tasks.set(name, "Starting…")
	m.emit("projects", m.Projects())
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()
	db := m.tasks.dbFor(name)

	if kit != "" {
		k := AdonisKits[kit]
		m.Logf("info", "Creating %s in %s (this takes a few minutes)...", k.Label, name)
		m.tasks.set(name, "Downloading the AdonisJS starter kit…")
		m.emit("projects", m.Projects())
		args := []string{"exec", "--yes", "--", "create-adonisjs@latest", ".", "--kit=" + k.Kit, "--pkg=npm"}
		if db.Mode == "mysql" || db.Mode == "none" {
			args = append(args, "--skip-migrations")
		}
		cmd, err := m.npmCmd(ctx, dir, args...)
		if err == nil {
			err = m.runLogged(name, cmd, logFile)
		}
		if err != nil {
			finish(err)
			return
		}
	} else if !exists(filepath.Join(dir, "node_modules")) {
		m.tasks.set(name, "npm install…")
		m.emit("projects", m.Projects())
		cmd, err := m.npmCmd(ctx, dir, "install", "--no-audit", "--no-fund")
		if err == nil {
			err = m.runLogged(name, cmd, logFile)
		}
		if err != nil {
			finish(fmt.Errorf("npm install failed: %w", err))
			return
		}
	}
	if err := m.setupAdonisEnv(ctx, name, dir, db, logFile); err != nil {
		finish(err)
		return
	}
	// Write the proxy vhost, then start the dev server before marking the
	// project ready, so "ready" means it really opens.
	m.tasks.set(name, "Starting dev server…")
	m.emit("projects", m.Projects())
	_ = m.Reload()
	if err := m.StartDev(name); err != nil {
		m.tasks.set(name, "")
		m.tasks.setError(name, trimTo(err.Error(), 300))
		m.Logf("error", "%v", err)
		m.emit("projects", m.Projects())
		return
	}
	m.tasks.set(name, "")
	m.emit("projects", m.Projects())
	p := m.findProject(name)
	m.Logf("ok", "Ready: %s", firstNonEmpty(p.HTTPS, p.URL))
}

// setupAdonisEnv gives the project its own PORT, points APP_URL at its
// LokalKit address and, for MySQL, switches Lucid to LokalKit's MySQL.
func (m *Manager) setupAdonisEnv(ctx context.Context, name, dir string, db DBChoice, logFile string) error {
	envPath := filepath.Join(dir, ".env")
	if !exists(envPath) {
		if b, err := os.ReadFile(filepath.Join(dir, ".env.example")); err == nil {
			_ = os.WriteFile(envPath, b, 0o644)
		}
	}
	b, _ := os.ReadFile(envPath)
	env := string(b)
	set := map[string]string{"HOST": "127.0.0.1"}
	if p, err := strconv.Atoi(envValue(env, "PORT")); err != nil || p == adonisFirstPort || p == 0 {
		set["PORT"] = strconv.Itoa(m.allocAdonisPort())
	}
	if err := os.WriteFile(envPath, []byte(SetEnvValues(env, set)), 0o644); err != nil {
		return err
	}
	p := m.findProject(name)
	url := firstNonEmpty(p.HTTPS, p.URL)
	b, _ = os.ReadFile(envPath)
	_ = os.WriteFile(envPath, []byte(SetEnvValues(string(b), map[string]string{"APP_URL": url})), 0o644)

	cfg := m.config()
	dbName := db.Name
	if dbName == "" {
		dbName = filepathSafeDB(name)
	}
	useMySQL := db.Mode == "mysql"
	if useMySQL {
		m.tasks.set(name, "Configuring MySQL…")
		m.emit("projects", m.Projects())
		cmd, err := m.aceCmd(ctx, dir, "configure", "@adonisjs/lucid", "--db=mysql", "--install", "--force")
		if err == nil {
			err = m.runLogged(name, cmd, logFile)
		}
		if err != nil {
			m.Logf("error", "Could not switch %s to MySQL (%v); it keeps SQLite", name, err)
			useMySQL = false
		} else {
			b, _ := os.ReadFile(envPath)
			out := SetEnvValues(string(b), map[string]string{
				"DB_CONNECTION": "mysql", "DB_HOST": "127.0.0.1", "DB_PORT": strconv.Itoa(cfg.MySQLPort),
				"DB_USER": "root", "DB_PASSWORD": "", "DB_DATABASE": dbName,
			})
			if err := os.WriteFile(envPath, []byte(out), 0o644); err != nil {
				return err
			}
		}
	}
	if db.Mode != "none" {
		m.tasks.set(name, "Running migrations…")
		m.emit("projects", m.Projects())
		cmd, err := m.aceCmd(ctx, dir, "migration:run", "--force")
		if err == nil {
			err = m.runLogged(name, cmd, logFile)
		}
		if err != nil {
			m.Logf("error", "Migrations failed for %s: %v", name, err)
		} else if useMySQL {
			m.Logf("ok", "Database %s ready (MySQL), tables created", dbName)
		} else {
			m.Logf("ok", "SQLite database ready, tables created")
		}
	}
	return nil
}

// ---------- Apache reverse proxy ----------

// BuildProxyVhost makes Apache forward a project's domain to its own server
// (AdonisJS dev server), websockets included for hot reload.
func BuildProxyVhost(p Project, listenPort int, ssl *SSLFiles) string {
	target := fmt.Sprintf("http://127.0.0.1:%d/", p.ProxyPort)
	msg := fmt.Sprintf(`"<!doctype html><html><body style='font-family:system-ui;padding:40px'><h1>%s</h1><p>The AdonisJS dev server is not running. Start it from LokalKit (Projects &rarr; %s &rarr; Start).</p></body></html>"`, p.Name, p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "# Generated by LokalKit for %s (AdonisJS on port %d)\n", p.Name, p.ProxyPort)
	port := listenPort
	if ssl != nil {
		port = ssl.Port
	}
	fmt.Fprintf(&b, "<VirtualHost *:%d>\n    ServerName %s\n    ServerAlias *.%s\n", port, p.Host, p.Host)
	if ssl != nil {
		fmt.Fprintf(&b, "    SSLEngine on\n    SSLCertificateFile \"%s\"\n    SSLCertificateKeyFile \"%s\"\n", slash(ssl.Cert), slash(ssl.Key))
		b.WriteString("    RequestHeader set X-Forwarded-Proto \"https\"\n")
	} else {
		b.WriteString("    RequestHeader set X-Forwarded-Proto \"http\"\n")
	}
	b.WriteString("    ProxyPreserveHost On\n")
	b.WriteString("    ProxyTimeout 600\n")
	fmt.Fprintf(&b, "    ProxyPass / %s upgrade=websocket\n", target)
	fmt.Fprintf(&b, "    ProxyPassReverse / %s\n", target)
	fmt.Fprintf(&b, "    ErrorDocument 502 %s\n    ErrorDocument 503 %s\n", msg, msg)
	b.WriteString("</VirtualHost>\n")
	return b.String()
}
