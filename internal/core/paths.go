package core

import (
	"os"
	"path/filepath"
	"strings"
)

// Paths holds every folder LokalKit works with. Everything lives under Root
// so the whole install is portable (copy the folder, it still works).
type Paths struct {
	Root string `json:"root"`
	Bin  string `json:"bin"`
	Etc  string `json:"etc"`
	WWW  string `json:"www"`
	Data string `json:"data"`
	Logs string `json:"logs"`
	Tmp  string `json:"tmp"`
}

// ResolveRoot returns the LokalKit root folder: LOKALKIT_ROOT if set,
// otherwise the folder containing the executable.
func ResolveRoot() string {
	if r := strings.TrimSpace(os.Getenv("LOKALKIT_ROOT")); r != "" {
		if abs, err := filepath.Abs(r); err == nil {
			return abs
		}
		return r
	}
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

func NewPaths(root string) Paths {
	return Paths{
		Root: root,
		Bin:  filepath.Join(root, "bin"),
		Etc:  filepath.Join(root, "etc"),
		WWW:  filepath.Join(root, "www"),
		Data: filepath.Join(root, "data"),
		Logs: filepath.Join(root, "logs"),
		Tmp:  filepath.Join(root, "tmp"),
	}
}

// Ensure creates the folder layout if it does not exist yet.
func (p Paths) Ensure() error {
	dirs := []string{
		p.Bin,
		filepath.Join(p.Bin, "apache"),
		filepath.Join(p.Bin, "php"),
		filepath.Join(p.Bin, "mysql"),
		filepath.Join(p.Bin, "mailpit"),
		filepath.Join(p.Bin, "composer"),
		filepath.Join(p.Etc, "apache", "sites-enabled"),
		filepath.Join(p.Etc, "mysql"),
		p.WWW, p.Data, p.Logs, p.Tmp,
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// slash converts a Windows path to forward slashes, which Apache and
// MySQL config files accept on every platform.
func slash(p string) string { return filepath.ToSlash(p) }

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
