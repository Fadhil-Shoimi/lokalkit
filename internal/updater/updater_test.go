package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true}, {"0.1.0", "0.1.0", false}, {"0.1.0", "0.2.0", false},
		{"0.1.0", "0.1.0-dev", true}, {"0.1.0-dev", "0.1.0", false}, {"v1.0.0", "0.9.9", true},
		{"0.10.0", "0.9.0", true}, {"0.1.1", "0.1.0-dev+abc", true},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q,%q)=%v", c.a, c.b, got)
		}
	}
}

func fakeGitHub(t *testing.T, exe []byte, withSHA bool, badSHA bool) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest":
			assets := fmt.Sprintf(`{"name":"LokalKit.exe","browser_download_url":"%s/dl/LokalKit.exe","size":%d}`, srv.URL, len(exe))
			if withSHA {
				assets += fmt.Sprintf(`,{"name":"LokalKit.exe.sha256","browser_download_url":"%s/dl/LokalKit.exe.sha256","size":64}`, srv.URL)
			}
			fmt.Fprintf(w, `{"tag_name":"v0.2.0","body":"- Databases tab","html_url":"https://example/r","assets":[%s]}`, assets)
		case "/dl/LokalKit.exe":
			w.Write(exe)
		case "/dl/LokalKit.exe.sha256":
			sum := sha256.Sum256(exe)
			s := hex.EncodeToString(sum[:])
			if badSHA {
				s = strings.Repeat("0", 64)
			}
			fmt.Fprintf(w, "%s  LokalKit.exe\n", s)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestAndDownload(t *testing.T) {
	exe := []byte("MZ new lokalkit")
	srv := fakeGitHub(t, exe, true, false)
	rel, err := Latest(context.Background(), srv.URL+"/latest")
	if err != nil || rel.Version != "0.2.0" || rel.SHA256 == "" || rel.Notes != "- Databases tab" {
		t.Fatalf("%+v %v", rel, err)
	}
	dest := filepath.Join(t.TempDir(), "new.exe")
	var last int64
	if err := Download(context.Background(), rel, dest, func(r, total int64) { last = r }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != string(exe) || last != int64(len(exe)) {
		t.Error("download content/progress")
	}
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	srv := fakeGitHub(t, []byte("MZ tampered"), true, true)
	rel, err := Latest(context.Background(), srv.URL+"/latest")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "new.exe")
	if err := Download(context.Background(), rel, dest, nil); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("bad download must be removed")
	}
}

func TestLatestErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := Latest(context.Background(), srv.URL); err == nil || !strings.Contains(err.Error(), "public") {
		t.Errorf("404 should explain the repository must be public: %v", err)
	}
}

func TestReplace(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "LokalKit.exe")
	newExe := filepath.Join(dir, "tmp", "LokalKit-new.exe")
	os.MkdirAll(filepath.Dir(newExe), 0o755)
	os.WriteFile(cur, []byte("old"), 0o755)
	os.WriteFile(newExe, []byte("new"), 0o755)
	if err := Replace(cur, newExe); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cur); string(b) != "new" {
		t.Error("exe not replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "LokalKit.old.exe")); string(b) != "old" {
		t.Error("old exe should be kept until cleanup")
	}
	Cleanup(cur)
	if _, err := os.Stat(filepath.Join(dir, "LokalKit.old.exe")); err == nil {
		t.Error("cleanup")
	}
	// A failed install puts the original back.
	os.WriteFile(cur, []byte("v2"), 0o755)
	if err := Replace(cur, filepath.Join(dir, "missing.exe")); err == nil {
		t.Fatal("expected failure")
	}
	if b, _ := os.ReadFile(cur); string(b) != "v2" {
		t.Error("original not restored")
	}
}
