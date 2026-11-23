// Package installer downloads and installs the server binaries (Apache, PHP,
// MySQL, Mailpit, Composer, Adminer) into LokalKit's bin folder.
package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sources holds every URL the installer uses. Defaults are compiled in; a
// newer manifest.json from the repository can override any field, so a moved
// download can be fixed without shipping a new LokalKit.exe.
type Sources struct {
	PHPReleasesJSON string   `json:"phpReleasesJson"`
	PHPDownloadBase string   `json:"phpDownloadBase"`
	PHPBranch       string   `json:"phpBranch"` // e.g. "8.3"
	ApacheIndex     string   `json:"apacheIndex"`
	ApacheBase      string   `json:"apacheBase"`
	MySQLVersion    string   `json:"mysqlVersion"`
	MySQLURLs       []string `json:"mysqlUrls"` // tried in order
	MailpitURL      string   `json:"mailpitUrl"`
	ComposerURL     string   `json:"composerUrl"`
	ComposerSHA256  string   `json:"composerSha256Url"`
	AdminerURL      string   `json:"adminerUrl"`
	VCRedistURL     string   `json:"vcRedistUrl"`
	NodeIndex       string   `json:"nodeIndex"`
	DBeaverURL      string   `json:"dbeaverUrl"`
	DBeaverVersion  string   `json:"dbeaverVersionXml"`
	DBeaverGitHub   string   `json:"dbeaverGithub"` // fallback, %s = version
	NodeBase        string   `json:"nodeBase"`
}

// ManifestURL is where a newer Sources override is published.
const ManifestURL = "https://raw.githubusercontent.com/Fadhil-Shoimi/lokalkit/main/manifest.json"

func DefaultSources() Sources {
	return Sources{
		PHPReleasesJSON: "https://windows.php.net/downloads/releases/releases.json",
		PHPDownloadBase: "https://windows.php.net/downloads/releases/",
		PHPBranch:       "8.3",
		ApacheIndex:     "https://www.apachelounge.com/download/",
		ApacheBase:      "https://www.apachelounge.com",
		MySQLVersion:    "8.4.3",
		MySQLURLs: []string{
			"https://downloads.mysql.com/archives/get/p/23/file/mysql-8.4.3-winx64.zip",
			"https://cdn.mysql.com/archives/mysql-8.4/mysql-8.4.3-winx64.zip",
		},
		MailpitURL:     "https://github.com/axllent/mailpit/releases/latest/download/mailpit-windows-amd64.zip",
		ComposerURL:    "https://getcomposer.org/download/latest-stable/composer.phar",
		ComposerSHA256: "https://getcomposer.org/download/latest-stable/composer.phar.sha256",
		AdminerURL:     "https://www.adminer.org/latest.php",
		VCRedistURL:    "https://aka.ms/vs/17/release/vc_redist.x64.exe",
		NodeIndex:      "https://nodejs.org/dist/index.json",
		DBeaverURL:     "https://dbeaver.io/files/dbeaver-ce-latest-win32.win32.x86_64.zip",
		DBeaverVersion: "https://dbeaver.io/product/version.xml",
		DBeaverGitHub:  "https://github.com/dbeaver/dbeaver/releases/download/%s/dbeaver-ce-%s-win32.win32.x86_64.zip",
		NodeBase:       "https://nodejs.org/dist/",
	}
}

// Artifact is one resolved download.
type Artifact struct {
	Version string
	URLs    []string // tried in order
	SHA256  string   // empty = not verified
	Kind    string   // "zip" or "file"
	Name    string   // folder (zip) or file name to install as
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) LokalKit/0.1"

