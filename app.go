package main

import (
	"context"
	"fmt"
	"path/filepath"

	"lokalkit/internal/core"
	"lokalkit/internal/installer"
	"lokalkit/internal/tray"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const AppVersion = "0.1.0"

// App is the bridge between the Go engine and the web UI.
// Every exported method is callable from JavaScript.
type App struct {
	ctx       context.Context
	m         *core.Manager
	initErr   string
	tray      *tray.Tray
	inst      *installer.Installer
	stopWatch chan struct{}
	quitting  bool // true when quitting for real (tray "Quit"), not hiding
	hidTold   bool // already told the user the app keeps running in the tray
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	m, err := core.NewManager(core.ResolveRoot(), func(event string, data any) {
		runtime.EventsEmit(ctx, event, data)
		if event == "services" && a.tray != nil {
			if svcs, ok := data.([]core.ServiceInfo); ok {
				a.tray.Update(svcs)
			}
		}
	})
	if err != nil {
		a.initErr = err.Error()
		return
	}
	a.m = m
	a.tray = tray.Start(a.trayActions(), m.Services())
	a.inst = installer.New(m.P.Root, a.detectComponent, func(p installer.Progress) {
		runtime.EventsEmit(ctx, "install-progress", p)
	})
	a.stopWatch = make(chan struct{})
	go m.WatchProjects(a.stopWatch)
	m.Logf("info", "LokalKit %s ready. Folder: %s", AppVersion, m.P.Root)
	if m.Config().StartAllOnLaunch {
		go m.StartAll()
	}
}

// trayActions maps the tray menu to app actions. Long work runs in its own
// goroutine so the tray menu never freezes.
func (a *App) trayActions() tray.Actions {
	bg := func(fn func() error) func() {
		return func() {
			go func() {
				if err := fn(); err != nil {
					a.m.Logf("error", "%v", err)
				}
			}()
		}
	}
	return tray.Actions{
		Show:         a.ShowWindow,
		StartAll:     bg(a.m.StartAll),
		StopAll:      bg(a.m.StopAll),
		Toggle:       func(id string) { bg(func() error { return a.m.Toggle(id) })() },
		OpenWeb:      a.OpenWeb,
		OpenDatabase: bg(a.OpenDatabase),
		OpenTerminal: bg(func() error { return a.OpenTerminal("") }),
		OpenWWW:      bg(func() error { return a.OpenFolder("www") }),
		OpenMailpit:  a.OpenMailpit,
		Quit:         a.QuitApp,
	}
}

// ShowWindow brings the window back from the tray (or from minimised).
func (a *App) ShowWindow() {
	runtime.WindowUnminimise(a.ctx)
	runtime.WindowShow(a.ctx)
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowSetAlwaysOnTop(a.ctx, false)
}

// QuitApp really exits: stops services (per settings) and removes the tray icon.
func (a *App) QuitApp() {
	a.quitting = true
	runtime.Quit(a.ctx)
}

func (a *App) shutdown(ctx context.Context) {
	if a.stopWatch != nil {
		close(a.stopWatch)
	}
	if a.m != nil && a.m.Config().StopAllOnExit {
		_ = a.m.StopAll()
	}
	if a.tray != nil {
		a.tray.Stop()
	}
}

// beforeClose runs when the window's X is clicked. With "minimize to tray"
// on, the window is hidden and LokalKit keeps running in the system tray.
// Returning true cancels the close.
func (a *App) beforeClose(ctx context.Context) bool {
	if a.m == nil || a.quitting {
		return false
	}
	cfg := a.m.Config()
	if cfg.MinimizeToTray && a.tray != nil {
		runtime.WindowHide(ctx)
		if !a.hidTold {
			a.hidTold = true
			a.m.Logf("info", "LokalKit is still running in the system tray. Right-click the switch icon to quit.")
		}
		return true
	}
	if !a.m.AnyRunning() || !cfg.StopAllOnExit {
		return false
	}
	res, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type:          runtime.QuestionDialog,
		Title:         "Quit LokalKit?",
		Message:       "Services are still running and will all be stopped. Continue?",
		Buttons:       []string{"Yes", "No"},
		DefaultButton: "Yes",
		CancelButton:  "No",
	})
	if err != nil {
		return false
	}
	return res != "Yes" && res != "Ok"
}

// detectComponent tells the installer what the engine already found in bin.
func (a *App) detectComponent(id string) (bool, string) {
	if id == "php" {
		if p, ok := a.m.CurrentPHP(); ok {
			return true, p.Version
		}
		return false, ""
	}
	for _, s := range a.m.Services() {
		if s.ID == id {
			return s.Installed, s.Version
		}
	}
	return false, ""
}

// GetComponents lists what can be downloaded and what is already installed.
func (a *App) GetComponents() []installer.Component {
	if a.inst == nil {
		return nil
	}
	return a.inst.Components()
}

