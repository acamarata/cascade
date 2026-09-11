// Purpose: §D-3 arbitration tests — exclusive lock double-open (same
//
//	process) and the daemon-owns-store / socket-probe refusal paths. Split
//	from driver_test.go under R-14.117; the cross-process half of the
//	same-purpose test lives in lock_crossprocess_test.go, split out under
//	the same file's 300-line cap.
//
// Constraints: this file carries NO build tag and asserts the PORTABLE
//
//	errors.Is(err, sqlite.ErrLockHeld) check on every platform, now that
//	flock_windows.go implements a real LockFileEx lock instead of an
//	unconditional refusal. Darwin/linux's STRONGER, errno-specific
//	assertion (errors.Is(err, syscall.EWOULDBLOCK)) lives in the
//	build-tagged lock_unix_errno_test.go so it is never loosened by this
//	file going portable.
//
// SPORT: providers.sqlite.Driver/ADDED (P1-E02-W1-S02-T2), CHANGED
//
//	(windows LockFileEx ticket: ErrLockHeld, obsolete windows skips
//	removed, cross-process test split to lock_crossprocess_test.go).
package sqlite_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/providers/sqlite"
)

// TestOpen_ExclusiveLockRefusesSecondOpener proves the §D-3 "never two
// writers" invariant end to end through the public Open API, same-process.
// This is necessary but not sufficient evidence on its own — see
// TestOpen_ExclusiveLockRefusesSecondOpener_CrossProcess (in
// lock_crossprocess_test.go) for why a genuinely separate OS process is
// also required.
func TestOpen_ExclusiveLockRefusesSecondOpener(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cascade.db")
	ctx := context.Background()

	first, err := sqlite.Open(ctx, path)
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
	if !errors.Is(err, sqlite.ErrLockHeld) {
		t.Fatalf("second Open: want errors.Is(err, sqlite.ErrLockHeld), got %v", err)
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
// Opens would succeed, defeating the "never two writers" invariant. Now
// runs on windows too: flock_windows.go implements a real lock, so this
// is no longer unreachable there.
func TestOpen_ExclusiveLockRefusesSecondOpener_RelativeVsAbsolute(t *testing.T) {
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
// path and refuse the second Open. Now runs on windows too (symlinks
// require either Developer Mode or an elevated process there; the CI
// runner image has Developer Mode enabled).
func TestOpen_ExclusiveLockRefusesSecondOpener_SymlinkVsTarget(t *testing.T) {
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

// TestProbeExclusiveLock covers the health-check primitive, which measured
// 0% despite being what StorageHealthCheck relies on to report whether a
// database is already in use. An uncovered probe that always answered
// "free" would make the health check quietly useless. Now expects a real
// Held/free answer on every platform: flock_windows.go no longer reports
// Unsupported.
func TestProbeExclusiveLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.db")

	t.Run("free when nobody holds it", func(t *testing.T) {
		res, err := sqlite.ProbeExclusiveLock(path)
		if err != nil {
			t.Fatalf("ProbeExclusiveLock on a free path: %v", err)
		}
		if res.Held {
			t.Fatalf("Held = true on a path nobody has opened")
		}
	})

	t.Run("held while a real driver has it open", func(t *testing.T) {
		d, err := sqlite.Open(context.Background(), path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = d.Close() }()

		res, err := sqlite.ProbeExclusiveLock(path)
		if err != nil {
			t.Fatalf("ProbeExclusiveLock while open: %v", err)
		}
		if !res.Held {
			t.Fatal("Held = false while a driver holds the database open")
		}
	})

	t.Run("probe does not keep the lock", func(t *testing.T) {
		free := filepath.Join(t.TempDir(), "free.db")
		if _, err := sqlite.ProbeExclusiveLock(free); err != nil {
			t.Fatalf("first probe: %v", err)
		}
		// A real Open must still succeed afterwards: the probe releases.
		d, err := sqlite.Open(context.Background(), free)
		if err != nil {
			t.Fatalf("Open after probe (probe failed to release): %v", err)
		}
		_ = d.Close()
	})
}
