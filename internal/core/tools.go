package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// toolDirs are the folders added to PATH in LokalKit terminals.
func (m *Manager) toolDirs() []string {
	var dirs []string
	if php, ok := m.CurrentPHP(); ok {
		dirs = append(dirs, php.Dir)
	}
	if bin := findUnder(filepath.Join(m.P.Bin, "mysql"), filepath.Join("bin", exeName("mysql"))); bin != "" {
		dirs = append(dirs, filepath.Dir(bin))
	}
	if dir := m.ensureComposerShim(); dir != "" {
		dirs = append(dirs, dir)
	}
	for _, t := range []struct{ base, rel string }{
		{"nodejs", exeName("node")},
		{"git", filepath.Join("cmd", exeName("git"))},
	} {
		if bin := findUnder(filepath.Join(m.P.Bin, t.base), t.rel); bin != "" {
			dirs = append(dirs, filepath.Dir(bin))
		}
	}
	return dirs
}

// ensureComposerShim creates composer.bat next to composer.phar so that
// "composer" works in the terminal.
func (m *Manager) ensureComposerShim() string {
	dir := filepath.Join(m.P.Bin, "composer")
	if !exists(filepath.Join(dir, "composer.phar")) {
		return ""
	}
	if runtime.GOOS == "windows" {
		bat := filepath.Join(dir, "composer.bat")
		if !exists(bat) {
			_ = os.WriteFile(bat, []byte("@echo off\r\nphp \"%~dp0composer.phar\" %*\r\n"), 0o644)
		}
	}
	return dir
}

// OpenTerminal opens a console in dir (default: www) with PHP, MySQL,
// Composer, Node and Git on PATH. If command is set, it runs it first.
func (m *Manager) OpenTerminal(dir, command string) error {
	if dir == "" {
		dir = m.P.WWW
	}
	env := envWithPath(m.toolDirs())
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		if command == "" {
			initPath, err := m.prepareTerminal()
			if err != nil {
				return err
			}
			command = initPath
		}
		if wt := windowsTerminal(); wt != "" {
			// Windows Terminal with the LokalKit profile (colours, icon). The
			// command line is given too, so it works even before Windows
			// Terminal has picked up the new profile.
			cmd = exec.Command(wt, "-p", terminalProfileName, "-d", dir, "cmd.exe", "/K", command)
		} else {
			cmd = terminalCommand(dir, command)
		}
	} else {
		term := os.Getenv("TERMINAL")
		if term == "" {
			term = "x-terminal-emulator"
		}
		cmd = exec.Command(term)
	}
	cmd.Dir, cmd.Env = dir, env
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// OpenPath opens a folder or file with Explorer.
func OpenPath(p string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer.exe", p)
	case "darwin":
		cmd = exec.Command("open", p)
	default:
		cmd = exec.Command("xdg-open", p)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// OpenDatabase launches HeidiSQL if available; otherwise returns the
// Adminer URL for the caller to open in the browser.
func (m *Manager) OpenDatabase() (url string, err error) {
	// A database tool can't connect to a stopped MySQL ("Connection refused"),
	// so start it first.
	if s, _ := m.service("mysql"); s.Info().Installed && !s.Running() {
		m.Logf("info", "MySQL is not running; starting it before opening the database tool...")
		if err := m.Start("mysql"); err != nil {
			return "", fmt.Errorf("MySQL could not start, so the database tool can't connect: %w", err)
		}
	}
	if dbeaver := findUnder(filepath.Join(m.P.Bin, "dbeaver"), exeName("dbeaver")); dbeaver != "" {
		// Already open? Bring it to the front instead of starting a second
		// DBeaver, which fails with "Can't lock workspace".
		if pids := runningPIDs(dbeaver); len(pids) > 0 {
			if focusProcess(pids) {
				m.Logf("info", "DBeaver is already open; brought it to the front")
			} else {
				m.Logf("info", "DBeaver is already running (still starting?)")
			}
			return "", nil
		}
		cmd := exec.Command(dbeaver, DBeaverArgs(m.config().MySQLPort, dbeaverHasConnection())...)
		cmd.Dir = filepath.Dir(dbeaver)
		if err := cmd.Start(); err != nil {
			return "", err
		}
		go func() { _ = cmd.Wait() }()
		m.Logf("info", "Opening DBeaver (connection \"LokalKit MySQL\")...")
		return "", nil
	}
	if heidi := findUnder(filepath.Join(m.P.Bin, "heidisql"), exeName("heidisql")); heidi != "" {
		cfg := m.config()
		cmd := exec.Command(heidi, "-h=127.0.0.1", "-u=root", fmt.Sprintf("-P=%d", cfg.MySQLPort))
		if err := cmd.Start(); err != nil {
			return "", err
		}
		go func() { _ = cmd.Wait() }()
		return "", nil
	}
	if exists(filepath.Join(m.adminerDir(), "index.php")) {
		return m.LocalURL("/adminer/"), nil
	}
	return "", errors.New("no database tool found. Install DBeaver or Adminer from the Install tab")
}

// DBeaverArgs opens DBeaver connected to "LokalKit MySQL". The connection
// is created the first time; afterwards the saved one is reused (asking to
// create it again would add a duplicate to DBeaver's list).
func DBeaverArgs(mysqlPort int, exists bool) []string {
	if exists {
		return []string{"-nosplash", "-con", "name=" + dbeaverConnName + "|create=false|connect=true"}
	}
	con := strings.Join([]string{
		"id=lokalkit-mysql",
		"name=" + dbeaverConnName,
		"driver=mysql",
		"host=127.0.0.1",
		fmt.Sprintf("port=%d", mysqlPort),
		"user=root",
		"password=",
		"savePassword=true",
		"prop.allowPublicKeyRetrieval=true",
		"prop.useSSL=false",
		"save=true",
		"connect=true",
	}, "|")
	return []string{"-nosplash", "-con", con}
}

const dbeaverConnName = "LokalKit MySQL"

// dbeaverHasConnection checks DBeaver's saved connections
// (%APPDATA%\DBeaverData\workspace6\*\.dbeaver\data-sources*.json).
func dbeaverHasConnection() bool {
	base := os.Getenv("APPDATA")
	if base == "" {
		return false
	}
	files, _ := filepath.Glob(filepath.Join(base, "DBeaverData", "workspace6", "*", ".dbeaver", "data-sources*.json"))
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil && strings.Contains(string(b), `"`+dbeaverConnName+`"`) {
			return true
		}
	}
	return false
}

