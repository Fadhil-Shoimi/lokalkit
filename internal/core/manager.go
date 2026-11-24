package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

type ServiceInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Ports     string `json:"ports"`
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Busy      bool   `json:"busy"`
	Hint      string `json:"hint"` // where to put the binary when missing
}

type Service interface {
	ID() string
	Info() ServiceInfo
	Start() error
	Stop() error
	Running() bool
}

type LogEntry struct {
	Time  string `json:"time"`
	Level string `json:"level"` // info, ok, error
	Msg   string `json:"msg"`
}

// Emitter pushes events to the UI ("log", "services").
type Emitter func(event string, data any)

type Manager struct {
	P        Paths
	cfgMu    sync.RWMutex
	cfg      Config
	services []Service
	busyMu   sync.Mutex
	busy     map[string]bool
	emit     Emitter
	logMu    sync.Mutex
	history  []LogEntry

	trustAsked bool // asked Windows to trust the CA during this session

	// ConfirmTrust, when set, explains the upcoming Windows certificate
	// prompt and returns false if the user chose "Not now".
	ConfirmTrust func() bool

	tasks projectTasks // background Composer/Laravel jobs per project

	watchMu sync.Mutex
	projSig string // last seen projects signature (see WatchProjects)
}

func NewManager(root string, emit Emitter) (*Manager, error) {
	if emit == nil {
		emit = func(string, any) {}
	}
	m := &Manager{P: NewPaths(root), emit: emit, busy: map[string]bool{}}
	if err := m.P.Ensure(); err != nil {
		return nil, fmt.Errorf("cannot create folders in %s: %w", root, err)
	}
	cfg, err := LoadConfig(m.configPath())
	m.cfg = cfg
	if err != nil {
		m.Logf("error", "%v (using default settings)", err)
	}
	onExit := func(name string, expected bool, err error) {
		if !expected {
			m.Logf("error", "%s stopped unexpectedly (%v)", name, err)
		}
		m.emitServices()
	}
	m.services = []Service{
		&Apache{m: m, proc: NewProc("Apache", onExit)},
		&MySQL{m: m, proc: NewProc("MySQL", onExit)},
		&Mailpit{m: m, proc: NewProc("Mailpit", onExit)},
	}
	return m, nil
}

func (m *Manager) configPath() string { return filepath.Join(m.P.Root, "lokalkit.json") }

func (m *Manager) config() Config {
	m.cfgMu.RLock()
	defer m.cfgMu.RUnlock()
	return m.cfg
}

func (m *Manager) Config() Config { return m.config() }

// SaveConfig validates and stores new settings. Returns true when running
// services need a restart to pick them up.
func (m *Manager) SaveConfig(c Config) (bool, error) {
	if err := c.Validate(); err != nil {
		return false, err
	}
	if err := SaveConfig(m.configPath(), c); err != nil {
		return false, err
	}
	m.cfgMu.Lock()
	old := m.cfg
	m.cfg = c
	m.cfgMu.Unlock()
	m.Logf("ok", "Settings saved")
	if old.TLD != c.TLD || old.SSL != c.SSL || old.SSLPort != c.SSLPort || old.ApachePort != c.ApachePort {
		m.syncAppURLs(old)
	}
	if old.HideDeprecations != c.HideDeprecations {
		if php, ok := m.CurrentPHP(); ok {
			_ = m.EnsurePHPIni(php)
		}
	}
	return serviceSettingsChanged(old, c) && m.anyRunning(), nil
}

func (m *Manager) Logf(level, format string, args ...any) {
	e := LogEntry{Time: time.Now().Format("15:04:05"), Level: level, Msg: fmt.Sprintf(format, args...)}
	m.logMu.Lock()
	m.history = append(m.history, e)
	if len(m.history) > 300 {
		m.history = m.history[len(m.history)-300:]
	}
	if f, err := os.OpenFile(filepath.Join(m.P.Logs, "lokalkit.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "%s [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), level, e.Msg)
		f.Close()
	}
	m.logMu.Unlock()
	m.emit("log", e)
}

func (m *Manager) History() []LogEntry {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	return append([]LogEntry(nil), m.history...)
}

func (m *Manager) Services() []ServiceInfo {
	out := make([]ServiceInfo, 0, len(m.services))
	m.busyMu.Lock()
	busy := map[string]bool{}
	for k, v := range m.busy {
		busy[k] = v
	}
	m.busyMu.Unlock()
	for _, s := range m.services {
		info := s.Info()
		info.Busy = busy[s.ID()]
		out = append(out, info)
	}
	return out
}

func (m *Manager) emitServices() { m.emit("services", m.Services()) }

func (m *Manager) service(id string) (Service, error) {
	for _, s := range m.services {
		if s.ID() == id {
			return s, nil
		}
	}
	return nil, fmt.Errorf("unknown service: %s", id)
}

func (m *Manager) setBusy(id string, b bool) bool {
	m.busyMu.Lock()
	if b && m.busy[id] {
		m.busyMu.Unlock()
		return false
	}
	m.busy[id] = b
	m.busyMu.Unlock()
	m.emitServices()
	return true
}

func (m *Manager) anyRunning() bool {
	for _, s := range m.services {
		if s.Running() {
			return true
		}
	}
	return false
}

