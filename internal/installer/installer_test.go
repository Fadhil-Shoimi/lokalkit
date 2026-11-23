package installer

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(body))
	}
	w.Close()
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// fakeSites serves look-alikes of windows.php.net, Apache Lounge, MySQL,
// GitHub (Mailpit), getcomposer.org and adminer.org.
func fakeSites(t *testing.T) (*httptest.Server, Sources) {
	php84Zip := makeZip(t, map[string]string{"php.exe": "php84", "php8apache2_4.dll": "dll"})
	phpZip := makeZip(t, map[string]string{"php.exe": "php", "php8apache2_4.dll": "dll", "php.ini-development": ";ini"})
	apacheZip := makeZip(t, map[string]string{"Apache24/bin/httpd.exe": "httpd", "ReadMe.txt": "hi"})
	mysqlZip := makeZip(t, map[string]string{"mysql-8.4.3-winx64/bin/mysqld.exe": "mysqld"})
	mailpitZip := makeZip(t, map[string]string{"mailpit.exe": "mailpit"})
	composer := []byte("<?php // composer")

	mux := http.NewServeMux()
	mux.HandleFunc("/php/releases.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"8.3":{"version":"8.3.99",
			"nts-vs16-x64":{"zip":{"path":"php-8.3.99-nts-Win32-vs16-x64.zip","sha256":"00"}},
			"ts-vs16-x86":{"zip":{"path":"php-8.3.99-Win32-vs16-x86.zip","sha256":"00"}},
			"ts-vs16-x64":{"zip":{"path":"php-8.3.99-Win32-vs16-x64.zip","sha256":"%s"}}},
			"8.4":{"version":"8.4.15",
			"ts-vs17-x64":{"zip":{"path":"php-8.4.15-Win32-vs17-x64.zip","sha256":"%s"}}},
			"8.2":{"version":"8.2.1"}}`, sum(phpZip), sum(php84Zip))
	})
	mux.HandleFunc("/php/php-8.4.15-Win32-vs17-x64.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(php84Zip) })
	mux.HandleFunc("/php/php-8.3.99-Win32-vs16-x64.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(phpZip) })
	mux.HandleFunc("/apache/download/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a href="/download/VS17/binaries/httpd-2.4.9-240101-win64-VS17.zip">old</a>
			<a href="/download/VS17/binaries/httpd-2.4.66-250807-win64-VS17.zip">Apache 2.4.66 Win64</a>
			<a href="/download/VS17/binaries/httpd-2.4.66-250807-win32-VS17.zip">32-bit</a>`)
	})
	mux.HandleFunc("/download/VS17/binaries/httpd-2.4.66-250807-win64-VS17.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(apacheZip) })
	mux.HandleFunc("/download/VS17/binaries/httpd-2.4.66-250807-win64-VS17.zip.sha256", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "SHA256(httpd.zip)= %s\n", sum(apacheZip))
	})
	mux.HandleFunc("/mysql/broken.zip", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/mysql/mysql-8.4.3-winx64.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(mysqlZip) })
	mux.HandleFunc("/mailpit.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(mailpitZip) })
	mux.HandleFunc("/composer.phar", func(w http.ResponseWriter, r *http.Request) { w.Write(composer) })
	mux.HandleFunc("/composer.phar.sha256", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, sum(composer)) })
	nodeZip := makeZip(t, map[string]string{"node-v26.13.1-win-x64/node.exe": "node", "node-v26.13.1-win-x64/node_modules/npm/bin/npm-cli.js": "//"})
	mux.HandleFunc("/node/index.json", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"version":"v27.0.0","lts":false,"files":["win-x64-zip"]},{"version":"v26.13.1","lts":"Lithium","files":["linux-x64","win-x64-zip"]}]`)
	})
	mux.HandleFunc("/node/v26.13.1/SHASUMS256.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "aaaa  node-v26.13.1-linux-x64.tar.gz\n%s  node-v26.13.1-win-x64.zip\n", sum(nodeZip))
	})
	mux.HandleFunc("/node/v26.13.1/node-v26.13.1-win-x64.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(nodeZip) })
	dbeaverZip := makeZip(t, map[string]string{"dbeaver/dbeaver.exe": "dbeaver", "dbeaver/jre/bin/java.exe": "java"})
	mux.HandleFunc("/dbeaver/version.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><version><number>26.2.5</number><date>2026-11-17</date></version>`)
	})
	mux.HandleFunc("/dbeaver/latest.zip", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }) // force the mirror
	mux.HandleFunc("/gh/26.2.5/dbeaver-ce-26.2.5-win32.win32.x86_64.zip", func(w http.ResponseWriter, r *http.Request) { w.Write(dbeaverZip) })
	mux.HandleFunc("/adminer.php", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<?php // adminer") })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u := srv.URL
	return srv, Sources{
		PHPReleasesJSON: u + "/php/releases.json", PHPDownloadBase: u + "/php/", PHPBranch: "8.3",
		ApacheIndex: u + "/apache/download/", ApacheBase: u,
		MySQLVersion: "8.4.3", MySQLURLs: []string{u + "/mysql/broken.zip", u + "/mysql/mysql-8.4.3-winx64.zip"},
		MailpitURL: u + "/mailpit.zip", ComposerURL: u + "/composer.phar", ComposerSHA256: u + "/composer.phar.sha256",
		AdminerURL: u + "/adminer.php", NodeIndex: u + "/node/index.json", NodeBase: u + "/node/",
		DBeaverURL: u + "/dbeaver/latest.zip", DBeaverVersion: u + "/dbeaver/version.xml",
		DBeaverGitHub: u + "/gh/%s/dbeaver-ce-%s-win32.win32.x86_64.zip",
	}
}

