package service

// Purpose: the platform-neutral half of `cascade widget install|uninstall|
//   status` - the macOS menu-bar widget's LaunchAgent. It declares the
//   WidgetInstaller interface, the WidgetConfig every implementation is
//   driven by, the report shapes the CLI renders, and the fixed bundle
//   resolution (two absolute candidates, never an environment variable).
//   The launchd work itself is widget_darwin.go; every other GOOS gets the
//   typed refusal in widget_other.go.
// Inputs: a WidgetConfig the CLI composition root assembles: home directory,
//   uid, the cascade binary path, a runtime.PathProvider, an injected Getenv
//   (read for CASCADE_HOME only), an injected Clock and two command seams
//   (Runner for launchctl actions, OutputRunner for `launchctl print`).
// Outputs: DeltaReport / WidgetInstallReport / WidgetStatusReport values or a
//   pkg/cascade taxonomy error.
// Constraints: the bundle path comes from the two fixed candidates and
//   nothing else - no environment variable, no `open`, nothing relative to
//   the cascade binary. Every filesystem path derives from HomeDir or the
//   injected PathProvider, so tests run entirely under t.TempDir().
// SPORT: internal/daemon/service (P1-WID-04).

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	// WidgetLabel is the launchd label of the menu-bar widget agent.
	WidgetLabel = "com.acamarata.cascade.widget"
	// WidgetBundleName is the only bundle base name an install accepts.
	WidgetBundleName = "CascadeMenuBar.app"
	// widgetExecutable is the Mach-O inside the bundle launchd runs.
	widgetExecutable = "CascadeMenuBar"
)

// OutputRunner runs a command and returns its standard output. It is the
// output-capturing sibling of Runner, used for `launchctl print`.
type OutputRunner interface {
	Output(name string, args ...string) ([]byte, error)
}

// ExecOutputRunner is the production OutputRunner (real os/exec).
type ExecOutputRunner struct{}

// Output runs name with args and returns its standard output.
func (ExecOutputRunner) Output(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output() //nolint:gosec // fixed launchctl invocation, never user input
}

// WidgetConfig carries every external input the widget installer needs.
type WidgetConfig struct {
	// HomeDir roots ~/Library/LaunchAgents and ~/Applications.
	HomeDir string
	// SystemRoot prefixes /Applications ("" is the real root; tests pass a
	// temp dir). It is injected, never read from the environment.
	SystemRoot string
	// Candidates overrides the derived bundle candidate list; tests use it
	// to plant a wrong base name. Production leaves it nil.
	Candidates []string
	// UID builds launchd's gui/<uid> domain.
	UID int
	// Executable is the cascade binary path (os.Executable()); install
	// records its symlink-resolved target as CASCADE_CLI_PATH.
	Executable string
	// Paths supplies the socket path, data dir and log dir.
	Paths runtime.PathProvider
	// Getenv is read for CASCADE_HOME only.
	Getenv runtime.Getenv
	// Clock stamps installed_at.
	Clock runtime.Clock
	// Runner executes launchctl bootout/bootstrap.
	Runner Runner
	// Output captures `launchctl print`.
	Output OutputRunner
}

// WidgetInstallReport is DeltaReport plus the install timestamp.
type WidgetInstallReport struct {
	DeltaReport
	InstalledAt string `json:"installed_at"`
}

// WidgetInstaller manages the widget LaunchAgent. Install and Uninstall are
// idempotent exactly like Installer's.
type WidgetInstaller interface {
	Install(cfg WidgetConfig) (WidgetInstallReport, error)
	Uninstall(cfg WidgetConfig) (DeltaReport, error)
	Status(cfg WidgetConfig) (WidgetStatusReport, error)
}

// widgetCandidates returns the two fixed bundle locations, in priority order.
func widgetCandidates(cfg WidgetConfig) []string {
	if cfg.Candidates != nil {
		return cfg.Candidates
	}
	return []string{
		filepath.Join(cfg.SystemRoot, "/Applications", WidgetBundleName),
		filepath.Join(cfg.HomeDir, "Applications", WidgetBundleName),
	}
}

