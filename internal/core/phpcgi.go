package core

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PHP per project.
//
// Apache runs the default PHP as a module (fast, as before). A project
// pinned to another PHP branch (e.g. an old Laravel on 8.3 while the default
// is 8.6) is served by php-cgi of that version instead: LokalKit starts a
// few php-cgi workers on fixed local ports and the project's virtual host
// hands .php files to them through mod_proxy_fcgi.

const phpWorkers = 4 // php-cgi on Windows serves one request at a time per process

// PHPBranch turns "8.4.15" into "8.4".
func PHPBranch(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

// phpCGIBin is php-cgi.exe of an install ("" when the build has none).
func phpCGIBin(php PHPInstall) string {
	p := filepath.Join(php.Dir, exeName("php-cgi"))
	if exists(p) {
		return p
	}
	return ""
}

// PHPPoolPorts are the fixed local ports of a branch's php-cgi workers:
// 8.4 -> 9840-9843, 8.3 -> 9830-9833.
func PHPPoolPorts(version string) []int {
	parts := strings.Split(version, ".")
	maj, _ := strconv.Atoi(parts[0])
	minor := 0
	if len(parts) > 1 {
		minor, _ = strconv.Atoi(parts[1])
	}
	base := 9000 + maj*100 + minor*10
	ports := make([]int, phpWorkers)
	for i := range ports {
		ports[i] = base + i
	}
	return ports
}

// phpBalancer is the Apache balancer name for a branch ("lokalkit-php84").
func phpBalancer(version string) string {
	return "lokalkit-php" + strings.ReplaceAll(PHPBranch(version), ".", "")
}

// ProjectPHPChoice resolves the PHP a project runs on. override is true when
// it differs from the default PHP (so it needs php-cgi). missing is true when
// the project is pinned to a branch that is not installed (it then falls
// back to the default).
func (m *Manager) ProjectPHPChoice(name string) (php PHPInstall, override, missing bool, ok bool) {
	def, ok := m.CurrentPHP()
	if !ok {
		return PHPInstall{}, false, false, false
	}
	want := m.config().ProjectPHP[name]
	if want == "" || PHPBranch(def.Version) == want {
		return def, false, false, true
	}
	var best *PHPInstall
	all := m.PHPInstalls()
	for i, p := range all {
		if PHPBranch(p.Version) != want || phpCGIBin(p) == "" {
			continue
		}
		if best == nil || compareVer(p.Version, best.Version) > 0 {
			best = &all[i]
		}
	}
	if best == nil {
		return def, false, true, true
	}
	return *best, true, false, true
}

// phpForDir is the PHP for commands run in dir (Composer, artisan,
// terminals): the project's own PHP when dir is (inside) a project.
func (m *Manager) phpForDir(dir string) (PHPInstall, bool) {
	if dir != "" {
		clean := strings.ToLower(filepath.Clean(dir))
		for _, p := range ScanProjects(m.P.WWW, m.config()) {
			root := strings.ToLower(filepath.Clean(p.Path))
			if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
				php, _, _, ok := m.ProjectPHPChoice(p.Name)
				return php, ok
			}
		}
	}
	return m.CurrentPHP()
}

// pinnedPHP lists the PHP installs that projects need as php-cgi pools.
func (m *Manager) pinnedPHP(ps []Project) []PHPInstall {
	seen := map[string]bool{}
	var out []PHPInstall
	for _, p := range ps {
		if p.Kind == "adonis" {
			continue
		}
		php, override, _, ok := m.ProjectPHPChoice(p.Name)
		if ok && override && !seen[php.Name] {
			seen[php.Name] = true
			out = append(out, php)
		}
	}
	sort.Slice(out, func(i, j int) bool { return compareVer(out[i].Version, out[j].Version) > 0 })
	return out
}

