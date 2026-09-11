//go:build !windows

// Purpose: keeps the ORIGINAL, STRONGER unix assertion —
//
//	errors.Is(err, syscall.EWOULDBLOCK) — under test after lock_test.go's
//	TestOpen_ExclusiveLockRefusesSecondOpener switched to the portable
//	errors.Is(err, sqlite.ErrLockHeld) check shared with windows.
//
// Constraints: flock_darwin.go and flock_linux.go both wrap
//
//	errors.Join(errno, ErrLockHeld), never a substitution, so both checks
//	hold simultaneously on darwin/linux — this file exists only so the
//	STRONGER one stays under an explicit assertion of its own, matching
//	this ticket's design constraint that unix's guarantee must never be
//	loosened by the new portable check.
//
// SPORT: providers.sqlite.Driver/ADDED (windows LockFileEx ticket).
package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/acamarata/cascade/providers/sqlite"
)

// TestOpen_ExclusiveLockRefusesSecondOpener_UnixErrno proves darwin/linux's
// stronger guarantee still holds: the refusal traces all the way back to
// the real EWOULDBLOCK errno, not just the portable ErrLockHeld sentinel.
func TestOpen_ExclusiveLockRefusesSecondOpener_UnixErrno(t *testing.T) {
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
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("second Open: want errors.Is(err, syscall.EWOULDBLOCK), got %v", err)
	}
}