func get(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// LoadSources returns the defaults, overridden by the published manifest
// when it can be fetched quickly.
func LoadSources(ctx context.Context, manifestURL string) Sources {
	s := DefaultSources()
	if manifestURL == "" {
		return s
	}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if b, err := get(ctx, manifestURL); err == nil {
		var override Sources
		if json.Unmarshal(b, &override) == nil {
			s = mergeSources(s, override)
		}
	}
	return s
}

func mergeSources(base, o Sources) Sources {
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	set(&base.PHPReleasesJSON, o.PHPReleasesJSON)
	set(&base.PHPDownloadBase, o.PHPDownloadBase)
	set(&base.PHPBranch, o.PHPBranch)
	set(&base.ApacheIndex, o.ApacheIndex)
	set(&base.ApacheBase, o.ApacheBase)
	set(&base.MySQLVersion, o.MySQLVersion)
	if len(o.MySQLURLs) > 0 {
		base.MySQLURLs = o.MySQLURLs
	}
	set(&base.MailpitURL, o.MailpitURL)
	set(&base.ComposerURL, o.ComposerURL)
	set(&base.ComposerSHA256, o.ComposerSHA256)
	set(&base.AdminerURL, o.AdminerURL)
	set(&base.VCRedistURL, o.VCRedistURL)
	set(&base.NodeIndex, o.NodeIndex)
	set(&base.DBeaverURL, o.DBeaverURL)
	set(&base.DBeaverVersion, o.DBeaverVersion)
	set(&base.DBeaverGitHub, o.DBeaverGitHub)
	set(&base.NodeBase, o.NodeBase)
	return base
}

// ResolvePHP reads windows.php.net's releases.json and picks the newest
// Thread Safe x64 build of the branch (TS is required by Apache's module).
func ResolvePHP(ctx context.Context, s Sources) (Artifact, error) {
	b, err := get(ctx, s.PHPReleasesJSON)
	if err != nil {
		return Artifact{}, fmt.Errorf("cannot read PHP releases: %w", err)
	}
	var rel map[string]map[string]json.RawMessage
	if err := json.Unmarshal(b, &rel); err != nil {
		return Artifact{}, fmt.Errorf("unexpected PHP releases format: %w", err)
	}
	branch, ok := rel[s.PHPBranch]
	if !ok {
		return Artifact{}, fmt.Errorf("PHP %s not found in releases", s.PHPBranch)
	}
	var version string
	_ = json.Unmarshal(branch["version"], &version)
	var keys []string
	for k := range branch {
		if strings.HasPrefix(k, "ts-") && strings.HasSuffix(k, "-x64") {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return Artifact{}, fmt.Errorf("no Thread Safe x64 build for PHP %s", s.PHPBranch)
	}
	sort.Strings(keys)
	var build struct {
		Zip struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"zip"`
	}
	if err := json.Unmarshal(branch[keys[len(keys)-1]], &build); err != nil || build.Zip.Path == "" {
		return Artifact{}, fmt.Errorf("unexpected PHP build entry")
	}
	name := strings.TrimSuffix(build.Zip.Path, ".zip")
	return Artifact{
		Version: version, Kind: "zip", Name: name,
		URLs:   []string{s.PHPDownloadBase + build.Zip.Path},
		SHA256: strings.ToLower(build.Zip.SHA256),
	}, nil
}

var apacheZipRe = regexp.MustCompile(`href="([^"]*httpd-(2\.4\.\d+)-\d+-[Ww]in64-VS(\d+)\.zip)"`)

// ResolveApache finds the newest 64-bit Apache 2.4 build on Apache Lounge.
func ResolveApache(ctx context.Context, s Sources) (Artifact, error) {
	b, err := get(ctx, s.ApacheIndex)
	if err != nil {
		return Artifact{}, fmt.Errorf("cannot read the Apache Lounge download page: %w", err)
	}
	type cand struct {
		href, ver string
		vs        int
	}
	var cands []cand
	for _, m := range apacheZipRe.FindAllStringSubmatch(string(b), -1) {
		vs, _ := strconv.Atoi(m[3])
		cands = append(cands, cand{m[1], m[2], vs})
	}
	if len(cands) == 0 {
		return Artifact{}, fmt.Errorf("no Apache 2.4 Win64 download found on %s", s.ApacheIndex)
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].ver != cands[j].ver {
			return patchOf(cands[i].ver) > patchOf(cands[j].ver)
		}
		return cands[i].vs > cands[j].vs
	})
	c := cands[0]
	url := c.href
	if strings.HasPrefix(url, "/") {
		url = strings.TrimRight(s.ApacheBase, "/") + url
	}
	art := Artifact{Version: c.ver, Kind: "zip", Name: "httpd-" + c.ver, URLs: []string{url}}
	// Apache Lounge publishes a .sha256 next to most zips; use it when present.
	if sum, err := get(ctx, url+".sha256"); err == nil {
		art.SHA256 = firstHex64(string(sum))
	}
	return art, nil
}

