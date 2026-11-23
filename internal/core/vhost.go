package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

type Project struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	DocRoot string `json:"docRoot"`
	Host    string `json:"host"`
	URL     string `json:"url"`
	Kind    string `json:"kind"` // laravel, wordpress, php
}

var hostLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// HostLabel turns a folder name into a valid domain label ("My_App" -> "my-app").
func HostLabel(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.NewReplacer(" ", "-", "_", "-", ".", "-").Replace(s)
	if !hostLabelRe.MatchString(s) {
		return ""
	}
	return s
}

// Projects lists every folder in www that can become a virtual host.
func (m *Manager) Projects() []Project {
	return ScanProjects(m.P.WWW, m.config())
}

func ScanProjects(www string, cfg Config) []Project {
	entries, err := os.ReadDir(www)
	if err != nil {
		return nil
	}
	var out []Project
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		label := HostLabel(e.Name())
		if label == "" {
			continue
		}
		dir := filepath.Join(www, e.Name())
		p := Project{Name: e.Name(), Path: dir, DocRoot: dir, Host: label + "." + cfg.TLD, Kind: "php"}
		if isDir(filepath.Join(dir, "public")) {
			p.DocRoot = filepath.Join(dir, "public")
		}
		switch {
		case exists(filepath.Join(dir, "artisan")):
			p.Kind = "laravel"
		case exists(filepath.Join(dir, "wp-config.php")) || exists(filepath.Join(dir, "wp-login.php")):
			p.Kind = "wordpress"
		}
		p.URL = "http://" + p.Host
		if cfg.ApachePort != 80 {
			p.URL += fmt.Sprintf(":%d", cfg.ApachePort)
		}
		out = append(out, p)
	}
	return out
}

func BuildVhost(p Project, port int) string {
	dr := slash(p.DocRoot)
	return fmt.Sprintf(`# Dijana oleh LokalKit untuk %s
<VirtualHost *:%d>
    ServerName %s
    ServerAlias *.%s
    DocumentRoot "%s"
    <Directory "%s">
        Options Indexes FollowSymLinks
        AllowOverride All
        Require all granted
    </Directory>
</VirtualHost>
`, p.Name, port, p.Host, p.Host, dr, dr)
}

// WriteVhosts regenerates etc/apache/sites-enabled/auto.*.conf.
func (m *Manager) WriteVhosts() error {
	dir := filepath.Join(m.P.Etc, "apache", "sites-enabled")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	old, _ := filepath.Glob(filepath.Join(dir, "auto.*.conf"))
	for _, f := range old {
		_ = os.Remove(f)
	}
	cfg := m.config()
	def := fmt.Sprintf("# Dijana oleh LokalKit\n<VirtualHost *:%d>\n    ServerName localhost\n    DocumentRoot \"%s\"\n</VirtualHost>\n",
		cfg.ApachePort, slash(m.P.WWW))
	if err := os.WriteFile(filepath.Join(dir, "auto.000-localhost.conf"), []byte(def), 0o644); err != nil {
		return err
	}
	if ad := m.adminerDir(); exists(filepath.Join(ad, "index.php")) {
		alias := fmt.Sprintf("# Adminer: http://localhost/adminer/\nAlias /adminer \"%s\"\n<Directory \"%s\">\n    Require local\n    DirectoryIndex index.php\n</Directory>\n", slash(ad), slash(ad))
		if err := os.WriteFile(filepath.Join(dir, "auto.001-adminer.conf"), []byte(alias), 0o644); err != nil {
			return err
		}
	}
	if !cfg.AutoVhost {
		return nil
	}
	for _, p := range m.Projects() {
		if err := os.WriteFile(filepath.Join(dir, "auto."+p.Host+".conf"), []byte(BuildVhost(p, cfg.ApachePort)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

const (
	hostsBegin = "# >>> LokalKit (dijana automatik, jangan edit blok ini)"
	hostsEnd   = "# <<< LokalKit"
)

func HostsFilePath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		return filepath.Join(root, "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

// BuildHostsFile replaces the LokalKit block in a hosts file with entries for hosts.
func BuildHostsFile(current string, hosts []string) string {
	nl := "\n"
	if strings.Contains(current, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(current, "\r\n", "\n"), "\n")
	var kept []string
	inBlock := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == hostsBegin {
			inBlock = true
			continue
		}
		if inBlock {
			if t == hostsEnd {
				inBlock = false
			}
			continue
		}
		kept = append(kept, l)
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(hosts) > 0 {
		kept = append(kept, "", hostsBegin)
		for _, h := range hosts {
			kept = append(kept, "127.0.0.1 "+h)
		}
		kept = append(kept, hostsEnd)
	}
	return strings.Join(kept, nl) + nl
}

var ErrNeedAdmin = errors.New("perlu kebenaran admin untuk edit fail hosts")

// SyncHostsFile writes the hosts file. Returns changed=false when already up to date.
func SyncHostsFile(hosts []string) (changed bool, err error) {
	path := HostsFilePath()
	cur, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	want := BuildHostsFile(string(cur), hosts)
	if want == string(cur) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return true, ErrNeedAdmin
		}
		return true, err
	}
	return true, nil
}

func (m *Manager) hostNames() []string {
	if !m.config().AutoVhost {
		return nil
	}
	var hs []string
	for _, p := range m.Projects() {
		hs = append(hs, p.Host)
	}
	return hs
}

// SyncHosts updates the hosts file, asking Windows for admin rights (UAC)
// only when something actually changed.
func (m *Manager) SyncHosts() error {
	changed, err := SyncHostsFile(m.hostNames())
	if err == nil {
		if changed {
			m.Logf("info", "Fail hosts dikemas kini")
		}
		return nil
	}
	if !errors.Is(err, ErrNeedAdmin) || runtime.GOOS != "windows" {
		return err
	}
	m.Logf("info", "Minta kebenaran admin untuk kemas kini fail hosts...")
	exe, _ := os.Executable()
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	ps := fmt.Sprintf(`Start-Process -FilePath %s -ArgumentList '--sync-hosts', %s -Verb RunAs -Wait -WindowStyle Hidden`,
		q(exe), q(`"`+m.P.Root+`"`))
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("fail hosts tidak dikemas kini (kebenaran admin ditolak?): %s", lastLines(string(out), 2))
	}
	if changed, _ := SyncHostsFile(m.hostNames()); changed {
		return errors.New("fail hosts masih belum dikemas kini")
	}
	m.Logf("info", "Fail hosts dikemas kini")
	return nil
}

// RunElevatedHostsSync is what "lokalkit.exe --sync-hosts <root>" runs as admin.
func RunElevatedHostsSync(root string) error {
	p := NewPaths(root)
	cfg, err := LoadConfig(filepath.Join(root, "lokalkit.json"))
	if err != nil {
		return err
	}
	var hs []string
	if cfg.AutoVhost {
		for _, pr := range ScanProjects(p.WWW, cfg) {
			hs = append(hs, pr.Host)
		}
	}
	_, err = SyncHostsFile(hs)
	return err
}
