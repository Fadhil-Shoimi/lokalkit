package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lokalkit/internal/core"
	"lokalkit/internal/installer"
	"lokalkit/internal/tray"
	"lokalkit/internal/updater"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// AppVersion is set at build time for releases:
// wails build -ldflags "-X main.AppVersion=0.2.0"
var AppVersion = "0.1.0-dev"

// App is the bridge between the Go engine and the web UI.
// Every exported method is callable from JavaScript.
type App struct {
	ctx       context.Context
	m         *core.Manager
	initErr   string
	tray      *tray.Tray
	inst      *installer.Installer
	stopWatch chan struct{}

	resume []string // services to start after an update restart

	updMu      sync.Mutex
	updChecked time.Time
	updRelease *updater.Release
	updErr     string
	updBusy    bool

	phpMu      sync.Mutex
	phpChecked time.Time
	phpCache   []installer.PHPRelease
	quitting   bool // true when quitting for real (tray "Quit"), not hiding
	hidTold    bool // already told the user the app keeps running in the tray

	lkListener net.Listener     // endpoint of the terminal's lk command
	openURL    func(url string) // opens a URL in the browser (replaceable in tests)
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
	a.openURL = func(url string) { runtime.BrowserOpenURL(ctx, url) }
	m.ConfirmTrust = a.confirmTrust
	a.startLK() // the terminal's lk command
	core.TerminalIcon = tray.IconOn
	a.tray = tray.Start(a.trayActions(), m.Services())
	a.inst = installer.New(m.P.Root, a.detectComponent, func(p installer.Progress) {
		runtime.EventsEmit(ctx, "install-progress", p)
	})
	a.stopWatch = make(chan struct{})
	go m.WatchProjects(a.stopWatch)
	m.Logf("info", "LokalKit %s ready. Folder: %s", AppVersion, m.P.Root)
	if exe, err := os.Executable(); err == nil {
		updater.Cleanup(exe) // remove LokalKit.old.exe from a previous update
	}
	if len(a.resume) > 0 {
		m.Logf("ok", "Updated to LokalKit %s", AppVersion)
		go func(ids []string) {
			for _, id := range ids {
				if id != "" {
					_ = m.Start(id)
				}
			}
		}(a.resume)
	}
	if m.Config().CheckUpdates {
		go func() {
			time.Sleep(5 * time.Second)
			if info := a.CheckForUpdate(false); info.Available {
				runtime.EventsEmit(ctx, "update-available", info)
			}
		}()
	}
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
	a.stopLK()
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

// PHPUpdates says which PHP versions can be downloaded and whether a newer
// patch exists for the PHP currently in use.
type PHPUpdates struct {
	Releases []installer.PHPRelease `json:"releases"`
	Update   *installer.PHPRelease  `json:"update"` // newer build of the current branch
	Error    string                 `json:"error"`
}

func branchOf(v string) string {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return v
	}
	return parts[0] + "." + parts[1]
}

// GetPHPUpdates checks windows.php.net (cached for 6 hours unless force).
func (a *App) GetPHPUpdates(force bool) PHPUpdates {
	if a.inst == nil || a.m == nil {
		return PHPUpdates{}
	}
	a.phpMu.Lock()
	defer a.phpMu.Unlock()
	if force || a.phpCache == nil || time.Since(a.phpChecked) > 6*time.Hour {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		rel, err := a.inst.PHPReleases(ctx)
		if err != nil {
			return PHPUpdates{Error: err.Error()}
		}
		a.phpCache, a.phpChecked = rel, time.Now()
	}
	out := PHPUpdates{Releases: a.phpCache}
	if cur, ok := a.m.CurrentPHP(); ok {
		for i, r := range a.phpCache {
			if r.Branch == branchOf(cur.Version) && installer.CompareVersions(r.Version, cur.Version) > 0 {
				out.Update = &a.phpCache[i]
			}
		}
	}
	return out
}

// InstallPHP downloads the newest build of a PHP branch, carries your
// php.ini over, switches to it and restarts Apache. The old PHP is kept.
func (a *App) InstallPHP(branch string) error {
	if err := a.ready(); err != nil {
		return err
	}
	prev, _ := a.m.CurrentPHP()
	a.m.Logf("info", "Downloading PHP %s...", branch)
	name, err := a.inst.InstallPHP(branch)
	if err != nil {
		a.m.Logf("error", "PHP download failed: %v", err)
		return err
	}
	if err := a.m.AdoptPHPIni(prev.Name, name); err != nil {
		a.m.Logf("error", "php.ini: %v", err)
	}
	cfg := a.m.Config()
	cfg.PHPVersion = name
	if _, err := a.m.SaveConfig(cfg); err != nil {
		return err
	}
	cur, _ := a.m.CurrentPHP()
	a.m.Logf("ok", "Now using PHP %s (PHP %s is still installed if you want to switch back)", cur.Version, prev.Version)
	if err := a.m.Restart("apache"); err != nil {
		return err
	}
	a.phpMu.Lock()
	a.phpChecked = time.Time{} // re-evaluate the update badge
	a.phpMu.Unlock()
	return nil
}

