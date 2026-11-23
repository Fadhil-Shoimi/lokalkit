package tray

import (
	"testing"

	"lokalkit/internal/core"
)

func TestSummarize(t *testing.T) {
	svcs := []core.ServiceInfo{
		{ID: "apache", Installed: true, Running: true},
		{ID: "mysql", Installed: true},
		{ID: "mailpit"}, // not installed: ignored
	}
	s := Summarize(svcs)
	if s.Running != 1 || s.Total != 2 || !s.StopMode || s.Tooltip != "LokalKit: 1/2 services running" {
		t.Errorf("%+v", s)
	}
	svcs[0].Running = false
	if s := Summarize(svcs); s.StopMode || s.Tooltip != "LokalKit: all services stopped" {
		t.Errorf("%+v", s)
	}
	if s := Summarize(nil); s.Tooltip != "LokalKit: no services installed" {
		t.Errorf("%+v", s)
	}
	if got := ServiceTitle(core.ServiceInfo{Name: "MySQL", Version: "8.4.3", Busy: true}); got != "MySQL 8.4.3  …" {
		t.Errorf("title %q", got)
	}
}