// phpHandlerBlock goes inside a project's <VirtualHost> to send .php files
// to its php-cgi workers (name must be unique per virtual host).
//
// Apache builds the backend URL as handler + file path. On Windows the path
// starts with a drive letter, so the handler ends in "/" (otherwise
// "balancer://nameC:/www/..." breaks the balancer name), and the resulting
// SCRIPT_FILENAME "/C:/www/..." is turned back into "C:/www/..." for PHP.
func phpHandlerBlock(php PHPInstall, balancer bool, name string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "    # PHP %s for this project (php-cgi workers started by LokalKit)\n", php.Version)
	target := fmt.Sprintf("proxy:fcgi://127.0.0.1:%d/", PHPPoolPorts(php.Version)[0])
	if balancer {
		fmt.Fprintf(&b, "    <Proxy \"balancer://%s\">\n", name)
		for _, port := range PHPPoolPorts(php.Version) {
			fmt.Fprintf(&b, "        BalancerMember \"fcgi://127.0.0.1:%d\"\n", port)
		}
		b.WriteString("        ProxySet timeout=600\n    </Proxy>\n")
		target = "proxy:balancer://" + name + "/"
	}
	b.WriteString("    ProxyFCGIBackendType GENERIC\n")
	b.WriteString("    ProxyFCGISetEnvIf \"reqenv('SCRIPT_FILENAME') =~ m#" + scriptFilenameFix + "#\" SCRIPT_FILENAME \"$1\"\n")
	fmt.Fprintf(&b, "    <FilesMatch \"\\.php$\">\n        SetHandler \"%s\"\n    </FilesMatch>\n", target)
	return b.String()
}

// scriptFilenameFix drops the extra leading slash(es) mod_proxy_fcgi leaves
// in SCRIPT_FILENAME: "/C:/www/a.php" -> "C:/www/a.php", "//var/www/a.php"
// -> "/var/www/a.php".
const scriptFilenameFix = `^/+([A-Za-z]:/.*|/.*)$`

// withPHPHandler inserts the handler block (and CGIPassAuth, so Bearer
// tokens reach PHP) into every <VirtualHost> of a generated vhost.
func withPHPHandler(conf string, php PHPInstall, balancer bool, project string) string {
	conf = strings.ReplaceAll(conf, "        Require all granted\n    </Directory>\n",
		"        Require all granted\n        CGIPassAuth On\n    </Directory>\n")
	label := HostLabel(project)
	if label == "" {
		label = "project"
	}
	parts := strings.Split(conf, "</VirtualHost>")
	for i := 0; i < len(parts)-1; i++ {
		name := fmt.Sprintf("%s-%s-%d", phpBalancer(php.Version), label, i+1)
		parts[i] += phpHandlerBlock(php, balancer, name)
	}
	return strings.Join(parts, "</VirtualHost>")
}

// ---------- php-cgi workers ----------

type phpPool struct {
	php     PHPInstall
	procs   []*Proc
	stopped bool
}

type phpPools struct {
	mu    sync.Mutex
	pools map[string]*phpPool // by install name
}

// startPHPPools starts php-cgi workers for every pinned PHP (and stops
// pools no project needs any more).
func (m *Manager) startPHPPools(need []PHPInstall) error {
	m.cgi.mu.Lock()
	defer m.cgi.mu.Unlock()
	if m.cgi.pools == nil {
		m.cgi.pools = map[string]*phpPool{}
	}
	want := map[string]bool{}
	for _, php := range need {
		want[php.Name] = true
	}
	for name, pool := range m.cgi.pools {
		if !want[name] {
			pool.stop()
			delete(m.cgi.pools, name)
		}
	}
	for _, php := range need {
		if _, ok := m.cgi.pools[php.Name]; ok {
			continue
		}
		pool, err := m.startPHPPool(php)
		if err != nil {
			return err
		}
		m.cgi.pools[php.Name] = pool
	}
	return nil
}

