package core

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ProjectForDir is the project that dir is (inside) of, if any.
func (m *Manager) ProjectForDir(dir string) (Project, bool) {
	clean := strings.ToLower(filepath.Clean(dir))
	var best Project
	found := false
	for _, p := range m.Projects() {
		root := strings.ToLower(filepath.Clean(p.Path))
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			if !found || len(p.Path) > len(best.Path) {
				best, found = p, true
			}
		}
	}
	return best, found
}

// ProjectLogFile is the log worth reading for a project: Laravel's
// storage/logs/laravel.log (or the newest daily log), or the AdonisJS dev
// server output. "" when there is none yet.
func (m *Manager) ProjectLogFile(p Project) string {
	if p.Kind == "adonis" {
		if f := filepath.Join(m.P.Logs, "dev-"+p.Name+".log"); exists(f) {
			return f
		}
		return ""
	}
	dir := filepath.Join(p.Path, "storage", "logs")
	if f := filepath.Join(dir, "laravel.log"); exists(f) {
		return f
	}
	daily, _ := filepath.Glob(filepath.Join(dir, "laravel-*.log"))
	if len(daily) > 0 {
		sort.Strings(daily) // laravel-YYYY-MM-DD.log sorts by date
		return daily[len(daily)-1]
	}
	return ""
}

// TailLines returns the last n lines of a file (reading at most the last
// 1 MB, so huge logs stay fast).
func TailLines(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	const max = 1 << 20
	if st, err := f.Stat(); err == nil && st.Size() > max {
		_, _ = f.Seek(-max, io.SeekEnd)
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return "", err
	}
	b = bytes.TrimRight(b, "\r\n")
	lines := bytes.Split(b, []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimRight(string(bytes.Join(lines, []byte("\n"))), "\r") + "\n", nil
}
