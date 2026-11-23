package core

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Databases tab: list, create, export (backup), import and delete
// databases in LokalKit's MySQL, and spot projects whose database is missing.

type DatabaseInfo struct {
	Name     string   `json:"name"`
	SizeMB   float64  `json:"sizeMb"`
	Tables   int      `json:"tables"`
	Projects []string `json:"projects"` // projects whose .env uses it
}

type MissingDatabase struct {
	Project  string `json:"project"`
	Database string `json:"database"`
	Laravel  bool   `json:"laravel"`
}

// existingDBNameRe accepts names of databases that may already exist (e.g.
// "kedai-runcit" from Laragon); they are always backtick-quoted in SQL.
var existingDBNameRe = regexp.MustCompile(`^[A-Za-z0-9_$\-]{1,64}$`)

func checkDBName(name string) error {
	if !existingDBNameRe.MatchString(name) {
		return fmt.Errorf("invalid database name %q", name)
	}
	if systemSchemas[strings.ToLower(name)] {
		return fmt.Errorf("%s is a MySQL system database", name)
	}
	return nil
}

func (m *Manager) mysqlRunning() (*MySQL, error) {
	svc, _ := m.service("mysql")
	my := svc.(*MySQL)
	if !my.Info().Installed {
		return nil, errors.New("MySQL is not installed. Install it from the Install tab")
	}
	if !my.Running() {
		return nil, errors.New("MySQL is not running")
	}
	return my, nil
}

// Query runs SQL and returns tab-separated rows without a header.
func (s *MySQL) Query(sql string) ([][]string, error) {
	client := s.ClientBin()
	if client == "" {
		return nil, errors.New("mysql client not found")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, client, "-uroot", "-h127.0.0.1",
		fmt.Sprintf("-P%d", s.m.config().MySQLPort), "-N", "-B", "-e", sql)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\r\n"), "\n") {
		if line = strings.TrimRight(line, "\r"); line != "" {
			rows = append(rows, strings.Split(line, "\t"))
		}
	}
	return rows, nil
}

// projectDatabases maps database name -> projects whose .env points at
// LokalKit's MySQL with that DB_DATABASE.
func (m *Manager) projectDatabases() (map[string][]string, map[string]bool) {
	uses := map[string][]string{}
	laravel := map[string]bool{}
	for _, p := range m.Projects() {
		b, err := os.ReadFile(filepath.Join(p.Path, ".env"))
		if err != nil {
			continue
		}
		env := string(b)
		if envValue(env, "DB_CONNECTION") != "mysql" && envValue(env, "DB_CONNECTION") != "mariadb" {
			continue
		}
		if h := envValue(env, "DB_HOST"); h != "" && h != "127.0.0.1" && h != "localhost" {
			continue // a remote database, not ours
		}
		if db := envValue(env, "DB_DATABASE"); db != "" {
			uses[db] = append(uses[db], p.Name)
			laravel[p.Name] = p.Kind == "laravel"
		}
	}
	return uses, laravel
}

// ListDatabases returns user databases with size, table count and the
// projects using them.
func (m *Manager) ListDatabases() ([]DatabaseInfo, error) {
	my, err := m.mysqlRunning()
	if err != nil {
		return nil, err
	}
	rows, err := my.Query(`SELECT s.schema_name, COALESCE(SUM(t.data_length + t.index_length), 0), COUNT(t.table_name)
FROM information_schema.schemata s LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name
WHERE s.schema_name NOT IN ('mysql','sys','performance_schema','information_schema')
GROUP BY s.schema_name ORDER BY s.schema_name`)
	if err != nil {
		return nil, err
	}
	uses, _ := m.projectDatabases()
	return ParseDatabaseRows(rows, uses), nil
}

// ParseDatabaseRows turns query rows (name, bytes, tables) into DatabaseInfo.
func ParseDatabaseRows(rows [][]string, uses map[string][]string) []DatabaseInfo {
	var out []DatabaseInfo
	for _, r := range rows {
		if len(r) < 3 {
			continue
		}
		size, _ := strconv.ParseFloat(r[1], 64)
		tables, _ := strconv.Atoi(r[2])
		projects := uses[r[0]]
		sort.Strings(projects)
		out = append(out, DatabaseInfo{Name: r[0], SizeMB: float64(int(size/1048576*10+0.5)) / 10, Tables: tables, Projects: projects})
	}
	return out
}

