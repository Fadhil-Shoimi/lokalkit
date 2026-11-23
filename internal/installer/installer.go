package installer

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Component is one installable item shown in the Install tab.
type Component struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Recommended bool   `json:"recommended"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version"` // installed version, if known
}

// Progress is pushed to the UI while installing.
type Progress struct {
	ID       string `json:"id"`
	Phase    string `json:"phase"` // resolving, downloading, verifying, extracting, installing, done, error, cancelled
	Received int64  `json:"received"`
	Total    int64  `json:"total"` // -1 when unknown
	Version  string `json:"version"`
	Message  string `json:"message"`
}

// Detector reports whether a server component is already installed (the
// engine knows how to find Apache/PHP/MySQL/Mailpit in bin).
type Detector func(id string) (installed bool, version string)

type Installer struct {
	Root        string // LokalKit root folder
	ManifestURL string
	Detect      Detector
	OnProgress  func(Progress)

	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	sources *Sources

	// resolvers can be replaced in tests.
	resolvers map[string]func(context.Context, Sources) (Artifact, error)
}

func New(root string, detect Detector, onProgress func(Progress)) *Installer {
	return &Installer{
		Root: root, ManifestURL: ManifestURL, Detect: detect, OnProgress: onProgress,
		resolvers: map[string]func(context.Context, Sources) (Artifact, error){
			"apache":   ResolveApache,
			"php":      ResolvePHP,
			"mysql":    ResolveMySQL,
			"mailpit":  ResolveMailpit,
			"composer": ResolveComposer,
			"adminer":  ResolveAdminer,
			"nodejs":   ResolveNode,
			"dbeaver":  ResolveDBeaver,
			"vcredist": func(_ context.Context, s Sources) (Artifact, error) {
				return Artifact{Version: "latest", Kind: "run", Name: "vc_redist.x64.exe", URLs: []string{s.VCRedistURL}}, nil
			},
		},
	}
}

var catalog = []Component{
	{ID: "vcredist", Name: "Visual C++ Runtime", Description: "Required by Apache and PHP on Windows (installs system-wide, asks for admin)", Recommended: true},
	{ID: "apache", Name: "Apache", Description: "Web server (Apache Lounge, 64-bit)", Recommended: true},
	{ID: "php", Name: "PHP", Description: "PHP 8.3, Thread Safe 64-bit (windows.php.net)", Recommended: true},
	{ID: "mysql", Name: "MySQL", Description: "MySQL 8.4 LTS database (about 250 MB)", Recommended: true},
	{ID: "mailpit", Name: "Mailpit", Description: "Catches emails sent by your apps", Recommended: true},
	{ID: "composer", Name: "Composer", Description: "PHP package manager (needed for Laravel)", Recommended: true},
	{ID: "nodejs", Name: "Node.js", Description: "JavaScript runtime (LTS), builds the front-end of Laravel starter kits", Recommended: true},
	{ID: "dbeaver", Name: "DBeaver Community", Description: "Full database manager (free, similar to Navicat). About 130 MB, Java included"},
	{ID: "adminer", Name: "Adminer", Description: "Database manager in the browser (http://localhost/adminer/)"},
}

// Components lists everything installable with its current state.
func (in *Installer) Components() []Component {
	out := make([]Component, 0, len(catalog))
	for _, c := range catalog {
		switch c.ID {
		case "vcredist":
			if runtime.GOOS != "windows" {
				continue
			}
			c.Installed = VCRuntimeInstalled()
		case "composer":
			c.Installed = fileExists(filepath.Join(in.Root, "bin", "composer", "composer.phar"))
		case "adminer":
			c.Installed = fileExists(filepath.Join(in.Root, "etc", "apps", "adminer", "index.php"))
		case "nodejs":
			c.Installed, c.Version = nodeInstalled(in.Root)
		case "dbeaver":
			c.Installed, c.Version = appInstalled(in.Root, "dbeaver", "dbeaver", "dbeaver-")
		default:
			if in.Detect != nil {
				c.Installed, c.Version = in.Detect(c.ID)
			}
		}
		out = append(out, c)
	}
	return out
}

// VCRuntimeInstalled checks for the Visual C++ 2015-2022 x64 runtime DLLs.
func VCRuntimeInstalled() bool {
	if runtime.GOOS != "windows" {
		return true
	}
	sys := filepath.Join(os.Getenv("SystemRoot"), "System32")
	for _, dll := range []string{"vcruntime140.dll", "vcruntime140_1.dll", "msvcp140.dll"} {
		if !fileExists(filepath.Join(sys, dll)) {
			return false
		}
	}
	return true
}

