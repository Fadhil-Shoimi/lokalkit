package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type MySQL struct {
	m    *Manager
	proc *Proc
}

func (s *MySQL) ID() string { return "mysql" }

func (s *MySQL) bin() string {
	return findUnder(filepath.Join(s.m.P.Bin, "mysql"), filepath.Join("bin", exeName("mysqld")))
}

func (s *MySQL) Info() ServiceInfo {
	bin := s.bin()
	name := "MySQL"
	if bin != "" && s.isMariaDB(filepath.Dir(bin)) {
		name = "MariaDB"
	}
	return ServiceInfo{
		ID: "mysql", Name: name, Installed: bin != "",
		Version: probeVersion(bin, "--version"), Running: s.proc.Running(),
		Ports: fmt.Sprint(s.m.config().MySQLPort), Hint: `bin\mysql\<versi>\bin\mysqld.exe`,
	}
}

func (s *MySQL) Running() bool { return s.proc.Running() }

func (s *MySQL) isMariaDB(binDir string) bool {
	return exists(filepath.Join(binDir, exeName("mariadb-install-db"))) ||
		exists(filepath.Join(binDir, exeName("mysql_install_db")))
}

func (s *MySQL) dataDir() string { return filepath.Join(s.m.P.Data, "mysql") }
func (s *MySQL) iniPath() string { return filepath.Join(s.m.P.Etc, "mysql", "my.ini") }

// ClientBin returns the mysql command line client, if present.
func (s *MySQL) ClientBin() string {
	bin := s.bin()
	if bin == "" {
		return ""
	}
	for _, n := range []string{"mysql", "mariadb"} {
		if p := filepath.Join(filepath.Dir(bin), exeName(n)); exists(p) {
			return p
		}
	}
	return ""
}

func BuildMyIni(baseDir, dataDir, logDir string, port int) string {
	return fmt.Sprintf(`# Dijana oleh LokalKit
[mysqld]
basedir="%s"
datadir="%s"
port=%d
bind-address=127.0.0.1
character-set-server=utf8mb4
collation-server=utf8mb4_unicode_ci
max_allowed_packet=128M
innodb_buffer_pool_size=256M
log-error="%s/mysql_error.log"

[client]
port=%d
default-character-set=utf8mb4
`, slash(baseDir), slash(dataDir), port, slash(logDir), port)
}

func (s *MySQL) Start() error {
	if s.proc.Running() {
		return nil
	}
	bin := s.bin()
	if bin == "" {
		return errors.New("MySQL tidak dijumpai. Letak MySQL/MariaDB dalam folder bin\\mysql")
	}
	port := s.m.config().MySQLPort
	if !PortFree(port) {
		return fmt.Errorf("port %d sedang digunakan (MySQL lain sedang berjalan?)", port)
	}
	binDir := filepath.Dir(bin)
	baseDir := filepath.Dir(binDir)
	if err := os.WriteFile(s.iniPath(), []byte(BuildMyIni(baseDir, s.dataDir(), s.m.P.Logs, port)), 0o644); err != nil {
		return err
	}
	if err := s.initialize(bin, binDir); err != nil {
		return err
	}
	if err := s.proc.Start(bin, []string{"--defaults-file=" + s.iniPath()}, baseDir,
		envWithPath([]string{binDir}), filepath.Join(s.m.P.Logs, "mysql_console.log")); err != nil {
		return err
	}
	if err := waitPort(port, s.proc.Exited(), 60*time.Second); err != nil {
		_ = s.proc.Stop(nil, 0)
		return fmt.Errorf("MySQL gagal dimulakan (%v). Lihat logs\\mysql_error.log", err)
	}
	return nil
}

// initialize creates a fresh data folder the first time (root, no password).
func (s *MySQL) initialize(bin, binDir string) error {
	if entries, _ := os.ReadDir(s.dataDir()); len(entries) > 0 {
		return nil
	}
	s.m.Logf("info", "Menyediakan data MySQL kali pertama (root tanpa kata laluan)...")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var cmd *exec.Cmd
	if s.isMariaDB(binDir) {
		_ = os.MkdirAll(s.dataDir(), 0o755)
		installer := filepath.Join(binDir, exeName("mariadb-install-db"))
		if !exists(installer) {
			installer = filepath.Join(binDir, exeName("mysql_install_db"))
		}
		cmd = exec.CommandContext(ctx, installer, "--datadir="+s.dataDir())
	} else {
		cmd = exec.CommandContext(ctx, bin, "--defaults-file="+s.iniPath(), "--initialize-insecure", "--console")
	}
	cmd.Dir = binDir
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(s.m.P.Logs, "mysql_init.log"), out, 0o644)
	if err != nil {
		_ = os.RemoveAll(s.dataDir())
		return fmt.Errorf("gagal sediakan data MySQL: %s", lastLines(string(out), 3))
	}
	return nil
}

func (s *MySQL) Stop() error {
	graceful := func() error {
		admin := filepath.Join(filepath.Dir(s.bin()), exeName("mysqladmin"))
		if !exists(admin) {
			admin = filepath.Join(filepath.Dir(s.bin()), exeName("mariadb-admin"))
		}
		if !exists(admin) {
			return errors.New("mysqladmin tiada")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, admin, "-uroot", "-h127.0.0.1",
			fmt.Sprintf("-P%d", s.m.config().MySQLPort), "shutdown")
		hideWindow(cmd)
		return cmd.Run()
	}
	return s.proc.Stop(graceful, 20*time.Second)
}

// Exec runs an SQL statement as root using the mysql client.
func (s *MySQL) Exec(sql string) error {
	client := s.ClientBin()
	if client == "" {
		return errors.New("mysql client tidak dijumpai")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client, "-uroot", "-h127.0.0.1",
		fmt.Sprintf("-P%d", s.m.config().MySQLPort), "-e", sql)
	hideWindow(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}
