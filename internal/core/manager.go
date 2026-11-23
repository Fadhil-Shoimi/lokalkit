package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
}

func NewManager(root string, emit Emitter) (*Manager, error) {
	if emit == nil {
		emit = func(string, any) {}
	}
	m := &Manager{P: NewPaths(root), emit: emit, busy: map[string]bool{}}
	if err := m.P.Ensure(); err != nil {
		return nil, fmt.Errorf("tidak dapat cipta folder dalam %s: %w", root, err)
	}
	cfg, err := LoadConfig(m.configPath())
	m.cfg = cfg
	if err != nil {
		m.Logf("error", "%v (guna tetapan asal)", err)
	}
	onExit := func(name string, expected bool, err error) {
		if !expected {
			m.Logf("error", "%s terhenti tanpa diduga (%v)", name, err)
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
	m.Logf("ok", "Tetapan disimpan")
	return old != c && m.anyRunning(), nil
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
	return nil, fmt.Errorf("servis tidak dikenali: %s", id)
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

func (m *Manager) Start(id string) error {
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

	if id == "apache" {
		if err := m.SyncHosts(); err != nil {
			m.Logf("error", "%v", err)
		}
	}
	m.Logf("info", "Memulakan %s...", name)
	if err := s.Start(); err != nil {
		m.Logf("error", "%v", err)
		return err
	}
	m.Logf("ok", "%s berjalan", name)
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
	m.Logf("info", "Menghentikan %s...", name)
	if err := s.Stop(); err != nil {
		m.Logf("error", "%v", err)
		return err
	}
	m.Logf("ok", "%s dihentikan", name)
	return nil
}

// StartAll starts every installed service in parallel.
func (m *Manager) StartAll() error {
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
	if err := m.WriteVhosts(nil); err != nil {
		return err
	}
	if err := m.SyncHosts(); err != nil {
		m.Logf("error", "%v", err)
	}
	if s, _ := m.service("apache"); s.Running() {
		if err := m.Stop("apache"); err != nil {
			return err
		}
		return m.Start("apache")
	}
	m.Logf("ok", "Virtual host dikemas kini")
	return nil
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