func TestResolvers(t *testing.T) {
	_, s := fakeSites(t)
	ctx := context.Background()
	php, err := ResolvePHP(ctx, s)
	if err != nil || php.Version != "8.3.99" || !strings.HasSuffix(php.URLs[0], "php-8.3.99-Win32-vs16-x64.zip") || php.SHA256 == "" {
		t.Errorf("php: %+v %v", php, err)
	}
	ap, err := ResolveApache(ctx, s)
	if err != nil || ap.Version != "2.4.66" || !strings.Contains(ap.URLs[0], "win64") || ap.SHA256 == "" {
		t.Errorf("apache: %+v %v", ap, err)
	}
	s.PHPBranch = "7.4"
	if _, err := ResolvePHP(ctx, s); err == nil {
		t.Error("missing branch should fail")
	}
}

func TestInstallEverything(t *testing.T) {
	_, s := fakeSites(t)
	root := t.TempDir()
	var mu sync.Mutex
	phases := map[string][]string{}
	in := New(root, nil, func(p Progress) {
		mu.Lock()
		phases[p.ID] = append(phases[p.ID], p.Phase)
		mu.Unlock()
	})
	in.sources = &s
	if err := in.Install([]string{"adminer", "composer", "mailpit", "mysql", "php", "apache", "nodejs", "dbeaver"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{
		"bin/apache/httpd-2.4.66/Apache24/bin/httpd.exe",
		"bin/php/php-8.3.99-Win32-vs16-x64/php8apache2_4.dll",
		"bin/mysql/mysql-8.4.3/mysql-8.4.3-winx64/bin/mysqld.exe",
		"bin/mailpit/mailpit/mailpit.exe",
		"bin/composer/composer.phar",
		"etc/apps/adminer/index.php",
		"bin/nodejs/node-v26.13.1-win-x64/node-v26.13.1-win-x64/node_modules/npm/bin/npm-cli.js",
		"bin/dbeaver/dbeaver-26.2.5/dbeaver/jre/bin/java.exe",
	} {
		if !fileExists(filepath.Join(root, filepath.FromSlash(f))) {
			t.Errorf("missing %s", f)
		}
	}
	for _, id := range []string{"apache", "php", "mysql"} {
		got := strings.Join(phases[id], ",")
		if !strings.HasPrefix(got, "resolving") || !strings.HasSuffix(got, "done") || !strings.Contains(got, "extracting") {
			t.Errorf("%s phases: %s", id, got)
		}
	}
	if !strings.Contains(strings.Join(phases["php"], ","), "verifying") {
		t.Error("php should be SHA-256 verified")
	}
	// Leftover downloads are cleaned up.
	if left, _ := os.ReadDir(filepath.Join(root, "tmp", "downloads")); len(left) != 0 {
		t.Errorf("downloads not cleaned: %v", left)
	}
	// Installing again replaces cleanly.
	if err := in.Install([]string{"php"}); err != nil {
		t.Fatal(err)
	}
}

func TestChecksumMismatch(t *testing.T) {
	_, s := fakeSites(t)
	root := t.TempDir()
	in := New(root, nil, nil)
	in.sources = &s
	in.resolvers["php"] = func(ctx context.Context, s Sources) (Artifact, error) {
		a, err := ResolvePHP(ctx, s)
		a.SHA256 = strings.Repeat("ab", 32)
		return a, err
	}
	err := in.Install([]string{"php"})
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("got %v", err)
	}
	if fileExists(filepath.Join(root, "bin", "php", "php-8.3.99-Win32-vs16-x64")) {
		t.Error("corrupted download must not be installed")
	}
}

func TestZipSlip(t *testing.T) {
	dir := t.TempDir()
	evil := filepath.Join(dir, "evil.zip")
	os.WriteFile(evil, makeZip(t, map[string]string{"../../escaped.txt": "x"}), 0o644)
	if err := extractZip(evil, filepath.Join(dir, "out")); err == nil {
		t.Fatal("zip slip not blocked")
	}
	if fileExists(filepath.Join(dir, "..", "escaped.txt")) {
		t.Fatal("file escaped")
	}
}

func TestHTMLInsteadOfFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html>please log in</html>")
	}))
	defer srv.Close()
	in := New(t.TempDir(), nil, nil)
	err := in.downloadURL(context.Background(), "x", "1", srv.URL+"/file.zip", filepath.Join(t.TempDir(), "f"))
	if err == nil || !strings.Contains(err.Error(), "web page") {
		t.Fatalf("got %v", err)
	}
}