// InstallComponents downloads and installs the chosen components. Progress
// arrives as "install-progress" events; this returns when all are done.
func (a *App) InstallComponents(ids []string) error {
	if err := a.ready(); err != nil {
		return err
	}
	a.m.Logf("info", "Installing: %v", ids)
	err := a.inst.Install(ids)
	if err != nil {
		a.m.Logf("error", "Install: %v", err)
	} else {
		a.m.Logf("ok", "Installation finished")
	}
	runtime.EventsEmit(a.ctx, "services", a.m.Services())
	return err
}

func (a *App) CancelInstall() {
	if a.inst != nil {
		a.inst.Cancel()
	}
}

type State struct {
	Version  string             `json:"version"`
	Error    string             `json:"error"`
	Paths    core.Paths         `json:"paths"`
	Config   core.Config        `json:"config"`
	Services []core.ServiceInfo `json:"services"`
	PHP      []core.PHPInstall  `json:"php"`
	Current  string             `json:"currentPhp"`
	Projects []core.Project     `json:"projects"`
	Logs     []core.LogEntry    `json:"logs"`
	SSL      core.SSLStatus     `json:"ssl"`
	Missing  []string           `json:"missingHosts"` // project hosts not in the hosts file
}

func (a *App) GetState() State {
	s := State{Version: AppVersion, Error: a.initErr}
	if a.m == nil {
		return s
	}
	s.Paths, s.Config = a.m.P, a.m.Config()
	s.Services = a.m.Services()
	s.PHP = a.m.PHPInstalls()
	if cur, ok := a.m.CurrentPHP(); ok {
		s.Current = cur.Name
	}
	s.Projects = a.m.Projects()
	s.Logs = a.m.History()
	s.SSL = a.m.SSLStatus()
	s.Missing = a.m.MissingFromHosts()
	return s
}

func (a *App) ready() error {
	if a.m == nil {
		return fmt.Errorf("LokalKit could not start: %s", a.initErr)
	}
	return nil
}

func (a *App) StartService(id string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.Start(id)
}

func (a *App) StopService(id string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.Stop(id)
}

func (a *App) StartAll() error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.StartAll()
}

func (a *App) StopAll() error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.StopAll()
}

func (a *App) Reload() error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.Reload()
}

// SaveConfig stores settings and restarts running services if needed.
func (a *App) SaveConfig(c core.Config) error {
	if err := a.ready(); err != nil {
		return err
	}
	restart, err := a.m.SaveConfig(c)
	if err != nil {
		return err
	}
	if restart {
		return a.m.RestartRunning()
	}
	return nil
}

// FixHosts adds every project to the Windows hosts file (asks for admin).
func (a *App) FixHosts() ([]string, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	err := a.m.SyncHosts()
	return a.m.MissingFromHosts(), err
}

// UseLocalhostDomain switches project domains to name.localhost, which
// browsers resolve by themselves (no hosts file, no admin prompt).
func (a *App) UseLocalhostDomain() error {
	if err := a.ready(); err != nil {
		return err
	}
	cfg := a.m.Config()
	cfg.TLD = "localhost"
	if _, err := a.m.SaveConfig(cfg); err != nil {
		return err
	}
	a.m.Logf("ok", "Project domains now end in .localhost (no hosts file needed)")
	return a.m.Reload()
}

// TrustCA asks Windows to trust the LokalKit certificate authority.
func (a *App) TrustCA() (core.SSLStatus, error) {
	if err := a.ready(); err != nil {
		return core.SSLStatus{}, err
	}
	err := a.m.TrustCA()
	return a.m.SSLStatus(), err
}

func (a *App) CreateProject(name, kind string) (core.Project, error) {
	if err := a.ready(); err != nil {
		return core.Project{}, err
	}
	return a.m.CreateProject(name, kind)
}

func (a *App) OpenURL(url string) { runtime.BrowserOpenURL(a.ctx, url) }

func (a *App) OpenWeb() {
	if a.m != nil {
		runtime.BrowserOpenURL(a.ctx, a.m.LocalURL("/"))
	}
}

func (a *App) OpenMailpit() {
	if a.m != nil {
		runtime.BrowserOpenURL(a.ctx, a.m.MailpitURL())
	}
}

func (a *App) OpenDatabase() error {
	if err := a.ready(); err != nil {
		return err
	}
	url, err := a.m.OpenDatabase()
	if err != nil {
		return err
	}
	if url != "" {
		runtime.BrowserOpenURL(a.ctx, url)
	}
	return nil
}

func (a *App) OpenTerminal(dir string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.OpenTerminal(dir, "")
}

// OpenFolder opens a LokalKit folder by key: root, www, bin, logs, etc.
func (a *App) OpenFolder(key string) error {
	if err := a.ready(); err != nil {
		return err
	}
	p := a.m.P
	paths := map[string]string{"root": p.Root, "www": p.WWW, "bin": p.Bin, "logs": p.Logs, "etc": p.Etc, "ssl": a.m.SSLStatus().Dir}
	if dir, ok := paths[key]; ok {
		return core.OpenPath(dir)
	}
	// Otherwise treat as a project folder name inside www.
	return core.OpenPath(filepath.Join(p.WWW, filepath.Base(key)))
}
