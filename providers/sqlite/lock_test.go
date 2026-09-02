// Purpose: §D-3 arbitration tests — exclusive flock double-open (Art.5
//
//	darwin/linux) and the daemon-owns-store / socket-probe refusal paths.
//	Split from driver_test.go under R-14.117.
//
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2).
package sqlite_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestOpen_ExclusiveLockRefusesSecondOpener proves the §D-3 "never two
// writers" invariant end to end through the public Open API: a second
// Open of the same path while the first is still held is refused, and on
// darwin/linux the refusal traces back to EWOULDBLOCK (Art.5). On windows
// (tier-2 scope, not run on this darwin host — verified by build+vet only
// per the ticket's environment note) the FIRST Open already refuses, since
// flock_windows.go always returns the compile-tagged refusal.
func TestOpen_ExclusiveLockRefusesSecondOpener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	ctx := context.Background()

	first, err := sqlite.Open(ctx, path)
	if runtime.GOOS == "windows" {
		if err == nil {
			t.Fatal("Open: want windows tier-2 refusal, got nil error")
		}
		if !cascade.HasKind(err, cascade.KindUnsupported) {
			t.Fatalf("Open on windows: want KindUnsupported, got %v", err)
		}
		if !strings.Contains(err.Error(), "windows") {
			t.Fatalf("Open on windows: refusal message %q does not mention windows", err.Error())
		}
		return
	}
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer func() {
		if err := first.Close(); err != nil {
			t.Errorf("first.Close: %v", err)
		}
	}()

	_, err = sqlite.Open(ctx, path)
	if err == nil {
		t.Fatal("second Open: want a §D-3 refusal, got nil error")
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second Open: want KindConflict, got %v", err)
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("second Open: want errors.Is(err, syscall.EWOULDBLOCK), got %v", err)
	}
}

// TestOpen_SocketProbeShortCircuitsBeforeFlock proves "socket-probe-first":
// when the injected SocketProbe reports a live daemon, Open refuses with
// ErrDaemonOwnsStore WITHOUT ever attempting the flock — proven by opening
// successfully afterward on the same path with no probe, which would fail
// if the first Open call had actually taken (and leaked) the flock.
func TestOpen_SocketProbeShortCircuitsBeforeFlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	ctx := context.Background()

	probe := func(string) (bool, error) { return true, nil }
	_, err := sqlite.Open(ctx, path, sqlite.WithSocketProbe(probe))
	if !errors.Is(err, sqlite.ErrDaemonOwnsStore) {
		t.Fatalf("Open with live-daemon probe: want errors.Is(err, ErrDaemonOwnsStore), got %v", err)
	}

	// No flock was taken, so a normal Open (no probe) must now succeed.
	d, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open after refused probe: want success (flock never taken), got %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestOpen_SocketProbeError proves a probe failure (distinct from "daemon
// is running") surfaces as KindUnavailable, not KindConflict — the store's
// liveness is genuinely unknown, which is a different situation from a
// confirmed live owner.
func TestOpen_SocketProbeError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	probeErr := errors.New("probe socket unreachable")
	probe := func(string) (bool, error) { return false, probeErr }
	_, err := sqlite.Open(context.Background(), path, sqlite.WithSocketProbe(probe))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("Open with failing probe: want KindUnavailable, got %v", err)
	}
	if !errors.Is(err, probeErr) {
		t.Fatalf("Open with failing probe: want errors.Is(err, probeErr), got %v", err)
	}
}

// TestOpen_ExclusiveLockRefusesSecondOpener_RelativeVsAbsolute proves the
// CR fix 1 canonicalization: opening the SAME database file first via its
// absolute path and then via a relative spelling of the identical path
// (after chdir into its directory) must derive the same sidecar lock
// path and refuse the second Open — not silently succeed with two
// unrelated ".lock" files. Without canonicalDBPath, "cascade.db" and its
// absolute equivalent produce two different lock filenames and both
// Opens would succeed, defeating the "never two writers" invariant.
func TestOpen_ExclusiveLockRefusesSecondOpener_RelativeVsAbsolute(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock_windows.go refuses on the FIRST Open (tier-2 scope); canonicalization is unreachable")
	}
	dir := t.TempDir()
	absPath := filepath.Join(dir, "cascade.db")
	ctx := context.Background()

	first, err := sqlite.Open(ctx, absPath)
	if err != nil {
		t.Fatalf("first Open (absolute path): %v", err)
	}
	defer func() {
		if err := first.Close(); err != nil {
			t.Errorf("first.Close: %v", err)
		}
	}()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("os.Chdir(%s): %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Errorf("os.Chdir(back to %s): %v", oldWD, err)
		}
	}()

	_, err = sqlite.Open(ctx, "cascade.db")
	if err == nil {
		t.Fatal("second Open via relative spelling of the same database: want a §D-3 refusal, got nil error")
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second Open via relative spelling: want KindConflict, got %v", err)
	}
}

// TestOpen_ExclusiveLockRefusesSecondOpener_SymlinkVsTarget proves the
// same canonicalization for a symlink pointing at the database file's
// real path: opening the target directly, then opening it again through a
// symlink from a different directory, must derive the same sidecar lock
// path and refuse the second Open.
func TestOpen_ExclusiveLockRefusesSecondOpener_SymlinkVsTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock_windows.go refuses on the FIRST Open (tier-2 scope); canonicalization is unreachable")
	}
	targetDir := t.TempDir()
	linkDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "cascade.db")
	linkPath := filepath.Join(linkDir, "cascade.db")

	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Fatalf("os.Symlink(%s -> %s): %v", linkPath, targetPath, err)
	}

	ctx := context.Background()
	first, err := sqlite.Open(ctx, targetPath)
	if err != nil {
		t.Fatalf("first Open (target path): %v", err)
	}
	defer func() {
		if err := first.Close(); err != nil {
			t.Errorf("first.Close: %v", err)
		}
	}()

	_, err = sqlite.Open(ctx, linkPath)
	if err == nil {
		t.Fatal("second Open via symlink to the same database: want a §D-3 refusal, got nil error")
	}
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("second Open via symlink: want KindConflict, got %v", err)
	}
}