func (in *Installer) Running() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.running
}

// Cancel stops the current installation.
func (in *Installer) Cancel() {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.cancel != nil {
		in.cancel()
	}
}

// Install installs the given components one after another. It returns the
// first error but keeps going with the rest.
func (in *Installer) Install(ids []string) error {
	in.mu.Lock()
	if in.running {
		in.mu.Unlock()
		return errors.New("an installation is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	in.running, in.cancel = true, cancel
	in.mu.Unlock()
	defer func() {
		cancel()
		in.mu.Lock()
		in.running, in.cancel = false, nil
		in.mu.Unlock()
	}()

	if in.sources == nil {
		s := LoadSources(ctx, in.ManifestURL)
		in.sources = &s
	}
	var firstErr error
	for _, id := range orderIDs(ids) {
		if err := in.installOne(ctx, id); err != nil {
			if errors.Is(err, context.Canceled) {
				in.emit(Progress{ID: id, Phase: "cancelled", Message: "Cancelled"})
				return err
			}
			in.emit(Progress{ID: id, Phase: "error", Message: err.Error()})
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", id, err)
			}
		}
	}
	return firstErr
}

// orderIDs installs in catalog order (runtime first) and drops unknown ids.
func orderIDs(ids []string) []string {
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	var out []string
	for _, c := range catalog {
		if want[c.ID] {
			out = append(out, c.ID)
		}
	}
	return out
}

func (in *Installer) emit(p Progress) {
	if in.OnProgress != nil {
		in.OnProgress(p)
	}
}

func (in *Installer) installOne(ctx context.Context, id string) error {
	resolve, ok := in.resolvers[id]
	if !ok {
		return fmt.Errorf("unknown component")
	}
	in.emit(Progress{ID: id, Phase: "resolving", Message: "Finding the latest version..."})
	art, err := resolve(ctx, *in.sources)
	if err != nil {
		return err
	}

	tmpDir := filepath.Join(in.Root, "tmp", "downloads")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	file := filepath.Join(tmpDir, id+"-"+safeName(art.Version)+filepath.Ext(art.Name)+".download")
	if art.Kind == "zip" {
		file = filepath.Join(tmpDir, id+"-"+safeName(art.Version)+".zip")
	}
	defer os.Remove(file)

	if err := in.download(ctx, id, art, file); err != nil {
		return err
	}
	if art.SHA256 != "" {
		in.emit(Progress{ID: id, Phase: "verifying", Version: art.Version, Message: "Checking SHA-256..."})
		if err := verifySHA256(file, art.SHA256); err != nil {
			return err
		}
	}

	switch art.Kind {
	case "zip":
		in.emit(Progress{ID: id, Phase: "extracting", Version: art.Version, Message: "Extracting..."})
		dest := filepath.Join(in.Root, "bin", id, safeName(art.Name))
		if err := extractZipAtomic(file, dest); err != nil {
			return err
		}
	case "file":
		dest := filepath.Join(in.Root, "bin", id, art.Name)
		if id == "adminer" {
			dest = filepath.Join(in.Root, "etc", "apps", "adminer", art.Name)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := copyFile(file, dest); err != nil {
			return err
		}
	case "run":
		in.emit(Progress{ID: id, Phase: "installing", Version: art.Version, Message: "Installing (approve the Windows prompt)..."})
		if err := runVCRedist(ctx, file); err != nil {
			return err
		}
	}
	in.emit(Progress{ID: id, Phase: "done", Version: art.Version, Message: "Installed"})
	return nil
}

const maxDownload = 1 << 30 // 1 GB safety limit

func (in *Installer) download(ctx context.Context, id string, art Artifact, dest string) error {
	var lastErr error
	for _, url := range art.URLs {
		if err := in.downloadURL(ctx, id, art.Version, url, dest); err != nil {
			if errors.Is(err, context.Canceled) {
				return err
			}
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("download failed: %w", lastErr)
}

func (in *Installer) downloadURL(ctx context.Context, id, version, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	client := &http.Client{} // no overall timeout: big files on slow links
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "text/html") && !strings.HasSuffix(url, ".php") {
		return fmt.Errorf("%s returned a web page instead of a file", url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	total := resp.ContentLength
	if total <= 0 {
		total = -1
	}
	var received int64
	last := time.Time{}
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return werr
			}
			received += int64(n)
			if received > maxDownload {
				f.Close()
				return errors.New("download is larger than expected")
			}
			if time.Since(last) > 150*time.Millisecond {
				last = time.Now()
				in.emit(Progress{ID: id, Phase: "downloading", Received: received, Total: total, Version: version, Message: "Downloading..."})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return rerr
		}
	}
	in.emit(Progress{ID: id, Phase: "downloading", Received: received, Total: received, Version: version, Message: "Downloaded"})
	if total > 0 && received != total {
		f.Close()
		return fmt.Errorf("download incomplete (%d of %d bytes)", received, total)
	}
	return f.Close()
}

func verifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA-256 mismatch: the download is corrupted or was changed (got %s…)", got[:12])
	}
	return nil
}

// extractZipAtomic extracts into a temporary folder, then swaps it into dest.
func extractZipAtomic(zipPath, dest string) error {
	tmp := dest + ".partial"
	_ = os.RemoveAll(tmp)
	if err := extractZip(zipPath, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	if _, err := os.Stat(dest); err == nil {
		if err := os.RemoveAll(dest); err != nil {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("cannot replace %s (stop the service first): %w", dest, err)
		}
	}
	return os.Rename(tmp, dest)
}

// extractZip unpacks a zip, refusing entries that would escape dest ("zip slip").
func extractZip(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("not a valid zip file: %w", err)
	}
	defer r.Close()
	root, _ := filepath.Abs(dest)
	for _, f := range r.File {
		name := filepath.FromSlash(f.Name)
		target := filepath.Join(root, name)
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path in zip: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			continue // never create symlinks from downloaded archives
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		if err := writeZipEntry(f, target); err != nil {
			return err
		}
	}
	return nil
}

func writeZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	mode := os.FileMode(0o644)
	if f.Mode()&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, io.LimitReader(rc, maxDownload)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".partial"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// runVCRedist runs Microsoft's installer. It elevates itself (UAC prompt).
func runVCRedist(ctx context.Context, exe string) error {
	if runtime.GOOS != "windows" {
		return errors.New("only needed on Windows")
	}
	final := strings.TrimSuffix(exe, ".download")
	if final == exe {
		final = exe + ".exe"
	}
	if err := os.Rename(exe, final); err != nil {
		return err
	}
	defer os.Remove(final)
	cmd := exec.CommandContext(ctx, final, "/install", "/passive", "/norestart")
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		switch exit.ExitCode() {
		case 3010: // installed, restart recommended
			return nil
		case 1638: // a newer version is already installed
			return nil
		case 1602:
			return errors.New("installation was cancelled")
		}
	}
	return err
}

