//go:build !windows

package tray

import "lokalkit/internal/core"

// Tray is a no-op outside Windows (LokalKit targets Windows; this keeps
// development builds and tests working on other systems).
type Tray struct{}

func Start(a Actions, initial []core.ServiceInfo) *Tray { return nil }

func (t *Tray) Update(services []core.ServiceInfo) {}

func (t *Tray) Stop() {}
