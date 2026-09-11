// Purpose: ErrLockHeld is the platform-independent sentinel every
//
//	acquireExclusiveLock implementation (flock_darwin.go, flock_linux.go,
//	flock_windows.go) wraps into its KindConflict error when the §D-3
//	exclusive lock is already held by another process. It gives callers a
//	single, portable check — errors.Is(err, sqlite.ErrLockHeld) — that
//	works identically on every platform.
//
// Constraints: this file only ADDS a portable check on top of each
//
//	platform's existing errno, it never replaces or loosens it: darwin and
//	linux still additionally satisfy errors.Is(err, syscall.EWOULDBLOCK) /
//	errors.Is(err, unix.EWOULDBLOCK) exactly as before (see the !windows
//	assertion in lock_unix_errno_test.go), because each unix
//	acquireExclusiveLock wraps errors.Join(errno, ErrLockHeld) rather than
//	substituting ErrLockHeld for errno.
//
// SPORT: providers.sqlite.Driver/CHANGED (windows LockFileEx ticket).

package sqlite

import "errors"

// ErrLockHeld is wrapped into the KindConflict error every
// acquireExclusiveLock implementation returns when the §D-3 exclusive lock
// is already held by another process. Test with
// errors.Is(err, sqlite.ErrLockHeld) for a check that is identical on
// darwin, linux, and windows.
var ErrLockHeld = errors.New("sqlite: exclusive lock is held by another process")