var unsafeChars = strings.NewReplacer("/", "-", "\\", "-", ":", "-", "..", "-", " ", "-")

func safeName(s string) string {
	s = unsafeChars.Replace(s)
	if s == "" || s == "." {
		return "latest"
	}
	return s
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// nodeInstalled looks for bin/nodejs/<folder>/node.exe.
func nodeInstalled(root string) (bool, string) {
	exe := "node"
	if runtime.GOOS == "windows" {
		exe = "node.exe"
	}
	matches, _ := filepath.Glob(filepath.Join(root, "bin", "nodejs", "*", exe))
	deeper, _ := filepath.Glob(filepath.Join(root, "bin", "nodejs", "*", "*", exe))
	matches = append(matches, deeper...)
	if fileExists(filepath.Join(root, "bin", "nodejs", exe)) {
		return true, ""
	}
	if len(matches) == 0 {
		return false, ""
	}
	v := filepath.Base(filepath.Dir(matches[len(matches)-1]))
	v = strings.TrimSuffix(strings.TrimPrefix(v, "node-v"), "-win-x64")
	return true, v
}

// appInstalled looks for bin/<dir>/<folder>[/<sub>]/<exe>.exe and returns the
// version taken from the folder name (after prefix).
func appInstalled(root, dir, exe, prefix string) (bool, string) {
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	matches, _ := filepath.Glob(filepath.Join(root, "bin", dir, "*", exe))
	deeper, _ := filepath.Glob(filepath.Join(root, "bin", dir, "*", "*", exe))
	matches = append(matches, deeper...)
	if len(matches) == 0 {
		return false, ""
	}
	rel, _ := filepath.Rel(filepath.Join(root, "bin", dir), matches[len(matches)-1])
	first := strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	v := strings.TrimPrefix(first, prefix)
	if v == first {
		v = ""
	}
	return true, v
}
