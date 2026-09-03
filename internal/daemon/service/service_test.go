// Purpose: platform-agnostic tests for service.go's shared machinery — the
//
//	piece every platform Installer (darwin, linux, and, vacuously, windows)
//	is built from. These tests carry NO //go:build tag deliberately: this
//	repo's CI runs `go test ./...` on native runners for every supported
//	GOOS including windows-latest (.github/workflows/ci.yml), so anything
//	asserted here must hold on every platform. That is why these tests
//	exercise writeManagedFile/removeManagedFile/wrapFSError/
//	validateInstallConfig/requireHomeDir directly rather than going through
//	NewInstaller(): NewInstaller() resolves to a REAL platform Installer
//	(darwinInstaller/linuxInstaller/windowsInstaller) whose Install/
//	Uninstall behavior differs by design — windows refuses unconditionally
//	— so a generic "install succeeds" assertion routed through it would
//	fail on windows CI. The platform-specific end-to-end behavior (the
//	actual DeltaReport.Action values, golden-fixture validation) is
//	covered by service_darwin_test.go / service_linux_test.go /
//	service_windows_test.go instead, each build-tagged to the platform it
//	proves.
//
// Constraints: Art.7.1 — every test here writes only under t.TempDir();
//
//	none execs a real process except the one narrow ExecRunner smoke test,
//	which invokes a deliberately nonexistent binary name (a pure "not
//	found" PATH lookup failure — no service is touched, nothing is
//	installed) purely to prove ExecRunner.Run propagates exec.Command's
//	error instead of swallowing it.
//
// SPORT: internal/daemon/service (ADD, per T-2 sport_updates).
package service

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeRunner is the Runner every test in this package injects instead of
// ever shelling out to a real launchctl/systemctl. It records every call
// and can be configured to fail on specific "name arg1 arg2..." keys.
type fakeRunner struct {
	calls  []string
	failOn map[string]error
}

func (f *fakeRunner) Run(name string, args ...string) error {
	key := name
	for _, a := range args {
		key += " " + a
	}
	f.calls = append(f.calls, key)
	if f.failOn != nil {
		if err, ok := f.failOn[key]; ok {
			return err
		}
	}
	return nil
}

// TestNewInstaller_NonNil is the one assertion this file makes that
// touches NewInstaller() at all: every platform must return a non-nil
// Installer, regardless of what that Installer's Install/Uninstall then
// do.
func TestNewInstaller_NonNil(t *testing.T) {
	if NewInstaller() == nil {
		t.Fatal("NewInstaller() = nil, want a platform Installer")
	}
}

// TestInstallIdempotency proves the shared convergence semantics every
// platform Install method is built from: writeManagedFile on a fresh path
// reports existed=false, and a second call with cascade's own marker
// present reports existed=true and actually overwrites the content — the
// exact "reloaded" convergence case (§5.9). This is the logic
// darwin/linux's Install methods key their DeltaReport.Action off of.
func TestInstallIdempotency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cascade.unit")
	const marker = "# managed-by-cascade-test"
	isManaged := func(b []byte) bool { return containsMarker(b, marker) }

	first := []byte(marker + "\ngeneration=1\n")
	existed, foreign, err := writeManagedFile(path, first, isManaged)
	if err != nil {
		t.Fatalf("first writeManagedFile: %v", err)
	}
	if existed || foreign {
		t.Fatalf("first install: existed=%v foreign=%v, want false,false", existed, foreign)
	}

	second := []byte(marker + "\ngeneration=2\n")
	existed, foreign, err = writeManagedFile(path, second, isManaged)
	if err != nil {
		t.Fatalf("second writeManagedFile: %v", err)
	}
	if !existed || foreign {
		t.Fatalf("second install (convergence): existed=%v foreign=%v, want true,false", existed, foreign)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Fatalf("convergence did not update content: got %q, want %q", got, second)
	}
}

// TestUninstallAbsentUnitIsNoop proves the other half of §5.9: removing a
// unit that never existed (or was already removed) is never an error —
// this is the check every platform Uninstall performs before it makes any
// Runner call at all.
func TestUninstallAbsentUnitIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "absent.unit")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("precondition: path unexpectedly exists (err=%v)", err)
	}
	if err := removeManagedFile(path); err != nil {
		t.Fatalf("removeManagedFile(absent) = %v, want nil", err)
	}
}

