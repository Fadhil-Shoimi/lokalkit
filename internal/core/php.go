package core

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type PHPInstall struct {
	Name      string `json:"name"` // folder name, e.g. php-8.3.30-Win32-vs16-x64
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
		return m.fixExtensionDir(php, ini)
	}
	var src []byte
	var err error
	for _, name := range []string{"php.ini-development", "php.ini-production"} {
		if src, err = os.ReadFile(filepath.Join(php.Dir, name)); err == nil {
			break
		}
	}
	if err != nil {
		return fmt.Errorf("php.ini-development not found in %s", php.Dir)
	}
	cfg := m.config()
	out := BuildPHPIni(string(src), php.Dir, cfg.MailpitSMTPPort, func(ext string) bool {
		return exists(filepath.Join(php.Dir, "ext", "php_"+ext+".dll"))
	})
	m.Logf("info", "Created php.ini for PHP %s", php.Version)
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
		if enabledExt(s, ext) {
			continue
		}
		// Only the real list entry ";extension=X" (no space after ';'). The
		// file's header comments contain examples like ";   extension=mysqli"
		// which must stay commented, or the module loads twice.
		re := regexp.MustCompile(`(?m)^;extension[ \t]*=[ \t]*(php_)?` + regexp.QuoteMeta(ext) + `(\.dll)?[ \t]*\r?$`)
		if loc := re.FindStringIndex(s); loc != nil {
			end := loc[1]
			if end > loc[0] && s[end-1] == '\r' {
				end--
			}
			s = s[:loc[0]] + "extension=" + ext + s[end:]
		} else {
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

var extDirRe = regexp.MustCompile(`(?m)^[ \t]*extension_dir[ \t]*=[ \t]*"?([^"\r\n;]*)"?.*$`)

// fixExtensionDir repairs an extension_dir that points somewhere else: the
// LokalKit folder was moved, or php.ini was copied from another install
// (e.g. Laragon). Without it PHP loads no extensions (openssl, zip, curl...).
func (m *Manager) fixExtensionDir(php PHPInstall, ini string) error {
	b, err := os.ReadFile(ini)
	if err != nil {
		return err
	}
	want := slash(filepath.Join(php.Dir, "ext"))
	fixed, changed := FixExtensionDir(string(b), want)
	if changed {
		m.Logf("info", "Fixed extension_dir in %s (it pointed to another folder)", ini)
	}
	if deduped, n := DedupeExtensions(fixed); n > 0 {
		fixed, changed = deduped, true
		m.Logf("info", "Removed %d duplicate extension line(s) from %s", n, ini)
	}
	if !changed {
		return nil
	}
	return os.WriteFile(ini, []byte(fixed), 0o644)
}

var extLineRe = regexp.MustCompile(`(?m)^[ \t]*extension[ \t]*=[ \t]*"?(?:php_)?([A-Za-z0-9_]+)(?:\.dll)?"?[ \t]*\r?$`)

// enabledExt reports whether ext is already enabled (uncommented).
func enabledExt(ini, ext string) bool {
	for _, m := range extLineRe.FindAllStringSubmatch(ini, -1) {
		if strings.EqualFold(m[1], ext) {
			return true
		}
	}
	return false
}

// DedupeExtensions comments out repeated "extension=X" lines, which make
// PHP warn "Module X is already loaded". Returns the count removed.
func DedupeExtensions(ini string) (string, int) {
	seen := map[string]bool{}
	removed := 0
	out := extLineRe.ReplaceAllStringFunc(ini, func(line string) string {
		name := strings.ToLower(extLineRe.FindStringSubmatch(line)[1])
		if seen[name] {
			removed++
			cr := ""
			if strings.HasSuffix(line, "\r") {
				cr = "\r"
			}
			return ";" + strings.TrimRight(strings.TrimLeft(line, " \t"), "\r") + " ; duplicate, disabled by LokalKit" + cr
		}
		seen[name] = true
		return line
	})
	return out, removed
}

// FixExtensionDir points an absolute extension_dir at want. Relative values
// and the right path are left alone.
func FixExtensionDir(ini, want string) (string, bool) {
	loc := extDirRe.FindStringSubmatchIndex(ini)
	if loc == nil {
		return ini, false
	}
	cur := strings.TrimSpace(ini[loc[2]:loc[3]])
	isAbs := filepath.IsAbs(cur) || strings.HasPrefix(cur, "/") || (len(cur) > 2 && cur[1] == ':')
	if !isAbs || strings.EqualFold(strings.TrimRight(filepath.ToSlash(cur), "/"), strings.TrimRight(want, "/")) {
		return ini, false
	}
	end := loc[1]
	if end > loc[0] && ini[end-1] == '\r' {
		end--
	}
	return ini[:loc[0]] + `extension_dir = "` + want + `"` + ini[end:], true
}

// AdoptPHPIni copies php.ini from one installed PHP to another (when the
// new one has none), so your settings follow a PHP update. extension_dir is
// corrected for the new folder.
func (m *Manager) AdoptPHPIni(fromName, toName string) error {
	var from, to PHPInstall
	for _, p := range m.PHPInstalls() {
		if p.Name == fromName {
			from = p
		}
		if p.Name == toName {
			to = p
		}
	}
	if to.Dir == "" {
		return fmt.Errorf("PHP %s not found", toName)
	}
	dst := filepath.Join(to.Dir, "php.ini")
	if exists(dst) || from.Dir == "" {
		return m.EnsurePHPIni(to)
	}
	b, err := os.ReadFile(filepath.Join(from.Dir, "php.ini"))
	if err != nil {
		return m.EnsurePHPIni(to)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return err
	}
	m.Logf("info", "Copied your php.ini settings from PHP %s to PHP %s", from.Version, to.Version)
	return m.EnsurePHPIni(to) // fixes extension_dir
}
