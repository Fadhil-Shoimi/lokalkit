package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeLaragon makes C:\laragon-like folders with a few projects.
func fakeLaragon(t *testing.T) string {
	root := t.TempDir()
	www := filepath.Join(root, "www")
	os.MkdirAll(filepath.Join(www, "shop", "public"), 0o755)
	os.WriteFile(filepath.Join(www, "shop", "artisan"), nil, 0o644)
	os.WriteFile(filepath.Join(www, "shop", ".env"), []byte("APP_NAME=Shop\nAPP_URL=http://shop.test\nDB_HOST=127.0.0.1\nDB_PORT=3306\nDB_DATABASE=shop\n"), 0o644)
	os.MkdirAll(filepath.Join(www, "blog"), 0o755)
	os.WriteFile(filepath.Join(www, "blog", "wp-config.php"), nil, 0o644)
	os.MkdirAll(filepath.Join(www, "Bad Name!"), 0o755)
	os.WriteFile(filepath.Join(www, "index.php"), []byte("<?php // laragon home"), 0o644)
	return root
}

func TestImportProjects(t *testing.T) {
	m := newTestManager(t)
	lar := fakeLaragon(t)
	os.MkdirAll(filepath.Join(m.P.WWW, "blog"), 0o755) // name clash with the Laragon blog

	cands, err := m.ScanImport(filepath.Join(lar, "www"))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ImportCandidate{}
	for _, c := range cands {
		byName[c.Name] = c
	}
	if len(cands) != 3 || byName["shop"].Kind != "laravel" || byName["shop"].Conflict != "" ||
		byName["blog"].Conflict == "" || byName["Bad Name!"].Conflict == "" {
		t.Fatalf("candidates: %+v", cands)
	}

	added, err := m.ImportProjects([]string{byName["shop"].Path})
	if err != nil || len(added) != 1 {
		t.Fatalf("import: %v %v", added, err)
	}
	p := m.findProject("shop")
	if !p.Linked || p.Path != byName["shop"].Path || p.HTTPS != "https://shop.test" {
		t.Fatalf("linked project: %+v", p)
	}
	env, _ := os.ReadFile(filepath.Join(p.Path, ".env"))
	if !strings.Contains(string(env), "APP_URL=https://shop.test") || !strings.Contains(string(env), "DB_PORT=3306") {
		t.Errorf(".env: %s", env)
	}
	if !exists(filepath.Join(p.Path, ".env.before-lokalkit")) {
		t.Error("backup missing")
	}
	if !exists(filepath.Join(m.P.Etc, "apache", "sites-enabled", "auto.shop.test.conf")) {
		t.Error("vhost not written for the linked project")
	}
	// Survives a restart (saved in lokalkit.json).
	m2, _ := NewManager(m.P.Root, nil)
	if !m2.findProject("shop").Linked {
		t.Error("linked project not persisted")
	}
	// Scanning again shows it as added; importing twice does nothing.
	cands, _ = m.ScanImport(filepath.Join(lar, "www"))
	for _, c := range cands {
		if c.Name == "shop" && !c.Added {
			t.Error("should show as already added")
		}
	}
	if added, _ := m.ImportProjects([]string{p.Path}); len(added) != 0 {
		t.Error("duplicate import")
	}
	// A single project folder can be imported directly too.
	if c, _ := m.ScanImport(filepath.Join(lar, "www", "shop")); len(c) != 1 {
		t.Errorf("single folder scan: %+v", c)
	}
	if err := m.UnlinkProject("shop"); err != nil {
		t.Fatal(err)
	}
	if m.findProject("shop").Path != "" || !exists(filepath.Join(lar, "www", "shop", "artisan")) {
		t.Error("unlink should remove it from LokalKit but keep the files")
	}
}

func TestListDatabases(t *testing.T) {
	root := t.TempDir()
	dd := filepath.Join(root, "data", "mysql-8.4")
	for _, d := range []string{"mysql", "sys", "performance_schema", "#innodb_redo", "shop", "kedai@002druncit"} {
		os.MkdirAll(filepath.Join(dd, d), 0o755)
	}
	os.WriteFile(filepath.Join(dd, "mysql.ibd"), nil, 0o644)
	if got := FindMySQLDataDir(root); got != dd {
		t.Fatalf("data dir %q", got)
	}
	if got := strings.Join(ListDatabases(dd), ","); got != "kedai-runcit,shop" {
		t.Errorf("databases: %s", got)
	}
}