func (m *Manager) adminerDir() string { return filepath.Join(m.P.Etc, "apps", "adminer") }

func (m *Manager) LocalURL(path string) string {
	port := m.config().ApachePort
	if port == 80 {
		return "http://localhost" + path
	}
	return fmt.Sprintf("http://localhost:%d%s", port, path)
}

func (m *Manager) MailpitURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", m.config().MailpitHTTPPort)
}

// CreateProject makes a new folder in www. kind: "blank" or "laravel".
func (m *Manager) CreateProject(name, kind string, db DBChoice) (Project, error) {
	label := HostLabel(name)
	if label == "" || label != name {
		return Project{}, errors.New("project names may only contain lowercase letters, numbers and '-' (e.g. my-shop)")
	}
	dir := filepath.Join(m.P.WWW, name)
	if exists(dir) {
		return Project{}, fmt.Errorf("folder %s already exists", name)
	}
	if db.Name == "" {
		db.Name = filepathSafeDB(name)
	}
	if db.Mode == "mysql" {
		// Fail early (MySQL missing, bad name) before anything is created.
		if err := m.CreateMySQLDatabase(db.Name); err != nil {
			return Project{}, err
		}
	}
	m.tasks.rememberDB(name, db)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Project{}, err
	}
	switch kind {
	case "laravel", "laravel-react", "laravel-vue", "laravel-livewire":
		if !exists(filepath.Join(m.P.Bin, "composer", "composer.phar")) {
			_ = os.Remove(dir)
			return Project{}, errors.New("Composer not found. Install it from the Install tab")
		}
		m.tasks.rememberKit(name, kind)
		// Runs in the background; progress shows on the project row.
		go m.installLaravel(name, dir, kind)
	default:
		page := blankIndexPHP(name, db, m.config().MySQLPort)
		if err := os.WriteFile(filepath.Join(dir, "index.php"), []byte(page), 0o644); err != nil {
			return Project{}, err
		}
	}
	m.Logf("ok", "Project %s created", name)
	if err := m.Reload(); err != nil {
		m.Logf("error", "%v", err)
	}
	for _, p := range m.Projects() {
		if p.Name == name {
			return p, nil
		}
	}
	return Project{Name: name, Path: dir}, nil
}

func filepathSafeDB(name string) string {
	out := []rune{}
	for _, r := range name {
		if r == '-' {
			r = '_'
		}
		out = append(out, r)
	}
	return string(out)
}

// blankIndexPHP is the starter page of a "Blank PHP" project.
func blankIndexPHP(name string, db DBChoice, port int) string {
	dbBlock := ""
	if db.Mode == "mysql" {
		dbBlock = fmt.Sprintf(`
<?php
// Database created by LokalKit
$db = @new mysqli('127.0.0.1', 'root', '', '%s', %d);
?>
<p>Database <code>%s</code>: <?= $db->connect_errno ? 'not connected (' . htmlspecialchars($db->connect_error) . ')' : 'connected ✓' ?></p>
`, db.Name, port, db.Name)
	}
	return fmt.Sprintf(`<?php
// Project %s, created by LokalKit
?>
<!doctype html>
<html><head><meta charset="utf-8"><title>%s</title></head>
<body style="font-family:system-ui;padding:40px">
<h1>%s</h1>
<p>PHP <?= PHP_VERSION ?> is running. Edit this file at <code><?= __FILE__ ?></code></p>%s
</body></html>
`, name, name, name, dbBlock)
}
