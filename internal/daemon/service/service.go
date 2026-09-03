// Package service implements `cascade daemon install`/`uninstall`
// (07-CLI-COMMAND-TREE.md §daemon: "install | uninstall (launchd/systemd
// units)") — platform-native service-unit management for the cascade
// daemon.
package service

// Purpose: the platform-neutral half of install/uninstall — the Installer
//   interface every platform implementation satisfies, the Config every
//   implementation is driven by, and the small set of helpers (managed-
//   file write with clobber refusal, input validation) shared verbatim by
//   service_darwin.go and service_linux.go. This file imports nothing
//   platform-specific and carries no //go:build tag, so it compiles
//   identically on every GOOS; the platform split lives entirely in
//   service_darwin.go / service_linux.go / service_windows.go, matching
//   this repo's established pattern (internal/daemon/lifecycle_unix.go vs
//   lifecycle_windows.go).
// Inputs: a Config the caller (cmd/cascade/daemon.go) assembles from
//   daemonDeps — HomeDir/Executable/LogPath/UID resolved once at the
//   composition root, a Runner that executes the platform service manager
//   CLI (launchctl/systemctl), never touched directly by this package.
// Outputs: a DeltaReport describing what Install/Uninstall did, or a
//   pkg/cascade taxonomy error.
// Constraints: Art.7.1 — every path this package touches comes from
//   Config.HomeDir, so tests drive it entirely under t.TempDir() and never
//   write to a real system path. Art.1 — Install/Uninstall never report an
//   outcome they did not actually perform; a Runner failure on a
//   correctness-bearing step always surfaces as an error, never a silently
//   downgraded DeltaReport.
// SPORT: internal/daemon/service (ADD, per T-2 sport_updates).

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Delta report actions. Install reports ActionInstalled on a fresh install
// and ActionReloaded on a convergent re-run (§5.9 idempotency AC). Uninstall
// reports ActionRemoved when it removed a real unit and ActionNotInstalled
// when there was nothing to remove — never a non-zero exit for either
// no-op case.
const (
	ActionInstalled    = "installed"
	ActionReloaded     = "reloaded"
	ActionRemoved      = "removed"
	ActionNotInstalled = "not installed"
)

// Config carries every external input a platform Installer needs. Every
// field is supplied by the caller (cmd/cascade/daemon.go's composition
// root in production, a test's literal values otherwise) so this package
// never reaches for the real environment itself.
type Config struct {
	// HomeDir is the user's real home directory — the root that platform
	// service directories (~/Library/LaunchAgents, ~/.config/systemd/user)
	// are resolved under. Distinct from CASCADE_HOME/runtime.PathProvider's
	// Root(): service units are OS-level artifacts, not cascade-managed
	// state. Tests always pass t.TempDir().
	HomeDir string
	// Executable is the absolute path to the cascade binary, as resolved
	// by os.Executable() at the composition root. Empty is a validation
	// error (the "missing binary" error path).
	Executable string
	// LogPath is the absolute path the generated unit directs the
	// daemon's stdout/stderr to (launchd's StandardOutPath/
	// StandardErrorPath; kept for parity on linux even though systemd
	// units normally rely on the journal).
	LogPath string
	// UID is the effective user id, used to build launchd's gui/<uid>
	// domain target. Unused on linux. Callers inject this (os.Getuid() in
	// production) rather than this package calling it directly.
	UID int
	// Runner executes the platform service-manager CLI (launchctl on
	// darwin, systemctl on linux). Production callers supply ExecRunner;
	// every test supplies a fake that performs no real exec call — this
	// package's own tests never shell out to launchctl/systemctl.
	Runner Runner
}

// DeltaReport describes what Install or Uninstall actually did.
type DeltaReport struct {
	Action string `json:"action"`
	Detail string `json:"detail"`
}

// Installer manages a platform-native service unit for the cascade daemon.
// Install and Uninstall are both idempotent: a second Install on an
// already-managed unit converges (ActionReloaded, exit 0) and a second
// Uninstall on an absent unit is a clean no-op (ActionNotInstalled, exit
// 0) — neither case is ever reported as an error.
type Installer interface {
	Install(cfg Config) (DeltaReport, error)
	Uninstall(cfg Config) (DeltaReport, error)
}

// Runner executes a named external command with args, matching the shape
// exec.Cmd.Run() needs without exposing *exec.Cmd itself — a fake Runner
// in tests never touches a real process.
type Runner interface {
	Run(name string, args ...string) error
}