// TestForeignFileRefused proves the clobber-refusal contract: a file at
// the target path that does not carry cascade's marker is left byte-for-
// byte untouched, and writeManagedFile reports foreign=true so the caller
// can refuse (the conservative default this ticket's brief requires).
func TestForeignFileRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cascade.unit")
	foreignContent := []byte("# hand-written by someone else\nkeep=me\n")
	if err := os.WriteFile(path, foreignContent, 0o644); err != nil {
		t.Fatalf("seed foreign file: %v", err)
	}

	isManaged := func(b []byte) bool { return containsMarker(b, "# managed-by-cascade-test") }
	existed, foreign, err := writeManagedFile(path, []byte("# managed-by-cascade-test\nours\n"), isManaged)
	if err != nil {
		t.Fatalf("writeManagedFile: %v", err)
	}
	if !existed || !foreign {
		t.Fatalf("existed=%v foreign=%v, want true,true", existed, foreign)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(got, foreignContent) {
		t.Fatalf("foreign file was modified: got %q, want untouched %q", got, foreignContent)
	}

	fErr := foreignUnitError(path)
	if !cascade.HasKind(fErr, cascade.KindConflict) {
		t.Fatalf("foreignUnitError kind = %v, want KindConflict", fErr)
	}
}

// TestValidateInstallConfig_MissingExecutable is this package's "missing
// binary" error path.
func TestValidateInstallConfig_MissingExecutable(t *testing.T) {
	err := validateInstallConfig(Config{HomeDir: t.TempDir()})
	if err == nil {
		t.Fatal("want an error for a missing Executable")
	}
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err kind = %v, want KindInvalidInput", err)
	}
}

// TestValidateInstallConfig_MissingHomeDir and TestRequireHomeDir_Missing
// cover the missing-home-directory validation both Install and Uninstall
// depend on.
func TestValidateInstallConfig_MissingHomeDir(t *testing.T) {
	err := validateInstallConfig(Config{Executable: "/usr/local/bin/cascade"})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("err kind = %v, want KindInvalidInput", err)
	}
}

func TestRequireHomeDir_Missing(t *testing.T) {
	if err := requireHomeDir(Config{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Errorf("requireHomeDir({}) kind = %v, want KindInvalidInput", err)
	}
	if err := requireHomeDir(Config{HomeDir: t.TempDir()}); err != nil {
		t.Errorf("requireHomeDir(valid) = %v, want nil", err)
	}
}

// TestWriteManagedFile_PermissionDenied is this package's "permission
// denied" error path: a parent directory with no write bit makes
// MkdirAll/WriteFile fail, and that failure must classify as
// KindPermissionDenied. Skipped on windows: Go's os.Mkdir mode bits do not
// enforce POSIX-style write denial there, so this technique cannot
// reliably reproduce the failure on that platform (a common Go stdlib
// test pattern, not a gap in this ticket's coverage — windows has no
// filesystem-permission code path here to begin with, since
// service_windows.go never touches the filesystem at all).
func TestWriteManagedFile_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced the same way on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission checks are bypassed")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("seed locked dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // let t.TempDir() clean up
	path := filepath.Join(locked, "nested", "cascade.unit")

	_, _, err := writeManagedFile(path, []byte("content"), func([]byte) bool { return true })
	if err == nil {
		t.Fatal("want a permission error, got nil")
	}
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("err kind = %v, want KindPermissionDenied", err)
	}
}

// TestWrapRunnerError_IsUnavailable proves an exec failure (the "exec
// failure" error path) classifies as KindUnavailable — a locally-
// unreachable dependency, matching how darwin/linux propagate a failed
// launchctl/systemctl invocation.
func TestWrapRunnerError_IsUnavailable(t *testing.T) {
	err := wrapRunnerError(os.ErrClosed, "launchctl bootstrap")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("err kind = %v, want KindUnavailable", err)
	}
}

// TestExecRunner_PropagatesFailure proves ExecRunner.Run does not swallow
// a failure. It invokes a deliberately nonexistent binary name — a PATH
// lookup failure exec.Command itself reports before any process is ever
// created, so nothing is installed, started, or touched on the machine.
func TestExecRunner_PropagatesFailure(t *testing.T) {
	err := ExecRunner{}.Run("cascade-service-test-nonexistent-binary-does-not-exist")
	if err == nil {
		t.Fatal("want an error invoking a nonexistent binary, got nil")
	}
}

// TestDeltaReport_ActionConstants pins the four action strings the
// contract names verbatim (§5.9's "reloaded" / "not installed", plus
// "installed" / "removed" for the non-convergent cases) so a rename is
// caught here, not by a downstream CLI-output snapshot.
func TestDeltaReport_ActionConstants(t *testing.T) {
	want := map[string]string{
		"ActionInstalled":    "installed",
		"ActionReloaded":     "reloaded",
		"ActionRemoved":      "removed",
		"ActionNotInstalled": "not installed",
	}
	got := map[string]string{
		"ActionInstalled":    ActionInstalled,
		"ActionReloaded":     ActionReloaded,
		"ActionRemoved":      ActionRemoved,
		"ActionNotInstalled": ActionNotInstalled,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
}
