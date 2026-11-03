//go:build linux

// Purpose: the linux HarnessCredentials: the harness entry is
// <configDir>/.credentials.json, mode 0600, owned by the current uid,
// replaced atomically. SPORT: HarnessCredentials/ADDED.

package secrets

import (
	"os"
	"syscall"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// NewHarnessCredentials returns the linux file HarnessCredentials, staging
// writes and keeping rollback slots in custody.
func NewHarnessCredentials(custody Custody, clock runtime.Clock) (HarnessCredentials, error) {
	if custody == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: harness credentials need a custody and a clock")
	}
	store := fileStore{uid: os.Getuid(), owner: harnessFileOwner, writeFn: (*os.File).Write, rename: os.Rename}
	return &harnessCredentials{store: store, custody: custody, clock: clock}, nil
}

// harnessFileOwner reports a file's owning uid.
func harnessFileOwner(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