// ExecRunner is the production Runner: a real os/exec invocation. Only
// cmd/cascade's composition root constructs one; internal/daemon/service's
// own tests always use a fake (Art.7.1 — this package's tests never
// install a real service on the developer's machine).
type ExecRunner struct{}

// Run executes name with args, discarding stdout/stderr (launchctl/
// systemctl are not chatty on success; failures are reported via the
// process's exit status, which exec.Cmd.Run() surfaces as an error).
func (ExecRunner) Run(name string, args ...string) error {
	return exec.Command(name, args...).Run() //nolint:gosec // name/args are fixed service-manager invocations, never user input
}

// requireHomeDir checks the one input every Uninstall needs regardless of
// platform: a resolved home directory to locate the unit under.
func requireHomeDir(cfg Config) error {
	if cfg.HomeDir == "" {
		return cascade.New(cascade.KindInvalidInput, "daemon service: missing home directory")
	}
	return nil
}

// validateInstallConfig checks the inputs every platform Install needs
// before touching the filesystem or a Runner. A missing Executable is
// this package's "missing binary" error path.
func validateInstallConfig(cfg Config) error {
	if err := requireHomeDir(cfg); err != nil {
		return err
	}
	if cfg.Executable == "" {
		return cascade.New(cascade.KindInvalidInput, "daemon service: missing executable path")
	}
	return nil
}

// writeManagedFile writes content to path, refusing to clobber a file that
// already exists there unless isManaged reports that cascade itself wrote
// it (the marker embedded in a prior render). This is the "do not clobber
// an existing unit you did not write" contract: refusing is the
// conservative default, and re-installing over cascade's own prior unit is
// how idempotent convergence (ActionReloaded) is possible at all.
//
// Returns existed=true when a file was already at path (managed or not);
// foreign=true when that file was NOT cascade-managed, in which case the
// caller must refuse and no write has happened.
func writeManagedFile(path string, content []byte, isManaged func([]byte) bool) (existed, foreign bool, err error) {
	current, readErr := os.ReadFile(path) //nolint:gosec // path is derived from Config.HomeDir, never user-controlled at this layer
	switch {
	case readErr == nil:
		existed = true
		if !isManaged(current) {
			return true, true, nil
		}
	case os.IsNotExist(readErr):
		// nothing there yet — proceed to write.
	default:
		return false, false, wrapFSError(readErr, "read existing service unit")
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // service-manager directories are conventionally world-readable
		return existed, false, wrapFSError(err, "create service unit directory")
	}
	if err := os.WriteFile(path, content, 0o644); err != nil { //nolint:gosec // unit files are conventionally world-readable, matching real launchd/systemd units
		return existed, false, wrapFSError(err, "write service unit")
	}
	return existed, false, nil
}

// removeManagedFile removes path if present, treating an already-absent
// file as success (the uninstall-when-absent no-op case is handled by the
// caller before this is ever reached, but a concurrent removal between the
// caller's Stat and this Remove must not surface as an error either).
func removeManagedFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return wrapFSError(err, "remove service unit")
	}
	return nil
}

// wrapFSError classifies a filesystem error into the taxonomy: permission
// errors become KindPermissionDenied (the "permission denied" error path
// every platform test exercises), everything else KindUnavailable (a
// transient/local dependency — the filesystem — was not usable).
func wrapFSError(err error, msg string) error {
	if os.IsPermission(err) {
		return cascade.Wrap(cascade.KindPermissionDenied, err, msg)
	}
	return cascade.Wrap(cascade.KindUnavailable, err, msg)
}

// wrapRunnerError classifies a Runner failure. A service-manager CLI that
// fails to run (missing binary, non-zero exit) is a locally-unavailable
// dependency, not a data problem.
func wrapRunnerError(err error, msg string) error {
	return cascade.Wrap(cascade.KindUnavailable, err, msg)
}

// foreignUnitError is returned when writeManagedFile finds a file at the
// target path that cascade did not write.
func foreignUnitError(path string) error {
	return cascade.Newf(cascade.KindConflict,
		"refusing to overwrite existing service unit not managed by cascade: %s", path)
}

// containsMarker reports whether content contains marker, the small helper
// isManaged predicates share.
func containsMarker(content []byte, marker string) bool {
	return bytes.Contains(content, []byte(marker))
}
