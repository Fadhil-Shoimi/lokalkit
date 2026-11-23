package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type PHPInstall struct {
	Name      string `json:"name"`    // folder name, e.g. php-8.3.30-Win32-vs16-x64
	Dir       string `json:"dir"`
	Version   string `json:"version"` // e.g. 8.3.30
	TS        bool   `json:"ts"`      // thread safe: needed for Apache module
	ApacheDLL string `json:"-"`
	Module    string `json:"-"` // php_module / php7_module
}

// PHPInstalls lists every PHP build found in bin/php, newest first.
func (m *Manager) PHPInstalls() []PHPInstall {
	base := filepath.Join(m.P.Bin, "php")
	var out []PHPInstall
	for _, name := range subDirs(base) {
		dir := filepath.Join(base, name)
		phpExe := filepath.Join(dir, exeName("php"))
		if !exists(phpExe) {
			continue
		}
		in := PHPInstall{Name: name, Dir: dir, Version: findVersion(name)}
		if in.Version == "" {
			in.Version = probeVersion(phpExe, "-v")
		}
		for _, cand := range []struct{ dll, mod string }{
			{"php8apache2_4.dll", "php_module"},
			{"php7apache2_4.dll", "php7_module"},
		} {
			if p := filepath.Join(dir, cand.dll); exists(p) {
				in.ApacheDLL, in.Module = p, cand.mod
				break
			}
		}
		in.TS = in.ApacheDLL != "" || exists(filepath.Join(dir, "php8ts.dll")) || exists(filepath.Join(dir, "php7ts.dll"))
		out = append(out, in)
	}
	return out
}

// CurrentPHP returns the PHP selected in config, or the newest one.
func (m *Manager) CurrentPHP() (PHPInstall, bool) {
	all := m.PHPInstalls()
	if len(all) == 0 {
		return PHPInstall{}, false
	}
	want := m.config().PHPVersion
	for _, p := range all {
		if p.Name == want {
			return p, true
		}
	}
	return all[0], true
}

var phpExtensions = []string{
	"bz2", "curl", "exif", "fileinfo", "gd", "gettext", "intl", "mbstring",
	"mysqli", "openssl", "pdo_mysql", "pdo_sqlite", "sodium", "sqlite3", "zip",
}

// EnsurePHPIni creates php.ini from php.ini-development with sensible
// development defaults. An existing php.ini is never touched.
func (m *Manager) EnsurePHPIni(php PHPInstall) error {
	ini := filepath.Join(php.Dir, "php.ini")
	if exists(ini) {
		return nil
	}
	var src []byte
	var err error
	for _, name := range []string{"php.ini-development", "php.ini-production"} {
		if src, err = os.ReadFile(filepath.Join(php.Dir, name)); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("php.ini-development tidak dijumpai dalam %s", php.Dir)
	}
	cfg := m.config()
	out := BuildPHPIni(string(src), php.Dir, cfg.MailpitSMTPPort, func(ext string) bool {
		return exists(filepath.Join(php.Dir, "ext", "php_"+ext+".dll"))
	})
	m.Logf("info", "php.ini dicipta untuk PHP %s", php.Version)
	return os.WriteFile(ini, []byte(out), 0o644)
}

// BuildPHPIni applies LokalKit defaults to a php.ini template.
func BuildPHPIni(src, phpDir string, smtpPort int, hasExt func(string) bool) string {
	setKey := func(s, key, value string) string {
		re := regexp.MustCompile(`(?m)^;?[ \t]*` + regexp.QuoteMeta(key) + `[ \t]*=.*$`)
		line := key + " = " + value
		if loc := re.FindStringIndex(s); loc != nil {
			return s[:loc[0]] + line + s[loc[1]:]
		}
		return s + "\n" + line + "\n"
	}
	s := setKey(src, "extension_dir", `"`+slash(filepath.Join(phpDir, "ext"))+`"`)
	for _, ext := range phpExtensions {
		if !hasExt(ext) {
			continue
		}
		re := regexp.MustCompile(`(?m)^;[ \t]*extension[ \t]*=[ \t]*(php_)?` + regexp.QuoteMeta(ext) + `(\.dll)?[ \t]*\r?$`)
		if re.MatchString(s) {
			s = re.ReplaceAllString(s, "extension="+ext)
		} else if !strings.Contains(s, "\nextension="+ext) {
			s += "\nextension=" + ext + "\n"
		}
	}
	settings := [][2]string{
		{"memory_limit", "512M"},
		{"upload_max_filesize", "128M"},
		{"post_max_size", "128M"},
		{"max_execution_time", "120"},
		{"date.timezone", "Asia/Kuala_Lumpur"},
		{"SMTP", "127.0.0.1"},
		{"smtp_port", fmt.Sprint(smtpPort)},
	}
	for _, kv := range settings {
		s = setKey(s, kv[0], kv[1])
	}
	return s
}
