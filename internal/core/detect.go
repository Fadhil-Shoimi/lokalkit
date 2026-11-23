package core

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func exeName(n string) string {
	if runtime.GOOS == "windows" {
		return n + ".exe"
	}
	return n
}

// subDirs lists the folders directly inside base, newest version first.
func subDirs(base string) []string {
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j], out[i]) })
	return out
}

// findUnder looks for rel inside base, base/*, and base/*/* so that both
// "bin/mysql/bin/mysqld.exe" and "bin/mysql/mysql-8.4.3-winx64/bin/mysqld.exe"
// work. This also matches the folder layout people already have from other tools.
func findUnder(base, rel string) string {
	if p := filepath.Join(base, rel); exists(p) {
		return p
	}
	for _, d := range subDirs(base) {
		lvl1 := filepath.Join(base, d)
		if p := filepath.Join(lvl1, rel); exists(p) {
			return p
		}
		for _, d2 := range subDirs(lvl1) {
			if p := filepath.Join(lvl1, d2, rel); exists(p) {
				return p
			}
		}
	}
	return ""
}

var verRe = regexp.MustCompile(`\d+\.\d+(\.\d+)?`)

func findVersion(s string) string { return verRe.FindString(s) }

// versionLess compares strings by the first version number found in them.
func versionLess(a, b string) bool {
	va, vb := findVersion(a), findVersion(b)
	if va == "" || vb == "" {
		return a < b
	}
	pa, pb := strings.Split(va, "."), strings.Split(vb, ".")
	for i := 0; i < 3; i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return a < b
}

var (
	versionCache   = map[string]string{}
	versionCacheMu sync.Mutex
)

// probeVersion runs "bin args..." once and caches the version it prints.
func probeVersion(bin string, args ...string) string {
	if bin == "" {
		return ""
	}
	versionCacheMu.Lock()
	if v, ok := versionCache[bin]; ok {
		versionCacheMu.Unlock()
		return v
	}
	versionCacheMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = filepath.Dir(bin)
	hideWindow(cmd)
	out, _ := cmd.CombinedOutput()
	v := findVersion(string(out))
	if v == "" {
		v = findVersion(bin)
	}

	versionCacheMu.Lock()
	versionCache[bin] = v
	versionCacheMu.Unlock()
	return v
}

// envWithPath returns the current environment with dirs prepended to PATH
// and extra variables (KEY=VALUE) set.
func envWithPath(dirs []string, extra ...string) []string {
	env := os.Environ()
	var clean []string
	for _, d := range dirs {
		if d != "" {
			clean = append(clean, d)
		}
	}
	prefix := strings.Join(clean, string(os.PathListSeparator))
	found := false
	for i, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, "PATH") {
			if prefix != "" {
				env[i] = k + "=" + prefix + string(os.PathListSeparator) + v
			}
			found = true
		}
	}
	if !found && prefix != "" {
		env = append(env, "PATH="+prefix)
	}
	for _, kv := range extra {
		k, _, _ := strings.Cut(kv, "=")
		replaced := false
		for i, e := range env {
			ek, _, _ := strings.Cut(e, "=")
			if strings.EqualFold(ek, k) {
				env[i] = kv
				replaced = true
			}
		}
		if !replaced {
			env = append(env, kv)
		}
	}
	return env
}
