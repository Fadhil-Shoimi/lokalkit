package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		args := []string{"/K"}
		if command != "" {
			args = append(args, command)
		} else {
			args = append(args, "title LokalKit Terminal && php -v")
		}
		cmd = exec.Command("cmd.exe", args...)
		newConsole(cmd)
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
	return "", errors.New("no database tool found. Put HeidiSQL in bin\\heidisql or adminer.php at etc\\apps\\adminer\\index.php")
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
func (m *Manager) CreateProject(name, kind string) (Project, error) {
	label := HostLabel(name)
	if label == "" || label != name {
		return Project{}, errors.New("project names may only contain lowercase letters, numbers and '-' (e.g. my-shop)")
	}
	dir := filepath.Join(m.P.WWW, name)
	if exists(dir) {
		return Project{}, fmt.Errorf("folder %s already exists", name)
	}
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
		page := fmt.Sprintf("<?php\n// Project %s, created by LokalKit\n?>\n<!doctype html>\n<html><head><meta charset=\"utf-8\"><title>%s</title></head>\n<body style=\"font-family:system-ui;padding:40px\">\n<h1>%s</h1>\n<p>PHP <?= PHP_VERSION ?> is running. Edit this file at <code><?= __FILE__ ?></code></p>\n</body></html>\n", name, name, name)
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
