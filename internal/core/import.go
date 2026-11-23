package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Importing projects from another tool (usually Laragon):
//   - project folders are linked where they are (no copying);
//   - APP_URL / DB_PORT in a Laravel .env are updated (with a backup);
//   - databases are copied from Laragon's MySQL data folder into LokalKit's
//     MySQL by briefly starting Laragon's own mysqld on a spare port.

type ImportCandidate struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Host     string `json:"host"`
	Added    bool   `json:"added"`    // already in LokalKit
	Conflict string `json:"conflict"` // why it cannot be added
}

// DetectLaragon returns Laragon's root folder (e.g. C:\laragon), or "".
func DetectLaragon() string {
	var roots []string
	if runtime.GOOS == "windows" {
		for _, d := range "CDEFG" {
			roots = append(roots, string(d)+`:\laragon`)
		}
	} else if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, "laragon"))
	}
	for _, r := range roots {
		if isDir(filepath.Join(r, "www")) {
			return r
		}
	}
	return ""
}

// isProjectRoot: the folder is itself a project (not a folder of projects
// like C:\laragon\www, which often has its own index.php).
func isProjectRoot(dir string) bool {
	for _, f := range []string{"artisan", "composer.json", "wp-config.php", "package.json"} {
		if exists(filepath.Join(dir, f)) {
			return true
		}
	}
	return false
}

