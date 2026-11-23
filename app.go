package main

import (
	"context"
	"fmt"
	"path/filepath"

	"lokalkit/internal/core"
	"lokalkit/internal/tray"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const AppVersion = "0.1.0"

// App is the bridge between the Go engine and the web UI.
// Every exported method is callable from JavaScript.
type App struct {
	ctx      context.Context
	m        *core.Manager
	initErr  string
	tray     *tray.Tray
	quitting bool // true when quitting for real (tray "Keluar"), not hiding
	hidTold  bool // already told the user the app keeps running in the tray
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
	m.Logf("info", "LokalKit %s sedia. Folder: %s", AppVersion, m.P.Root)
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
			a.m.Logf("info", "LokalKit masih berjalan di system tray. Klik kanan ikon suis untuk keluar.")
		}
		return true
	}
	if !a.m.AnyRunning() || !cfg.StopAllOnExit {
		return false
	}
	res, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
		Type:          runtime.QuestionDialog,
		Title:         "Keluar LokalKit?",
		Message:       "Servis masih berjalan. Semua servis akan dihentikan. Teruskan?",
		Buttons:       []string{"Ya", "Tidak"},
		DefaultButton: "Ya",
		CancelButton:  "Tidak",
	})
	if err != nil {
		return false
	}
	return res != "Ya" && res != "Yes" && res != "Ok"
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
	return s
}

func (a *App) ready() error {
	if a.m == nil {
		return fmt.Errorf("LokalKit tidak dapat dimulakan: %s", a.initErr)
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
