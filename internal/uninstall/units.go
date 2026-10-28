package uninstall

// Purpose: the daemon-unit and widget-agent items: each removes its unit
//   through the platform installer's own Uninstall (which refuses a file
//   it did not write) and reports would-remove or absent from a stat under
//   a dry run.
// Inputs: Deps.Services with DaemonCfg, Deps.Widgets with WidgetCfg.
// Outputs: unitItems.
// Constraints: no unit items on windows (no service management there).
//   The widget agent exists only on darwin. A dry run only stats.
// SPORT: internal/uninstall unit items/ADDED.

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/internal/daemon/service"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The unit file names the platform installers write (service_darwin.go,
// service_linux.go, widget.go).
const (
	daemonLaunchdLabel = "com.acamarata.cascade"
	daemonSystemdUnit  = "cascade.service"
)

// unitItem is one service unit.
type unitItem struct {
	kind    Kind
	locator string
	d       Deps
	remove  func() (service.DeltaReport, error)
}

func (u unitItem) Kind() Kind      { return u.kind }
func (u unitItem) Locator() string { return u.locator }
func (u unitItem) Owner() string   { return "cascade" }
func (u unitItem) Class() Class    { return ClassInstall }

// Remove uninstalls the unit, or stats it under a dry run.
func (u unitItem) Remove(_ context.Context, dryRun bool) (Delta, error) {
	delta := Delta{Kind: u.kind, Locator: u.locator}
	if err := guardPath(&delta, u.d); err != nil {
		return delta, err
	}
	if dryRun {
		exists, _, err := lstatRegular(u.locator)
		if err != nil {
			return delta, cascade.Wrapf(cascade.KindUnavailable, err, "uninstall: stat %s", u.locator)
		}
		delta.Action = ActionAbsent
		if exists {
			delta.Action = ActionWouldRemove
		}
		return delta, nil
	}
	rep, err := u.remove()
	if err != nil {
		delta.Action = ActionKept
		return delta, err
	}
	delta.Action = ActionRemoved
	if rep.Action == service.ActionNotInstalled {
		delta.Action = ActionAbsent
	}
	return delta, nil
}

// unitItems is the daemon unit then the widget agent, for the platforms
// that have them.
func unitItems(d Deps) []Item {
	if d.GOOS == "windows" {
		return nil
	}
	var out []Item
	if d.DaemonCfg != nil && d.Services != nil {
		cfg := *d.DaemonCfg
		out = append(out, unitItem{kind: KindDaemonUnit, locator: daemonUnitPath(d.GOOS, cfg.HomeDir), d: d,
			remove: func() (service.DeltaReport, error) { return d.Services.Uninstall(cfg) }})
	}
	if d.GOOS == "darwin" && d.WidgetCfg != nil {
		widgets := d.Widgets
		if widgets == nil {
			widgets = service.NewWidgetInstaller()
		}
		c := *d.WidgetCfg
		wcfg := service.WidgetConfig{HomeDir: c.HomeDir, UID: c.UID, Executable: c.Executable, Runner: c.Runner, Paths: d.Paths}
		out = append(out, unitItem{kind: KindWidgetAgent, d: d,
			locator: filepath.Join(c.HomeDir, "Library", "LaunchAgents", service.WidgetLabel+".plist"),
			remove:  func() (service.DeltaReport, error) { return widgets.Uninstall(wcfg) }})
	}
	return out
}

// daemonUnitPath is the unit file the platform installer writes.
func daemonUnitPath(goos, home string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", daemonLaunchdLabel+".plist")
	}
	return filepath.Join(home, ".config", "systemd", "user", daemonSystemdUnit)
}