// resolveWidgetBundle returns the first existing candidate. A winner whose
// base name is not CascadeMenuBar.app is refused (KindInvalidInput); no
// existing candidate is KindNotFound naming every path tried.
func resolveWidgetBundle(cfg WidgetConfig) (string, error) {
	cands := widgetCandidates(cfg)
	for _, c := range cands {
		ok, err := usableWidgetCandidate(c)
		if err != nil {
			return "", err
		}
		if ok {
			return c, nil
		}
	}
	return "", cascade.Newf(cascade.KindNotFound,
		"widget install: no %s found; looked in %s", WidgetBundleName, joinPaths(cands))
}

// widgetExecutablePath is <bundle>/Contents/MacOS/CascadeMenuBar.
func widgetExecutablePath(bundle string) string {
	return filepath.Join(bundle, "Contents", "MacOS", widgetExecutable)
}

// usableWidgetCandidate judges one candidate without following symlinks. An
// absent or non-directory candidate is skipped; a symlink, a wrong base name
// or a bundle without a regular executable file is an error naming it.
func usableWidgetCandidate(c string) (bool, error) {
	info, err := os.Lstat(c)
	if err != nil {
		return false, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, cascade.Newf(cascade.KindInvalidInput,
			"widget install: %s is a symlink; the bundle must be a real directory", c)
	}
	if !info.IsDir() {
		return false, nil
	}
	if filepath.Base(c) != WidgetBundleName {
		return false, cascade.Newf(cascade.KindInvalidInput,
			"widget install: %s is not a %s bundle", c, WidgetBundleName)
	}
	exe := widgetExecutablePath(c)
	ei, err := os.Lstat(exe)
	if err != nil || !ei.Mode().IsRegular() || ei.Mode().Perm()&0o111 == 0 {
		return false, cascade.Newf(cascade.KindNotFound,
			"widget install: %s is not a regular executable file", exe)
	}
	return true, nil
}

// joinPaths renders candidates as "a and b".
func joinPaths(paths []string) string {
	out := ""
	for i, p := range paths {
		if i > 0 {
			out += " and "
		}
		out += p
	}
	return out
}

// widgetPlistPath is ~/Library/LaunchAgents/com.acamarata.cascade.widget.plist.
func widgetPlistPath(homeDir string) string {
	return filepath.Join(homeDir, "Library", "LaunchAgents", WidgetLabel+".plist")
}

// widgetEnvironment builds the exact EnvironmentVariables set the plist
// records. CASCADE_HOME appears only when the installer's own environment
// sets it non-empty.
func widgetEnvironment(cfg WidgetConfig) (map[string]string, error) {
	cli, err := filepath.EvalSymlinks(cfg.Executable)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindNotFound, err, "widget install: resolve the cascade executable")
	}
	env := map[string]string{
		"CASCADE_CLI_PATH":           cli,
		"CASCADE_SOCKET_PATH":        cfg.Paths.SocketPath(),
		"CASCADE_WIDGET_STATUS_PATH": widgetStatusFilePath(cfg),
	}
	if cfg.Getenv != nil {
		if home := cfg.Getenv("CASCADE_HOME"); home != "" {
			env["CASCADE_HOME"] = home
		}
	}
	return env, nil
}

// widgetLogPath is the stdout/stderr path the installer writes into the plist.
func widgetLogPath(cfg WidgetConfig) string {
	return filepath.Join(cfg.Paths.LogDir(), "widget.log")
}

// widgetStatusFilePath is the self-report path the installer records.
func widgetStatusFilePath(cfg WidgetConfig) string {
	return filepath.Join(cfg.Paths.DataDir(), "widget", "widget-status.json")
}

// validateWidgetInstall checks the inputs Install needs before it touches
// the filesystem or a command seam.
func validateWidgetInstall(cfg WidgetConfig) error {
	switch {
	case cfg.HomeDir == "":
		return cascade.New(cascade.KindInvalidInput, "widget install: missing home directory")
	case cfg.Executable == "":
		return cascade.New(cascade.KindInvalidInput, "widget install: missing executable path")
	case cfg.Paths == nil || cfg.Clock == nil || cfg.Runner == nil:
		return cascade.New(cascade.KindInvalidInput, "widget install: paths, clock and runner are required")
	}
	return nil
}