func (m *Manager) Start(id string) error { return m.start(id, true) }

// start starts a service; syncHosts=false when the caller just synced the
// hosts file (avoids a second Windows admin prompt).
func (m *Manager) start(id string, syncHosts bool) error {
	s, err := m.service(id)
	if err != nil {
		return err
	}
	if s.Running() {
		return nil
	}
	name := s.Info().Name
	if !m.setBusy(id, true) {
		return nil
	}
	defer m.setBusy(id, false)

	if id == "apache" && syncHosts {
		if err := m.SyncHosts(); err != nil {
			m.Logf("error", "%v", err)
		}
	}
	m.Logf("info", "Starting %s...", name)
	if err := s.Start(); err != nil {
		m.Logf("error", "%v", err)
		return err
	}
	m.Logf("ok", "%s is running", name)
	return nil
}

func (m *Manager) Stop(id string) error {
	s, err := m.service(id)
	if err != nil {
		return err
	}
	if !s.Running() {
		return nil
	}
	name := s.Info().Name
	if !m.setBusy(id, true) {
		return nil
	}
	defer m.setBusy(id, false)
	m.Logf("info", "Stopping %s...", name)
	if err := s.Stop(); err != nil {
		m.Logf("error", "%v", err)
		return err
	}
	m.Logf("ok", "%s stopped", name)
	return nil
}

// ErrNothingInstalled is returned by StartAll when bin\ has no servers yet.
var ErrNothingInstalled = errors.New(`no services found yet. Put Apache, PHP and MySQL in the bin folder (click "Open bin"), then try again`)

// StartAll starts every installed service in parallel.
func (m *Manager) StartAll() error {
	installed := false
	for _, s := range m.services {
		if s.Info().Installed {
			installed = true
		}
	}
	if !installed {
		m.Logf("error", "%v. LokalKit folder: %s", ErrNothingInstalled, m.P.Root)
		return ErrNothingInstalled
	}
	return m.forAll(func(s Service) error {
		if !s.Info().Installed {
			return nil
		}
		return m.Start(s.ID())
	})
}

func (m *Manager) StopAll() error {
	return m.forAll(func(s Service) error { return m.Stop(s.ID()) })
}

func (m *Manager) forAll(fn func(Service) error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(m.services))
	for i, s := range m.services {
		wg.Add(1)
		go func(i int, s Service) {
			defer wg.Done()
			errs[i] = fn(s)
		}(i, s)
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (m *Manager) AnyRunning() bool { return m.anyRunning() }

// Toggle starts a stopped service or stops a running one.
func (m *Manager) Toggle(id string) error {
	s, err := m.service(id)
	if err != nil {
		return err
	}
	if s.Running() {
		return m.Stop(id)
	}
	return m.Start(id)
}

// Reload regenerates virtual hosts + hosts file and restarts Apache if running.
func (m *Manager) Reload() error {
	m.rememberProjects()
	if err := m.WriteVhosts(nil); err != nil {
		return err
	}
	hostsErr := m.SyncHosts()
	if hostsErr != nil {
		m.Logf("error", "%v", hostsErr)
	}
	if s, _ := m.service("apache"); s.Running() {
		if err := m.Stop("apache"); err != nil {
			return err
		}
		if err := m.start("apache", false); err != nil {
			return err
		}
	} else {
		m.Logf("ok", "Virtual hosts updated")
	}
	m.emit("projects", m.Projects())
	return hostsErr
}

// projectsSignature changes whenever a project is added, removed or its
// document root changes (e.g. Laravel's public/ appears).
func (m *Manager) projectsSignature() string {
	var b strings.Builder
	for _, p := range m.Projects() {
		b.WriteString(p.Host + "=" + p.DocRoot + "\n")
	}
	return b.String()
}

func (m *Manager) rememberProjects() {
	sig := m.projectsSignature()
	m.watchMu.Lock()
	m.projSig = sig
	m.watchMu.Unlock()
}

// WatchProjects polls the www folder and reloads automatically when
// projects change, so a new folder works at https://name.test right away.
func (m *Manager) WatchProjects(stop <-chan struct{}) {
	m.rememberProjects()
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		sig := m.projectsSignature()
		m.watchMu.Lock()
		changed := sig != m.projSig
		m.watchMu.Unlock()
		if !changed {
			continue
		}
		m.Logf("info", "Projects changed in www, updating...")
		if err := m.Reload(); err != nil {
			m.Logf("error", "%v", err)
		}
	}
}

// RestartRunning restarts services that are running (after settings change).
func (m *Manager) RestartRunning() error {
	return m.forAll(func(s Service) error {
		if !s.Running() {
			return nil
		}
		if err := m.Stop(s.ID()); err != nil {
			return err
		}
		return m.Start(s.ID())
	})
}

// serviceSettingsChanged reports whether running services must restart.
// Linked projects only need a reload, which Reload/the watcher handle.
func serviceSettingsChanged(a, b Config) bool {
	a.Linked, b.Linked = nil, nil
	return !reflect.DeepEqual(a, b)
}

// Restart restarts one service if it is running.
func (m *Manager) Restart(id string) error {
	s, err := m.service(id)
	if err != nil || !s.Running() {
		return err
	}
	if err := m.Stop(id); err != nil {
		return err
	}
	return m.Start(id)
}