func TestManifestOverride(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"phpBranch":"8.4","mysqlVersion":"8.4.9","mysqlUrls":["https://example/m.zip"]}`)
	}))
	defer srv.Close()
	s := LoadSources(context.Background(), srv.URL)
	if s.PHPBranch != "8.4" || s.MySQLVersion != "8.4.9" || len(s.MySQLURLs) != 1 || s.ApacheIndex == "" {
		t.Errorf("%+v", s)
	}
	if d := LoadSources(context.Background(), "http://127.0.0.1:1/none"); d.PHPBranch != "8.3" {
		t.Error("unreachable manifest should keep defaults")
	}
}

func TestResolveNode(t *testing.T) {
	_, s := fakeSites(t)
	a, err := ResolveNode(context.Background(), s)
	if err != nil || a.Version != "26.13.1" || a.SHA256 == "" || !strings.HasSuffix(a.URLs[0], "/node/v26.13.1/node-v26.13.1-win-x64.zip") {
		t.Fatalf("%+v %v", a, err)
	}
}

// TestResolveRealSources checks resolvers against the real websites.
// Run with LOKALKIT_NET=1 (needs internet access).
func TestResolveRealSources(t *testing.T) {
	if os.Getenv("LOKALKIT_NET") == "" {
		t.Skip("set LOKALKIT_NET=1")
	}
	a, err := ResolveNode(context.Background(), DefaultSources())
	if err != nil || a.SHA256 == "" {
		t.Fatalf("node: %+v %v", a, err)
	}
	t.Logf("node: %s %s", a.Version, a.URLs[0])
}

func TestDBeaverDetected(t *testing.T) {
	root := t.TempDir()
	exe := "dbeaver"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	os.MkdirAll(filepath.Join(root, "bin", "dbeaver", "dbeaver-26.2.5", "dbeaver"), 0o755)
	os.WriteFile(filepath.Join(root, "bin", "dbeaver", "dbeaver-26.2.5", "dbeaver", exe), nil, 0o644)
	if ok, v := appInstalled(root, "dbeaver", "dbeaver", "dbeaver-"); !ok || v != "26.2.5" {
		t.Fatalf("got %v %q", ok, v)
	}
}

func TestPHPReleasesAndBranchInstall(t *testing.T) {
	_, s := fakeSites(t)
	in := New(t.TempDir(), nil, nil)
	in.sources = &s
	rel, err := in.PHPReleases(context.Background())
	if err != nil || len(rel) != 2 || rel[0].Branch != "8.4" || rel[0].Version != "8.4.15" || rel[1].Version != "8.3.99" {
		t.Fatalf("releases: %+v %v", rel, err)
	}
	name, err := in.InstallPHP("8.4")
	if err != nil || name != "php-8.4.15-Win32-vs17-x64" {
		t.Fatalf("install: %q %v", name, err)
	}
	if !fileExists(filepath.Join(in.Root, "bin", "php", name, "php.exe")) {
		t.Fatal("PHP 8.4 not installed")
	}
	if in.phpBranch != "" {
		t.Error("branch override should be cleared")
	}
	if CompareVersions("8.3.10", "8.3.9") != 1 || CompareVersions("8.3.9", "8.3.9") != 0 || CompareVersions("8.3", "8.4.0") != -1 {
		t.Error("CompareVersions")
	}
}