func patchOf(v string) int {
	parts := strings.Split(v, ".")
	n, _ := strconv.Atoi(parts[len(parts)-1])
	return n
}

var hex64Re = regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`)

func firstHex64(s string) string { return strings.ToLower(hex64Re.FindString(s)) }

func ResolveMySQL(_ context.Context, s Sources) (Artifact, error) {
	if len(s.MySQLURLs) == 0 {
		return Artifact{}, fmt.Errorf("no MySQL download configured")
	}
	return Artifact{Version: s.MySQLVersion, Kind: "zip", Name: "mysql-" + s.MySQLVersion, URLs: s.MySQLURLs}, nil
}

func ResolveMailpit(_ context.Context, s Sources) (Artifact, error) {
	return Artifact{Version: "latest", Kind: "zip", Name: "mailpit", URLs: []string{s.MailpitURL}}, nil
}

func ResolveComposer(ctx context.Context, s Sources) (Artifact, error) {
	art := Artifact{Version: "latest", Kind: "file", Name: "composer.phar", URLs: []string{s.ComposerURL}}
	if sum, err := get(ctx, s.ComposerSHA256); err == nil {
		art.SHA256 = firstHex64(string(sum))
	}
	return art, nil
}

func ResolveAdminer(_ context.Context, s Sources) (Artifact, error) {
	return Artifact{Version: "latest", Kind: "file", Name: "index.php", URLs: []string{s.AdminerURL}}, nil
}

// ResolveNode picks the newest LTS Node.js with a Windows x64 zip from
// nodejs.org/dist/index.json and its SHA-256 from SHASUMS256.txt.
func ResolveNode(ctx context.Context, s Sources) (Artifact, error) {
	b, err := get(ctx, s.NodeIndex)
	if err != nil {
		return Artifact{}, fmt.Errorf("cannot read Node.js releases: %w", err)
	}
	var rel []struct {
		Version string          `json:"version"`
		LTS     json.RawMessage `json:"lts"` // false or a codename
		Files   []string        `json:"files"`
	}
	if err := json.Unmarshal(b, &rel); err != nil {
		return Artifact{}, fmt.Errorf("unexpected Node.js releases format: %w", err)
	}
	for _, r := range rel {
		if string(r.LTS) == "false" || len(r.LTS) == 0 {
			continue
		}
		hasZip := false
		for _, f := range r.Files {
			if f == "win-x64-zip" {
				hasZip = true
			}
		}
		if !hasZip {
			continue
		}
		name := "node-" + r.Version + "-win-x64"
		dir := strings.TrimRight(s.NodeBase, "/") + "/" + r.Version + "/"
		art := Artifact{Version: strings.TrimPrefix(r.Version, "v"), Kind: "zip", Name: name, URLs: []string{dir + name + ".zip"}}
		if sums, err := get(ctx, dir+"SHASUMS256.txt"); err == nil {
			for _, line := range strings.Split(string(sums), "\n") {
				if f := strings.Fields(line); len(f) == 2 && f[1] == name+".zip" {
					art.SHA256 = strings.ToLower(f[0])
				}
			}
		}
		return art, nil
	}
	return Artifact{}, fmt.Errorf("no Node.js LTS release with a Windows zip found")
}

var dbeaverVersionRe = regexp.MustCompile(`<number>\s*([0-9]+\.[0-9]+\.[0-9]+)\s*</number>`)

// ResolveDBeaver downloads the portable Windows zip of DBeaver Community
// (it bundles its own Java). The version, when dbeaver.io reports it, names
// the folder and enables the GitHub mirror as a fallback.
func ResolveDBeaver(ctx context.Context, s Sources) (Artifact, error) {
	art := Artifact{Version: "latest", Kind: "zip", Name: "dbeaver", URLs: []string{s.DBeaverURL}}
	if b, err := get(ctx, s.DBeaverVersion); err == nil {
		if m := dbeaverVersionRe.FindStringSubmatch(string(b)); m != nil {
			art.Version, art.Name = m[1], "dbeaver-"+m[1]
			if strings.Count(s.DBeaverGitHub, "%s") == 2 {
				art.URLs = append(art.URLs, fmt.Sprintf(s.DBeaverGitHub, m[1], m[1]))
			}
		}
	}
	return art, nil
}