func (a *App) CancelInstall() {
	if a.inst != nil {
		a.inst.Cancel()
	}
}

type State struct {
	Version  string                 `json:"version"`
	Error    string                 `json:"error"`
	Paths    core.Paths             `json:"paths"`
	Config   core.Config            `json:"config"`
	Services []core.ServiceInfo     `json:"services"`
	PHP      []core.PHPInstall      `json:"php"`
	Current  string                 `json:"currentPhp"`
	Projects []core.Project         `json:"projects"`
	Logs     []core.LogEntry        `json:"logs"`
	SSL      core.SSLStatus         `json:"ssl"`
	Missing  []string               `json:"missingHosts"` // project hosts not in the hosts file
	NoDB     []core.MissingDatabase `json:"missingDbs"`   // projects whose MySQL database doesn't exist
	Blocker  string                 `json:"hostsBlocker"` // antivirus guarding the hosts file, if any
}

func (a *App) GetState() State {
	s := State{Version: AppVersion, Error: a.initErr}
	if a.m == nil {
		return s.withEmptyLists()
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
	s.NoDB = a.m.MissingDatabases()
	if len(s.Missing) > 0 {
		s.Blocker = core.HostsBlocker()
	}
	return s.withEmptyLists()
}

// withEmptyLists turns nil slices into empty ones, so the UI gets [] and
// never null (which breaks .forEach/.length in JavaScript).
func (s State) withEmptyLists() State {
	if s.Services == nil {
		s.Services = []core.ServiceInfo{}
	}
	if s.PHP == nil {
		s.PHP = []core.PHPInstall{}
	}
	if s.Projects == nil {
		s.Projects = []core.Project{}
	}
	if s.Logs == nil {
		s.Logs = []core.LogEntry{}
	}
	if s.Missing == nil {
		s.Missing = []string{}
	}
	if s.NoDB == nil {
		s.NoDB = []core.MissingDatabase{}
	}
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

// RepairProject installs missing Composer packages and finishes Laravel setup.
func (a *App) RepairProject(name string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.RepairProject(name)
}

// confirmTrust explains the Windows "Security Warning" before it appears,
// so it doesn't come as a scary surprise.
func (a *App) confirmTrust() bool {
	res, err := runtime.MessageDialog(a.ctx, runtime.MessageDialogOptions{
		Type:  runtime.QuestionDialog,
		Title: "Set up HTTPS for your projects",
		Message: "LokalKit made its own local certificate so https://project.localhost works without browser warnings.\n\n" +
			"Next, Windows will show a \"Security Warning\" asking to install a certificate from \"LokalKit CA\". " +
			"This is expected: click Yes. It is only trusted on this computer and you only do this once.",
		Buttons:       []string{"Continue", "Not now"},
		DefaultButton: "Continue",
		CancelButton:  "Not now",
	})
	return err != nil || res == "Continue" || res == "Yes" || res == "Ok"
}

// ---------- LokalKit updates ----------

type UpdateInfo struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Notes     string `json:"notes"`
	PageURL   string `json:"pageUrl"`
	Error     string `json:"error"`
}

// CheckForUpdate asks GitHub Releases for a newer LokalKit (cached 12h).
func (a *App) CheckForUpdate(force bool) UpdateInfo {
	a.updMu.Lock()
	defer a.updMu.Unlock()
	if force || time.Since(a.updChecked) > 12*time.Hour {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		rel, err := updater.Latest(ctx, updater.FeedURL)
		a.updChecked, a.updRelease, a.updErr = time.Now(), rel, ""
		if err != nil {
			a.updErr = err.Error()
		}
	}
	info := UpdateInfo{Current: AppVersion, Error: a.updErr}
	if r := a.updRelease; r != nil {
		info.Latest, info.Notes, info.PageURL = r.Version, r.Notes, r.PageURL
		info.Available = updater.Newer(r.Version, AppVersion)
	}
	return info
}

// Self-updating (download + replace the running exe + restart) is turned
// off for now: in an unsigned exe that pattern gets the download flagged by
// antivirus. The banner opens the release page instead. The code for it is
// kept in internal/updater (Download, Replace) for when builds are signed.

// ---------- Databases tab ----------

type DatabasesView struct {
	Running   bool                   `json:"running"`
	Installed bool                   `json:"installed"`
	Databases []core.DatabaseInfo    `json:"databases"`
	Missing   []core.MissingDatabase `json:"missing"`
	BackupDir string                 `json:"backupDir"`
	Error     string                 `json:"error"`
}

func (a *App) GetDatabases() DatabasesView {
	if a.m == nil {
		return DatabasesView{}
	}
	v := DatabasesView{BackupDir: filepath.Join(a.m.P.Root, "backups")}
	for _, s := range a.m.Services() {
		if s.ID == "mysql" {
			v.Running, v.Installed = s.Running, s.Installed
		}
	}
	if !v.Running {
		return v
	}
	dbs, err := a.m.ListDatabases()
	if err != nil {
		v.Error = err.Error()
	}
	v.Databases = dbs
	v.Missing = a.m.MissingDatabases()
	return v
}

func (a *App) CreateDatabase(name string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.CreateMySQLDatabase(name)
}

func (a *App) CreateProjectDatabase(project string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.CreateProjectDatabase(project)
}

// ExportDatabase saves a .sql backup and shows it in Explorer.
func (a *App) ExportDatabase(name string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	path, err := a.m.ExportDatabase(name)
	if err == nil {
		_ = core.OpenPath(filepath.Dir(path))
	}
	return path, err
}

// ImportSQLFile asks for a .sql/.sql.gz file and imports it into name.
// Returns false when the user cancelled the file dialog.
func (a *App) ImportSQLFile(name string) (bool, error) {
	if err := a.ready(); err != nil {
		return false, err
	}
	file, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "Choose a .sql file to import into " + name,
		Filters: []runtime.FileFilter{{DisplayName: "SQL files (*.sql, *.sql.gz)", Pattern: "*.sql;*.sql.gz;*.gz"}},
	})
	if err != nil || file == "" {
		return false, err
	}
	return true, a.m.ImportSQL(name, file)
}

