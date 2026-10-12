//go:build darwin

package service

// Purpose: the darwin widget installer - writes the widget LaunchAgent plist
//   at ~/Library/LaunchAgents/com.acamarata.cascade.widget.plist and registers
//   it with `launchctl bootout` (ignored) then `launchctl bootstrap`, mirrors
//   service_darwin.go's daemon agent, and reads status back from launchctl
//   and the widget's self-report file.
// Inputs: WidgetConfig (all injected).
// Outputs: WidgetInstallReport, DeltaReport, WidgetStatusReport or a taxonomy
//   error.
// Constraints: never touches a path outside HomeDir and the injected
//   PathProvider; never execs outside the injected Runner/OutputRunner; an
//   unmanaged plist at the target path is refused and left untouched.
// SPORT: internal/daemon/service (P1-WID-04).

import (
	"os"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// NewWidgetInstaller returns the darwin widget installer.
func NewWidgetInstaller() WidgetInstaller { return darwinWidgetInstaller{} }

type darwinWidgetInstaller struct{}

// widgetTarget is launchctl's gui/<uid>/<widget label> service target.
func widgetTarget(uid int) string { return domainTarget(uid) + "/" + WidgetLabel }

// Install resolves the bundle, writes the plist (refusing an unmanaged file),
// then boots out (ignored) and bootstraps the agent.
func (darwinWidgetInstaller) Install(cfg WidgetConfig) (WidgetInstallReport, error) {
	if err := validateWidgetInstall(cfg); err != nil {
		return WidgetInstallReport{}, err
	}
	bundle, err := resolveWidgetBundle(cfg)
	if err != nil {
		return WidgetInstallReport{}, err
	}
	env, err := widgetEnvironment(cfg)
	if err != nil {
		return WidgetInstallReport{}, err
	}
	path := widgetPlistPath(cfg.HomeDir)
	if err := refuseForeignWidgetPlist(path); err != nil {
		return WidgetInstallReport{}, err
	}
	logDir := cfg.Paths.LogDir()
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return WidgetInstallReport{}, wrapFSError(err, "create widget log directory")
	}
	content := renderWidgetPlist(widgetExecutablePath(bundle), env, widgetLogPath(cfg))
	existed, foreign, err := writeManagedFile(path, content, isManagedWidgetPlist)
	if err != nil {
		return WidgetInstallReport{}, err
	}
	if foreign {
		return WidgetInstallReport{}, foreignUnitError(path)
	}
	_ = cfg.Runner.Run("launchctl", "bootout", widgetTarget(cfg.UID))
	if err := cfg.Runner.Run("launchctl", "bootstrap", domainTarget(cfg.UID), path); err != nil {
		return WidgetInstallReport{}, wrapRunnerError(err, "launchctl bootstrap")
	}
	rep := WidgetInstallReport{InstalledAt: cfg.Clock.Now().UTC().Format(time.RFC3339)}
	rep.Action, rep.Detail = ActionInstalled, "widget launch agent installed at "+path
	if existed {
		rep.Action, rep.Detail = ActionReloaded, "widget launch agent reloaded at "+path
	}
	return rep, nil
}

// refuseForeignWidgetPlist returns the conflict error when an unmanaged file
// holds the plist path, before anything is created on disk.
func refuseForeignWidgetPlist(path string) error {
	data, err := os.ReadFile(path) //nolint:gosec // derived from HomeDir
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return wrapFSError(err, "read widget launch agent")
	}
	if !isManagedWidgetPlist(data) {
		return foreignUnitError(path)
	}
	return nil
}

// Uninstall boots the agent out (best effort) and removes the plist. An
// absent plist is a clean no-op; an unmanaged one is refused.
func (darwinWidgetInstaller) Uninstall(cfg WidgetConfig) (DeltaReport, error) {
	if err := requireHomeDir(Config{HomeDir: cfg.HomeDir}); err != nil {
		return DeltaReport{}, err
	}
	if cfg.Runner == nil {
		return DeltaReport{}, cascade.New(cascade.KindInvalidInput, "widget uninstall: a runner is required")
	}
	path := widgetPlistPath(cfg.HomeDir)
	data, err := os.ReadFile(path) //nolint:gosec // derived from HomeDir
	if os.IsNotExist(err) {
		return DeltaReport{Action: ActionNotInstalled, Detail: "no widget launch agent at " + path}, nil
	}
	if err != nil {
		return DeltaReport{}, wrapFSError(err, "read widget launch agent")
	}
	if !isManagedWidgetPlist(data) {
		return DeltaReport{}, foreignUnitError(path)
	}
	_ = cfg.Runner.Run("launchctl", "bootout", widgetTarget(cfg.UID))
	if err := removeManagedFile(path); err != nil {
		return DeltaReport{}, err
	}
	return DeltaReport{Action: ActionRemoved, Detail: "widget launch agent removed from " + path}, nil
}

// Status reports installed, loaded and bundle state plus the widget's
// self-report. A missing plist is "not installed", not an error.
func (darwinWidgetInstaller) Status(cfg WidgetConfig) (WidgetStatusReport, error) {
	if err := requireHomeDir(Config{HomeDir: cfg.HomeDir}); err != nil {
		return WidgetStatusReport{}, err
	}
	path := widgetPlistPath(cfg.HomeDir)
	rep := WidgetStatusReport{PlistPath: path}
	data, err := os.ReadFile(path) //nolint:gosec // derived from HomeDir
	if os.IsNotExist(err) {
		return rep, nil
	}
	if err != nil {
		return WidgetStatusReport{}, wrapFSError(err, "read widget launch agent")
	}
	rep.Installed = true
	rep.Managed = isManagedWidgetPlist(data)
	if !rep.Managed {
		return rep, nil // never trust a plist this installer did not write
	}
	bundle, statusPath, ok := matchWidgetPlist(cfg, data)
	rep.ProgramValid = ok
	if !ok {
		return rep, nil
	}
	rep.BundlePath = bundle
	if info, serr := os.Stat(rep.BundlePath); serr == nil && info.IsDir() {
		rep.BundlePresent = true
	}
	if cfg.Output != nil {
		out, _ := cfg.Output.Output("launchctl", "print", widgetTarget(cfg.UID))
		rep.Loaded, rep.State = parseLaunchctlPrint(out, widgetTarget(cfg.UID))
	}
	readWidgetSelfReport(statusPath, &rep)
	return rep, nil
}
