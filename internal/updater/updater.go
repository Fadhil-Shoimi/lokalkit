// Package updater checks GitHub Releases for a newer LokalKit and replaces
// the running LokalKit.exe with it.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FeedURL is the GitHub "latest release" API endpoint LokalKit checks. The
// repository (or a separate releases repository) must be public.
const FeedURL = "https://api.github.com/repos/Fadhil-Shoimi/lokalkit/releases/latest"

const assetName = "LokalKit.exe"

type Release struct {
	Version  string `json:"version"` // "0.2.0"
	Tag      string `json:"tag"`
	Notes    string `json:"notes"`
	PageURL  string `json:"pageUrl"`
	AssetURL string `json:"-"`
	SHA256   string `json:"-"` // from LokalKit.exe.sha256, when published
	Size     int64  `json:"size"`
}

var client = &http.Client{Timeout: 30 * time.Second}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "LokalKit-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	return client.Do(req)
}

// Latest reads the newest published release from feed.
func Latest(ctx context.Context, feed string) (*Release, error) {
	resp, err := get(ctx, feed)
	if err != nil {
		return nil, fmt.Errorf("cannot reach the update server: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
	case 404:
		return nil, errors.New("no published release found (the repository must be public and have a release)")
	default:
		return nil, fmt.Errorf("update server answered HTTP %d", resp.StatusCode)
	}
	var gh struct {
		Tag    string `json:"tag_name"`
		Body   string `json:"body"`
		HTML   string `json:"html_url"`
		Draft  bool   `json:"draft"`
		Pre    bool   `json:"prerelease"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&gh); err != nil {
		return nil, fmt.Errorf("unexpected update information: %w", err)
	}
	if gh.Draft || gh.Pre {
		return nil, errors.New("latest release is not final")
	}
	rel := &Release{Version: strings.TrimPrefix(gh.Tag, "v"), Tag: gh.Tag, Notes: gh.Body, PageURL: gh.HTML}
	shaURL := ""
	for _, a := range gh.Assets {
		switch strings.ToLower(a.Name) {
		case strings.ToLower(assetName):
			rel.AssetURL, rel.Size = a.URL, a.Size
		case strings.ToLower(assetName) + ".sha256":
			shaURL = a.URL
		}
	}
	if rel.AssetURL == "" {
		return nil, fmt.Errorf("release %s has no %s yet (it may still be building)", gh.Tag, assetName)
	}
	if shaURL != "" {
		if r, err := get(ctx, shaURL); err == nil {
			b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			r.Body.Close()
			if m := regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`).FindString(string(b)); m != "" {
				rel.SHA256 = strings.ToLower(m)
			}
		}
	}
	return rel, nil
}

// Newer reports whether version a is newer than b. Pre-release suffixes
// ("0.1.0-dev") count as older than the plain version.
func Newer(a, b string) bool { return compare(a, b) > 0 }

func compare(a, b string) int {
	na, pa := split(a)
	nb, pb := split(b)
	for i := 0; i < 3; i++ {
		if na[i] != nb[i] {
			if na[i] < nb[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa == pb:
		return 0
	case pa == "":
		return 1 // 0.2.0 > 0.2.0-dev
	case pb == "":
		return -1
	}
	return strings.Compare(pa, pb)
}

func split(v string) ([3]int, string) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	pre := ""
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v, pre = v[:i], v[i+1:]
	}
	var n [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		n[i], _ = strconv.Atoi(p)
	}
	return n, pre
}

// Download fetches the release exe into dest and verifies its SHA-256.
func Download(ctx context.Context, rel *Release, dest string, progress func(received, total int64)) error {
	resp, err := get(ctx, rel.AssetURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	h := sha256.New()
	total := resp.ContentLength
	var got int64
	buf := make([]byte, 128<<10)
	last := time.Time{}
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			f.Write(buf[:n])
			h.Write(buf[:n])
			got += int64(n)
			if progress != nil && time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				progress(got, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			os.Remove(dest)
			return rerr
		}
	}
	f.Close()
	if total > 0 && got != total {
		os.Remove(dest)
		return fmt.Errorf("download incomplete (%d of %d bytes)", got, total)
	}
	if rel.SHA256 != "" {
		if sum := hex.EncodeToString(h.Sum(nil)); sum != rel.SHA256 {
			os.Remove(dest)
			return fmt.Errorf("SHA-256 mismatch: the download is corrupted or was changed")
		}
	}
	if progress != nil {
		progress(got, got)
	}
	return nil
}

// Replace swaps the running executable for newExe. Windows can't overwrite
// a running .exe but can rename it, so the current one becomes
// LokalKit.old.exe (deleted on the next start) and the new one takes its
// place. On failure the original is put back.
func Replace(currentExe, newExe string) error {
	old := oldPath(currentExe)
	_ = os.Remove(old)
	if err := os.Rename(currentExe, old); err != nil {
		return fmt.Errorf("cannot replace %s: %w", filepath.Base(currentExe), err)
	}
	if err := os.Rename(newExe, currentExe); err != nil {
		_ = os.Rename(old, currentExe)
		return fmt.Errorf("cannot install the new version: %w", err)
	}
	return nil
}

func oldPath(exe string) string {
	ext := filepath.Ext(exe)
	return strings.TrimSuffix(exe, ext) + ".old" + ext
}

// Cleanup removes the previous version left behind by Replace.
func Cleanup(currentExe string) { _ = os.Remove(oldPath(currentExe)) }
