//go:build !windows

// Package pbd (lifecycle_lock_unix.go): unix (linux, darwin, and other
// non-windows) implementation of AppendIf's exclusive lock —
// golang.org/x/sys/unix.Flock(LOCK_EX), BLOCKING (no LOCK_NB): contenders
// queue for the lock rather than fail, which is what lets sixteen
// goroutines (or two processes) race AppendIf and have the sequence check
// alone decide the winner. This deliberately differs from
// providers/sqlite/flock_linux.go's non-blocking §D-3 arbitration lock —
// a different, non-importable package (plugins/** imports pkg/** and
// plugins/pbd/internal/pews only, Art.10.2) solving a different problem
// (detecting "already open elsewhere", not serializing a compare-and-append).
// Constraints: build-tagged !windows per write_scope.
// SPORT: plugins/pbd lifecycle_appendif (ADD) — P1-PBD-07.
package pbd

import (
	"os"

	"golang.org/x/sys/unix"

	"github.com/acamarata/cascade/pkg/cascade"
)

// acquireJournalLock opens (creating if absent) path and blocks until it
// holds an exclusive flock on it, returning an unlock func that releases
// the lock and closes the file descriptor. Each call opens its own file
// descriptor, so this blocks a second same-process caller exactly as it
// blocks a second process: flock locks are per open-file-description, not
// per-process.
func acquireJournalLock(path string) (unlock func() error, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInternal, err, "pbd: opening lock file %q", path)
	}
	if ferr := unix.Flock(int(f.Fd()), unix.LOCK_EX); ferr != nil {
		_ = f.Close()
		return nil, cascade.Wrapf(cascade.KindInternal, ferr, "pbd: locking %q", path)
	}
	return func() error {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		return f.Close()
	}, nil
}