// TestImportDatabasesIntegration copies a database from a "Laragon" MySQL
// data folder into LokalKit's MySQL using real mysqld processes.
// Run with LOKALKIT_IT=1 on Linux with mysql-server installed.
func TestImportDatabasesIntegration(t *testing.T) {
	if os.Getenv("LOKALKIT_IT") == "" || runtime.GOOS != "linux" {
		t.Skip("set LOKALKIT_IT=1 on Linux with mysql-server")
	}
	if _, err := exec.LookPath("mysqld"); err != nil {
		if _, err := os.Stat("/usr/sbin/mysqld"); err != nil {
			t.Skip("mysqld not installed")
		}
	}
	layout := func(root string) string {
		dir := filepath.Join(root, "bin", "mysql", "mysql-8.0", "bin")
		os.MkdirAll(dir, 0o755)
		os.Symlink("/usr/sbin/mysqld", filepath.Join(dir, "mysqld"))
		for _, c := range []string{"mysql", "mysqldump", "mysqladmin"} {
			os.Symlink("/usr/bin/"+c, filepath.Join(dir, c))
		}
		os.Symlink("/usr/share", filepath.Join(root, "bin", "mysql", "mysql-8.0", "share")) // share/mysql/errmsg.sys
		return dir
	}

	// 1) LokalKit with its own MySQL.
	m := newTestManager(t)
	layout(m.P.Root)
	cfg := m.Config()
	cfg.MySQLPort = freePort(t)
	m.SaveConfig(cfg)
	if err := m.Start("mysql"); err != nil {
		b, _ := os.ReadFile(filepath.Join(m.P.Logs, "mysql_error.log"))
		t.Fatalf("LokalKit MySQL: %v\n%s", err, lastLines(string(b), 10))
	}
	t.Cleanup(func() { m.Stop("mysql") })

	// 2) A "Laragon" with an existing database.
	lar := t.TempDir()
	lbin := layout(lar)
	ldata := filepath.Join(lar, "data", "mysql-8.0")
	os.MkdirAll(filepath.Dir(ldata), 0o755)
	run := func(name string, args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
	}
	run(filepath.Join(lbin, "mysqld"), "--no-defaults", "--initialize-insecure", "--user=root",
		"--basedir="+filepath.Dir(lbin), "--datadir="+ldata)
	lport := freePort(t)
	srv := exec.Command(filepath.Join(lbin, "mysqld"), "--no-defaults", "--user=root", "--basedir="+filepath.Dir(lbin),
		"--datadir="+ldata, "--port="+strconv.Itoa(lport), "--bind-address=127.0.0.1", "--mysqlx=OFF",
		"--socket="+filepath.Join(lar, "s.sock"), "--pid-file="+filepath.Join(lar, "p.pid"))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	if err := waitPort(lport, nil, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	run("mysql", "-uroot", "-h127.0.0.1", "-P"+strconv.Itoa(lport), "-e",
		"CREATE DATABASE `kedai-runcit`; CREATE TABLE `kedai-runcit`.items (id INT PRIMARY KEY, name VARCHAR(50)); INSERT INTO `kedai-runcit`.items VALUES (1,'Gula'),(2,'Kopi');")
	run("mysqladmin", "-uroot", "-h127.0.0.1", "-P"+strconv.Itoa(lport), "shutdown")
	srv.Wait()

	// 3) Import it.
	dbs, err := m.LegacyDatabasesIn(lar)
	if err != nil || strings.Join(dbs.Databases, ",") != "kedai-runcit" {
		t.Fatalf("legacy dbs: %+v %v", dbs, err)
	}
	if err := m.ImportDatabases(lar, dbs.Databases); err != nil {
		b, _ := os.ReadFile(filepath.Join(m.P.Logs, "import-mysql.log"))
		t.Fatalf("import: %v\n%s", err, lastLines(string(b), 10))
	}
	out, err := exec.Command("mysql", "-uroot", "-h127.0.0.1", "-P"+strconv.Itoa(cfg.MySQLPort), "-N", "-e",
		"SELECT GROUP_CONCAT(name ORDER BY id) FROM `kedai-runcit`.items").CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "Gula,Kopi" {
		t.Fatalf("rows in LokalKit MySQL: %q %v", out, err)
	}
	// The old data folder was closed again (no leftover server).
	if !PortFree(lport) {
		t.Error("legacy server still running")
	}
}