func (a *App) DropDatabase(name string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.DropDatabase(name)
}

// StartDev / StopDev run an AdonisJS project's dev server.
func (a *App) StartDev(name string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.StartDev(name)
}

func (a *App) StopDev(name string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.StopDev(name)
}

// FixAppURL points a project's APP_URL at its LokalKit address.
func (a *App) FixAppURL(name string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.FixAppURL(name)
}

// FixHosts adds every project to the Windows hosts file (asks for admin).
func (a *App) FixHosts() ([]string, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	err := a.m.SyncHostsNow()
	return a.m.MissingFromHosts(), err
}

// SetProjectPHP pins a project to a PHP branch ("8.3"), or "" for the default PHP.
func (a *App) SetProjectPHP(name, branch string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.SetProjectPHP(name, branch)
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

func (a *App) CreateProject(name, kind string, db core.DBChoice) (core.Project, error) {
	if err := a.ready(); err != nil {
		return core.Project{}, err
	}
	return a.m.CreateProject(name, kind, db)
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
	paths := map[string]string{"root": p.Root, "www": p.WWW, "bin": p.Bin, "logs": p.Logs, "etc": p.Etc, "ssl": a.m.SSLStatus().Dir, "backups": filepath.Join(p.Root, "backups")}
	if dir, ok := paths[key]; ok {
		return core.OpenPath(dir)
	}
	// Otherwise it is a project name (its folder may be outside www).
	for _, pr := range a.m.Projects() {
		if pr.Name == key {
			return core.OpenPath(pr.Path)
		}
	}
	return core.OpenPath(filepath.Join(p.WWW, filepath.Base(key)))
}

// ---------- importing existing projects ----------

// DetectLaragon returns the Laragon folder (e.g. C:\laragon) if found.
func (a *App) DetectLaragon() string { return core.DetectLaragon() }

// PickFolder shows a folder chooser.
func (a *App) PickFolder(title string) (string, error) {
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{Title: title})
}

func (a *App) ScanImport(dir string) ([]core.ImportCandidate, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.m.ScanImport(dir)
}

func (a *App) ImportProjects(paths []string) ([]string, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	return a.m.ImportProjects(paths)
}

func (a *App) UnlinkProject(name string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.UnlinkProject(name)
}

// LegacyDatabases lists databases in another tool's MySQL data folder.
func (a *App) LegacyDatabases(root string) (core.LegacyDatabases, error) {
	if err := a.ready(); err != nil {
		return core.LegacyDatabases{}, err
	}
	return a.m.LegacyDatabasesIn(root)
}

func (a *App) ImportDatabases(root string, dbs []string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.m.ImportDatabases(root, dbs)
}