// MissingDatabases lists projects whose DB_DATABASE doesn't exist in MySQL.
func (m *Manager) MissingDatabases() []MissingDatabase {
	my, err := m.mysqlRunning()
	if err != nil {
		return nil
	}
	rows, err := my.Query("SELECT schema_name FROM information_schema.schemata")
	if err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, r := range rows {
		have[strings.ToLower(r[0])] = true
	}
	uses, laravel := m.projectDatabases()
	var out []MissingDatabase
	for db, projects := range uses {
		if have[strings.ToLower(db)] {
			continue
		}
		for _, p := range projects {
			out = append(out, MissingDatabase{Project: p, Database: db, Laravel: laravel[p]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}

// CreateProjectDatabase creates the database a project's .env expects and,
// for Laravel, runs its migrations.
func (m *Manager) CreateProjectDatabase(project string) error {
	var target MissingDatabase
	for _, md := range m.MissingDatabases() {
		if md.Project == project {
			target = md
		}
	}
	if target.Database == "" {
		return fmt.Errorf("%s has no missing database", project)
	}
	if err := checkDBName(target.Database); err != nil {
		return err
	}
	my, err := m.ensureMySQL()
	if err != nil {
		return err
	}
	if err := my.Exec("CREATE DATABASE IF NOT EXISTS `" + target.Database + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		return err
	}
	m.Logf("ok", "Database %s created for %s", target.Database, project)
	if target.Laravel {
		p := m.findProject(project)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		cmd, err := m.artisanCmd(ctx, p.Path, "migrate", "--force", "--no-interaction")
		if err != nil {
			return err
		}
		if err := m.runLogged(project, cmd, filepath.Join(m.P.Logs, "composer-"+project+".log")); err != nil {
			return fmt.Errorf("database created, but migrations failed: %w", err)
		}
		m.tasks.set(project, "")
		m.Logf("ok", "Migrations ran for %s", project)
	}
	return nil
}

func (m *Manager) backupDir() string { return filepath.Join(m.P.Root, "backups") }

// ExportDatabase writes a full .sql dump to backups/<db>-<time>.sql.
func (m *Manager) ExportDatabase(name string) (string, error) {
	if err := checkDBName(name); err != nil {
		return "", err
	}
	my, err := m.mysqlRunning()
	if err != nil {
		return "", err
	}
	dump := filepath.Join(filepath.Dir(my.bin()), exeName("mysqldump"))
	if !exists(dump) {
		dump = filepath.Join(filepath.Dir(my.bin()), exeName("mariadb-dump"))
	}
	if !exists(dump) {
		return "", errors.New("mysqldump not found")
	}
	if err := os.MkdirAll(m.backupDir(), 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(m.backupDir(), fmt.Sprintf("%s-%s.sql", name, time.Now().Format("20060102-150405")))
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	args := []string{"-uroot", "-h127.0.0.1", fmt.Sprintf("-P%d", m.config().MySQLPort),
		"--single-transaction", "--routines", "--triggers", "--events", "--default-character-set=utf8mb4"}
	if !strings.Contains(filepath.Base(dump), "mariadb") {
		args = append(args, "--set-gtid-purged=OFF")
	}
	// Just the database name (no --databases): the dump then has no
	// CREATE DATABASE/USE, so it can be restored into any database name.
	args = append(args, name)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	cmd := exec.CommandContext(ctx, dump, args...)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = f, &stderr
	err = cmd.Run()
	f.Close()
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("export failed: %v %s", err, lastLines(stderr.String(), 3))
	}
	m.Logf("ok", "Exported %s to %s", name, path)
	return path, nil
}

// ImportSQL runs a .sql (or .sql.gz) file into a database, creating it if needed.
func (m *Manager) ImportSQL(name, file string) error {
	if err := checkDBName(name); err != nil {
		return err
	}
	my, err := m.mysqlRunning()
	if err != nil {
		return err
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	var in io.Reader = f
	if strings.HasSuffix(strings.ToLower(file), ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return fmt.Errorf("not a valid .gz file: %w", err)
		}
		defer gz.Close()
		in = gz
	}
	if err := my.Exec("CREATE DATABASE IF NOT EXISTS `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci"); err != nil {
		return err
	}
	m.Logf("info", "Importing %s into %s...", filepath.Base(file), name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	cmd := exec.CommandContext(ctx, my.ClientBin(), "-uroot", "-h127.0.0.1",
		fmt.Sprintf("-P%d", m.config().MySQLPort), "--default-character-set=utf8mb4", name)
	hideWindow(cmd)
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stderr = in, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("import failed: %v %s", err, lastLines(stderr.String(), 3))
	}
	m.Logf("ok", "Imported %s into %s", filepath.Base(file), name)
	return nil
}

// DropDatabase deletes a database and everything in it.
func (m *Manager) DropDatabase(name string) error {
	if err := checkDBName(name); err != nil {
		return err
	}
	my, err := m.mysqlRunning()
	if err != nil {
		return err
	}
	if err := my.Exec("DROP DATABASE `" + name + "`"); err != nil {
		return err
	}
	m.Logf("ok", "Deleted database %s", name)
	return nil
}
