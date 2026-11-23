//go:build windows

package tray

import (
	_ "embed"
	"runtime"
	"sync"

	"lokalkit/internal/core"

	"fyne.io/systray"
)

//go:embed icons/tray-on.ico
var iconOn []byte

//go:embed icons/tray-off.ico
var iconOff []byte

type Tray struct {
	a     Actions
	mu    sync.Mutex
	ready bool
	last  []core.ServiceInfo

	all      *systray.MenuItem
	services map[string]*systray.MenuItem
	order    []string
	iconIsOn *bool
}

// Start shows the tray icon. The Windows tray window and its message loop
// must live on the same OS thread, so the whole tray runs on its own locked
// goroutine, separate from the Wails UI thread.
func Start(a Actions, initial []core.ServiceInfo) *Tray {
	t := &Tray{a: a, last: initial, services: map[string]*systray.MenuItem{}}
	go func() {
		runtime.LockOSThread()
		systray.Run(t.onReady, nil)
	}()
	return t
}

func (t *Tray) onReady() {
	systray.SetIcon(iconOff)
	systray.SetTitle("LokalKit")
	systray.SetTooltip("LokalKit")
	systray.SetOnTapped(t.a.Show)

	open := systray.AddMenuItem("Open LokalKit", "Show the LokalKit window")
	systray.AddSeparator()
	t.all = systray.AddMenuItem("Start All", "Start or stop all services")

	t.mu.Lock()
	initial := t.last
	t.mu.Unlock()
	for _, svc := range initial {
		item := systray.AddMenuItemCheckbox(ServiceTitle(svc), "Click to start/stop", svc.Running)
		t.services[svc.ID] = item
		t.order = append(t.order, svc.ID)
		id := svc.ID
		go listen(item, func() { t.a.Toggle(id) })
	}

	systray.AddSeparator()
	web := systray.AddMenuItem("Open localhost", "")
	db := systray.AddMenuItem("Database", "")
	term := systray.AddMenuItem("Terminal", "")
	www := systray.AddMenuItem("www folder", "")
	mail := systray.AddMenuItem("Mailpit (email)", "")
	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit", "Stop all services and quit")

	go listen(open, t.a.Show)
	go listen(t.all, func() {
		t.mu.Lock()
		stop := Summarize(t.last).StopMode
		t.mu.Unlock()
		if stop {
			t.a.StopAll()
		} else {
			t.a.StartAll()
		}
	})
	go listen(web, t.a.OpenWeb)
	go listen(db, t.a.OpenDatabase)
	go listen(term, t.a.OpenTerminal)
	go listen(www, t.a.OpenWWW)
	go listen(mail, t.a.OpenMailpit)
	go listen(quit, t.a.Quit)

	t.mu.Lock()
	t.ready = true
	t.mu.Unlock()
	t.Update(initial)
}

func listen(item *systray.MenuItem, fn func()) {
	for range item.ClickedCh {
		if fn != nil {
			fn()
		}
	}
}

// Update refreshes the icon, tooltip and menu from the latest service state.
func (t *Tray) Update(services []core.ServiceInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last = services
	if !t.ready {
		return
	}
	sum := Summarize(services)
	on := sum.Running > 0
	if t.iconIsOn == nil || *t.iconIsOn != on {
		if on {
			systray.SetIcon(iconOn)
		} else {
			systray.SetIcon(iconOff)
		}
		t.iconIsOn = &on
	}
	systray.SetTooltip(sum.Tooltip)

	if sum.StopMode {
		t.all.SetTitle("Stop All")
	} else {
		t.all.SetTitle("Start All")
	}
	if sum.AnyBusy || sum.Total == 0 {
		t.all.Disable()
	} else {
		t.all.Enable()
	}

	for _, svc := range services {
		item, ok := t.services[svc.ID]
		if !ok {
			continue
		}
		if !svc.Installed {
			item.Hide()
			continue
		}
		item.Show()
		item.SetTitle(ServiceTitle(svc))
		if svc.Running {
			item.Check()
		} else {
			item.Uncheck()
		}
		if svc.Busy {
			item.Disable()
		} else {
			item.Enable()
		}
	}
}

// Stop removes the tray icon.
func (t *Tray) Stop() { systray.Quit() }
