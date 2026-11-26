package main

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"lokalkit/internal/core"
)

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

func plain(s string) string { return ansi.ReplaceAllString(s, "") }

func lkTestApp(t *testing.T) (*App, string, *[]string) {
	t.Helper()
	root := t.TempDir()
	for _, v := range []string{"php-8.6.0", "php-8.4.15"} {
		d := filepath.Join(root, "bin", "php", v)
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "php"), nil, 0o755)
		os.WriteFile(filepath.Join(d, "php-cgi"), nil, 0o755)
		os.WriteFile(filepath.Join(d, "php.ini"), []byte("; test\n"), 0o644)
	}
	q := filepath.Join(root, "www", "qistas")
	os.MkdirAll(filepath.Join(q, "public"), 0o755)
	os.MkdirAll(filepath.Join(q, "storage", "logs"), 0o755)
	var log strings.Builder
	for i := 1; i <= 60; i++ {
		log.WriteString("line " + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "\n")
	}
	os.WriteFile(filepath.Join(q, "storage", "logs", "laravel.log"), []byte(log.String()), 0o644)
	m, err := core.NewManager(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	var opened []string
	a := &App{m: m, openURL: func(u string) { opened = append(opened, u) }}
	return a, q, &opened
}

func TestLKCommands(t *testing.T) {
	a, q, opened := lkTestApp(t)
	run := func(dir string, args ...string) string {
		t.Helper()
		out, err := a.runLK(dir, args)
		if err != nil {
			t.Fatalf("lk %v: %v", args, err)
		}
		return plain(out)
	}
	if out := run(q); !strings.Contains(out, "lk open") || !strings.Contains(out, "lk php") {
		t.Errorf("help: %s", out)
	}
	if out := run(filepath.Join(q, "app"), "url"); out != "https://qistas.test\n" {
		t.Errorf("url from a subfolder: %q", out)
	}
	run(q, "open")
	if len(*opened) != 1 || (*opened)[0] != "https://qistas.test" {
		t.Errorf("open: %v", *opened)
	}
	out := run(q, "log", "5")
	if !strings.HasSuffix(out, "line 56\nline 57\nline 58\nline 59\nline 60\n") || strings.Contains(out, "line 55") {
		t.Errorf("log 5: %q", out)
	}
	if out := run(q, "php"); !strings.Contains(out, "qistas uses PHP 8.6.0 (default)") || !strings.Contains(out, "8.6, 8.4") {
		t.Errorf("php: %s", out)
	}
	if out := run(q, "php", "8.4"); !strings.Contains(out, "qistas now uses PHP 8.4.15") {
		t.Errorf("php 8.4: %s", out)
	}
	if a.m.Config().ProjectPHP["qistas"] != "8.4" {
		t.Error("pin not saved")
	}
	if out := run(q, "status"); !strings.Contains(out, "qistas  https://qistas.test") || !strings.Contains(out, "PHP 8.4.15 (pinned)") {
		t.Errorf("status: %s", out)
	}
	if out := run(q, "php", "default"); !strings.Contains(out, "now uses PHP 8.6.0") {
		t.Errorf("php default: %s", out)
	}
	if out := run(a.m.P.Root, "projects"); !strings.Contains(out, "qistas") {
		t.Errorf("projects: %s", out)
	}
	// Errors are clear.
	if _, err := a.runLK(a.m.P.Root, []string{"url"}); err == nil || !strings.Contains(err.Error(), "not inside a LokalKit project") {
		t.Errorf("url outside a project: %v", err)
	}
	if _, err := a.runLK(q, []string{"php", "7.4"}); err == nil {
		t.Error("php 7.4 should fail (not installed)")
	}
	if _, err := a.runLK(q, []string{"dev"}); err == nil {
		t.Error("lk dev on a Laravel project should fail")
	}
	if _, err := a.runLK(q, []string{"start", "nginx"}); err == nil {
		t.Error("unknown service should fail")
	}
	if _, err := a.runLK(q, []string{"frobnicate"}); err == nil || !strings.Contains(err.Error(), "lk help") {
		t.Errorf("unknown command: %v", err)
	}
}

func TestServiceIDs(t *testing.T) {
	if ids, _ := serviceIDs(nil); strings.Join(ids, ",") != "mysql,mailpit,apache" {
		t.Errorf("all: %v", ids)
	}
	if ids, _ := serviceIDs([]string{"web", "DB"}); strings.Join(ids, ",") != "apache,mysql" {
		t.Errorf("aliases: %v", ids)
	}
}

// The endpoint answers lk.cmd's exact curl call, and refuses anything without the token.
func TestLKEndpoint(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl not installed")
	}
	a, q, _ := lkTestApp(t)
	srv := httptest.NewServer(a.lkHandler("secret-token"))
	defer srv.Close()
	call := func(token string, args string) string {
		out, _ := exec.Command(curl, "-s", "-X", "POST", srv.URL+"/lk", "-H", "X-LK-Token: "+token,
			"--data-urlencode", "dir="+q, "--data-urlencode", "args="+args).Output()
		return plain(string(out))
	}
	if out := call("secret-token", "url"); out != "https://qistas.test\n" {
		t.Errorf("url: %q", out)
	}
	if out := call("secret-token", "php 9.9"); !strings.Contains(out, "✗ PHP 9.9 is not installed") {
		t.Errorf("error output: %q", out)
	}
	if out := call("wrong", "url"); !strings.Contains(out, "forbidden") {
		t.Errorf("bad token should be refused: %q", out)
	}
}