// ScanImport lists projects in dir (a folder of projects like
// C:\laragon\www, or a single project folder).
func (m *Manager) ScanImport(dir string) ([]ImportCandidate, error) {
	if dir == "" {
		return nil, errors.New("choose a folder")
	}
	if !isDir(dir) {
		return nil, fmt.Errorf("folder not found: %s", dir)
	}
	if samePath(dir, m.P.WWW) {
		return nil, errors.New("that is LokalKit's own www folder; its projects are already listed")
	}
	var dirs []string
	if isProjectRoot(dir) {
		dirs = []string{dir}
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				dirs = append(dirs, filepath.Join(dir, e.Name()))
			}
		}
	}
	existing := map[string]Project{}
	for _, p := range m.Projects() {
		existing[p.Host] = p
	}
	cfg := m.config()
	var out []ImportCandidate
	for _, d := range dirs {
		name := filepath.Base(d)
		c := ImportCandidate{Name: name, Path: d, Kind: "php"}
		p, ok := projectFromDir(name, d, cfg)
		if !ok {
			c.Conflict = "folder name can't be a web address (use letters, numbers and -)"
		} else {
			c.Kind, c.Host = p.Kind, p.Host
			if ex, dup := existing[p.Host]; dup {
				if samePath(ex.Path, d) {
					c.Added = true
				} else {
					c.Conflict = "a project with this name already exists"
				}
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// ImportProjects links the given folders and updates their Laravel .env.
func (m *Manager) ImportProjects(paths []string) ([]string, error) {
	cfg := m.config()
	existing := map[string]bool{}
	for _, p := range m.Projects() {
		existing[p.Host] = true
	}
	var added []string
	var problems []string
	for _, path := range paths {
		name := filepath.Base(path)
		p, ok := projectFromDir(name, path, cfg)
		switch {
		case !isDir(path):
			problems = append(problems, name+": folder not found")
			continue
		case !ok:
			problems = append(problems, name+": invalid name for a web address")
			continue
		case existing[p.Host]:
			continue // already added
		}
		cfg.Linked = append(cfg.Linked, LinkedProject{Name: name, Path: path})
		existing[p.Host] = true
		added = append(added, name)
	}
	if len(added) > 0 {
		if _, err := m.SaveConfig(cfg); err != nil {
			return nil, err
		}
		for _, name := range added {
			p := m.findProject(name)
			if err := m.updateImportedEnv(p); err != nil {
				m.Logf("error", "%s: could not update .env: %v", name, err)
			}
			m.Logf("ok", "Imported %s → %s (files stay in %s)", name, firstNonEmpty(p.HTTPS, p.URL), p.Path)
		}
		if err := m.Reload(); err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return added, errors.New(strings.Join(problems, "; "))
	}
	return added, nil
}

// updateImportedEnv points APP_URL at the new address and DB_PORT at
// LokalKit's MySQL, keeping a one-time backup of the original .env.
func (m *Manager) updateImportedEnv(p Project) error {
	envPath := filepath.Join(p.Path, ".env")
	b, err := os.ReadFile(envPath)
	if err != nil {
		return nil // no .env: nothing to do
	}
	backup := envPath + ".before-lokalkit"
	if !exists(backup) {
		if err := os.WriteFile(backup, b, 0o644); err != nil {
			return err
		}
	}
	env := string(b)
	set := map[string]string{"APP_URL": firstNonEmpty(p.HTTPS, p.URL)}
	if v := envValue(env, "DB_HOST"); v == "127.0.0.1" || v == "localhost" {
		set["DB_PORT"] = strconv.Itoa(m.config().MySQLPort)
	}
	if v := envValue(env, "MAIL_HOST"); v == "127.0.0.1" || v == "localhost" {
		set["MAIL_PORT"] = strconv.Itoa(m.config().MailpitSMTPPort)
	}
	return os.WriteFile(envPath, []byte(SetEnvValues(env, set)), 0o644)
}

func envValue(env, key string) string {
	for _, line := range strings.Split(env, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key+"=") {
			return strings.Trim(strings.TrimPrefix(line, key+"="), `"'`)
		}
	}
	return ""
}

// UnlinkProject removes an imported project from LokalKit. Its files are not touched.
func (m *Manager) UnlinkProject(name string) error {
	cfg := m.config()
	var kept []LinkedProject
	found := false
	for _, l := range cfg.Linked {
		if l.Name == name {
			found = true
			continue
		}
		kept = append(kept, l)
	}
	if !found {
		return fmt.Errorf("%s is not an imported project", name)
	}
	cfg.Linked = kept
	if _, err := m.SaveConfig(cfg); err != nil {
		return err
	}
	m.Logf("ok", "Removed %s from LokalKit (its files were not touched)", name)
	return m.Reload()
}

func samePath(a, b string) bool {
	ca, _ := filepath.Abs(a)
	cb, _ := filepath.Abs(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(ca), filepath.Clean(cb))
	}
	return filepath.Clean(ca) == filepath.Clean(cb)
}

// ---------- databases ----------

type LegacyDatabases struct {
	DataDir   string   `json:"dataDir"`
	Databases []string `json:"databases"`
}

var systemSchemas = map[string]bool{"mysql": true, "performance_schema": true, "sys": true, "information_schema": true}

// FindMySQLDataDir finds a MySQL data folder under root/data (Laragon keeps
// e.g. C:\laragon\data\mysql-8.4). The newest-looking one wins.
func FindMySQLDataDir(root string) string {
	base := filepath.Join(root, "data")
	for _, d := range subDirs(base) {
		dir := filepath.Join(base, d)
		if isDir(filepath.Join(dir, "mysql")) && (exists(filepath.Join(dir, "ibdata1")) || exists(filepath.Join(dir, "mysql.ibd"))) {
			return dir
		}
	}
	return ""
}

// ListDatabases lists user databases in a MySQL data folder (one folder per
// database; MySQL encodes special characters as @XXXX).
func ListDatabases(dataDir string) []string {
	entries, _ := os.ReadDir(dataDir)
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() || strings.HasPrefix(n, "#") || systemSchemas[n] {
			continue
		}
		out = append(out, decodeMySQLName(n))
	}
	sort.Strings(out)
	return out
}