func (m *Manager) startPHPPool(php PHPInstall) (*phpPool, error) {
	bin := phpCGIBin(php)
	if bin == "" {
		return nil, fmt.Errorf("php-cgi not found in %s", php.Dir)
	}
	if err := m.EnsurePHPIni(php); err != nil {
		return nil, err
	}
	pool := &phpPool{php: php}
	env := envWithPath([]string{php.Dir}, "PHPRC="+php.Dir, "PHP_FCGI_MAX_REQUESTS=0")
	logPath := filepath.Join(m.P.Logs, "php-cgi-"+PHPBranch(php.Version)+".log")
	for _, port := range PHPPoolPorts(php.Version) {
		if !PortFree(port) {
			// Most likely a worker left behind by an earlier LokalKit, which
			// still serves this PHP fine. Anything else shows up as errors.
			m.Logf("info", "Port %d is already in use; PHP %s will share it", port, PHPBranch(php.Version))
			continue
		}
		args := []string{"-b", fmt.Sprintf("127.0.0.1:%d", port)}
		var proc *Proc
		proc = NewProc(fmt.Sprintf("php-cgi %s:%d", php.Version, port), func(_ string, expected bool, _ error) {
			if expected {
				return
			}
			// Restart a crashed worker unless the pool was stopped.
			go func() {
				time.Sleep(time.Second)
				m.cgi.mu.Lock()
				stopped := pool.stopped
				m.cgi.mu.Unlock()
				if !stopped {
					_ = proc.Start(bin, args, php.Dir, env, logPath)
				}
			}()
		})
		if err := proc.Start(bin, args, php.Dir, env, logPath); err != nil {
			pool.stop()
			return nil, fmt.Errorf("cannot start php-cgi %s: %w", php.Version, err)
		}
		pool.procs = append(pool.procs, proc)
		if err := waitPort(port, proc.Exited(), 10*time.Second); err != nil {
			pool.stop()
			return nil, fmt.Errorf("php-cgi %s did not start (%v). See logs\\php-cgi-%s.log", php.Version, err, PHPBranch(php.Version))
		}
	}
	m.Logf("ok", "PHP %s ready for pinned projects (php-cgi ×%d)", php.Version, len(pool.procs))
	return pool, nil
}

func (p *phpPool) stop() {
	p.stopped = true
	for _, proc := range p.procs {
		_ = proc.Stop(nil, 0)
	}
}

// stopPHPPools stops every php-cgi worker.
func (m *Manager) stopPHPPools() {
	m.cgi.mu.Lock()
	defer m.cgi.mu.Unlock()
	for name, pool := range m.cgi.pools {
		pool.stop()
		delete(m.cgi.pools, name)
	}
}

// SetProjectPHP pins a project to a PHP branch ("8.3"); "" follows the
// default PHP again. Apache is reloaded so it takes effect at once.
func (m *Manager) SetProjectPHP(name, branch string) error {
	if m.findProject(name).Path == "" {
		return fmt.Errorf("project %q not found", name)
	}
	cfg := m.Config()
	if def, ok := m.CurrentPHP(); ok && branch == PHPBranch(def.Version) {
		branch = "" // same as the default: no pin needed
	}
	if branch != "" {
		found := false
		for _, p := range m.PHPInstalls() {
			if PHPBranch(p.Version) == branch && phpCGIBin(p) != "" {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("PHP %s is not installed. Install it from the PHP menu first", branch)
		}
	}
	pins := map[string]string{}
	for k, v := range cfg.ProjectPHP {
		pins[k] = v
	}
	if branch == "" {
		delete(pins, name)
	} else {
		pins[name] = branch
	}
	if len(pins) == 0 {
		pins = nil
	}
	cfg.ProjectPHP = pins
	if _, err := m.SaveConfig(cfg); err != nil {
		return err
	}
	if branch == "" {
		m.Logf("ok", "%s now uses the default PHP", name)
	} else {
		m.Logf("ok", "%s now uses PHP %s", name, branch)
	}
	return m.Reload()
}

// hasApacheModule reports whether the installed Apache ships a module.
func (m *Manager) hasApacheModule(mod string) bool {
	bin := findUnder(filepath.Join(m.P.Bin, "apache"), filepath.Join("bin", exeName("httpd")))
	if bin == "" {
		return false
	}
	return exists(filepath.Join(filepath.Dir(filepath.Dir(bin)), "modules", "mod_"+mod+".so"))
}

func (m *Manager) phpBalancerAvailable() bool {
	for _, mod := range []string{"proxy_balancer", "slotmem_shm", "lbmethod_byrequests"} {
		if !m.hasApacheModule(mod) {
			return false
		}
	}
	return true
}

// compareVer compares dotted versions numerically.
func compareVer(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
