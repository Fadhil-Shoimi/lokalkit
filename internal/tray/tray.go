// Package tray shows LokalKit's icon and menu in the Windows notification area.
package tray

import "lokalkit/internal/core"

// Actions are the callbacks the tray menu triggers. They run on the tray's
// goroutine, so each should return quickly (start long work in a goroutine).
type Actions struct {
	Show         func()
	StartAll     func()
	StopAll      func()
	Toggle       func(id string)
	OpenWeb      func()
	OpenDatabase func()
	OpenTerminal func()
	OpenWWW      func()
	OpenMailpit  func()
	Quit         func()
}

// Summary is the text and icon state derived from the services.
type Summary struct {
	Running  int
	Total    int
	AnyBusy  bool
	Tooltip  string
	StopMode bool // the "all" item should say "Henti Semua"
}

// Summarize computes the tooltip and state shown by the tray.
func Summarize(services []core.ServiceInfo) Summary {
	var s Summary
	for _, svc := range services {
		if !svc.Installed {
			continue
		}
		s.Total++
		if svc.Running {
			s.Running++
		}
		if svc.Busy {
			s.AnyBusy = true
		}
	}
	s.StopMode = s.Running > 0
	switch {
	case s.Total == 0:
		s.Tooltip = "LokalKit: tiada servis dipasang"
	case s.Running == 0:
		s.Tooltip = "LokalKit: semua servis berhenti"
	case s.Running == s.Total:
		s.Tooltip = "LokalKit: semua servis berjalan"
	default:
		s.Tooltip = "LokalKit: " + itoa(s.Running) + "/" + itoa(s.Total) + " servis berjalan"
	}
	return s
}

// ServiceTitle is the menu label for one service.
func ServiceTitle(svc core.ServiceInfo) string {
	t := svc.Name
	if svc.Version != "" {
		t += " " + svc.Version
	}
	if svc.Busy {
		t += "  …"
	}
	return t
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}