func decodeMySQLName(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '@' && i+4 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+5], 16, 32); err == nil {
				b.WriteRune(rune(v))
				i += 4
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// LegacyDatabasesIn lists the databases of the tool installed at root.
func (m *Manager) LegacyDatabasesIn(root string) (LegacyDatabases, error) {
	dir := FindMySQLDataDir(root)
	if dir == "" {
		return LegacyDatabases{}, fmt.Errorf("no MySQL data folder found in %s", filepath.Join(root, "data"))
	}
	return LegacyDatabases{DataDir: dir, Databases: ListDatabases(dir)}, nil
}

// ImportDatabases copies databases from the MySQL data folder of the tool
// at root (e.g. Laragon) into LokalKit's running MySQL.
func (m *Manager) ImportDatabases(root string, dbs []string) error {
	if len(dbs) == 0 {
		return errors.New("choose at least one database")
	}
	mysqlSvc, _ := m.service("mysql")
	if !mysqlSvc.Running() {
		return errors.New("start LokalKit's MySQL first")
	}
	src, err := m.LegacyDatabasesIn(root)
	if err != nil {
		return err
	}
	ourBin := mysqlSvc.(*MySQL).bin()
	ourBinDir := filepath.Dir(ourBin)
	// Prefer the other tool's own mysqld: it matches the data folder's version.
	mysqld := findUnder(filepath.Join(root, "bin", "mysql"), filepath.Join("bin", exeName("mysqld")))
	if mysqld == "" {
		mysqld = ourBin
	}
	dump := filepath.Join(ourBinDir, exeName("mysqldump"))
	client := filepath.Join(ourBinDir, exeName("mysql"))
	for _, f := range []string{dump, client} {
		if !exists(f) {
			return fmt.Errorf("%s not found", f)
		}
	}

	port, err := freeTCPPort()
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(m.P.Tmp, "import-mysql-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	logFile := filepath.Join(m.P.Logs, "import-mysql.log")
	args := []string{
		"--no-defaults",
		"--basedir=" + filepath.Dir(filepath.Dir(mysqld)),
		"--datadir=" + src.DataDir,
		"--port=" + strconv.Itoa(port),
		"--bind-address=127.0.0.1",
		"--loose-mysqlx=OFF",
		"--pid-file=" + filepath.Join(tmp, "mysqld.pid"),
		"--log-error=" + logFile,
	}
	if runtime.GOOS != "windows" {
		args = append(args, "--socket="+filepath.Join(tmp, "mysqld.sock"))
		if os.Geteuid() == 0 {
			args = append(args, "--user=root")
		}
	}
	m.Logf("info", "Opening the databases in %s...", src.DataDir)
	tempProc := NewProc("Import MySQL", nil)
	if err := tempProc.Start(mysqld, args, filepath.Dir(mysqld), envWithPath([]string{filepath.Dir(mysqld)}), filepath.Join(m.P.Logs, "import-mysql-console.log")); err != nil {
		return err
	}
	defer tempProc.Stop(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, filepath.Join(ourBinDir, exeName("mysqladmin")), "-uroot", "-h127.0.0.1", "-P"+strconv.Itoa(port), "shutdown")
		hideWindow(c)
		return c.Run()
	}, 30*time.Second)
	if err := waitPort(port, tempProc.Exited(), 90*time.Second); err != nil {
		b, _ := os.ReadFile(logFile)
		hint := ""
		if strings.Contains(string(b), "lock") || strings.Contains(string(b), "Unable to lock") {
			hint = " Close Laragon (and its MySQL) first."
		}
		return fmt.Errorf("could not open the old databases (%v).%s Details: %s", err, hint, lastLines(string(b), 3))
	}

	m.Logf("info", "Copying %d database(s): %s", len(dbs), strings.Join(dbs, ", "))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	dumpArgs := []string{"-uroot", "-h127.0.0.1", "-P" + strconv.Itoa(port),
		"--single-transaction", "--routines", "--triggers", "--events", "--default-character-set=utf8mb4"}
	if !exists(filepath.Join(ourBinDir, exeName("mariadb-dump"))) {
		dumpArgs = append(dumpArgs, "--set-gtid-purged=OFF") // MySQL only
	}
	dumpArgs = append(append(dumpArgs, "--databases"), dbs...)
	dumpCmd := exec.CommandContext(ctx, dump, dumpArgs...)
	loadCmd := exec.CommandContext(ctx, client, "-uroot", "-h127.0.0.1",
		"-P"+strconv.Itoa(m.config().MySQLPort), "--default-character-set=utf8mb4")
	hideWindow(dumpCmd)
	hideWindow(loadCmd)
	pr, pw := io.Pipe()
	var dumpErr, loadErr bytes.Buffer
	dumpCmd.Stdout, dumpCmd.Stderr = pw, &dumpErr
	loadCmd.Stdin, loadCmd.Stderr = pr, &loadErr
	if err := loadCmd.Start(); err != nil {
		return err
	}
	derr := dumpCmd.Run()
	pw.CloseWithError(derr)
	lerr := loadCmd.Wait()
	if derr != nil {
		return fmt.Errorf("export failed: %v %s", derr, lastLines(dumpErr.String(), 3))
	}
	if lerr != nil {
		return fmt.Errorf("import failed: %v %s", lerr, lastLines(loadErr.String(), 3))
	}
	m.Logf("ok", "Copied database(s): %s", strings.Join(dbs, ", "))
	return nil
}

func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
