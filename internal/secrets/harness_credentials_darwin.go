//go:build darwin

// Purpose: the darwin HarnessCredentials: the harness entry is a generic
// password in the user's default keychain, reached through /usr/bin/security
// (the shared commandRunner; no CGO). SPORT: HarnessCredentials/ADDED.

package secrets

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// NewHarnessCredentials returns the darwin keychain HarnessCredentials,
// staging writes and keeping rollback slots in custody.
func NewHarnessCredentials(custody Custody, clock runtime.Clock) (HarnessCredentials, error) {
	if custody == nil || clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "secrets: harness credentials need a custody and a clock")
	}
	store := &keychainStore{run: execRunner, realpath: filepath.EvalSymlinks, basis: map[string][2]string{}}
	store.ident = func(ctx context.Context) (string, string, error) { return harnessIdent(ctx, store.run) }
	return &harnessCredentials{store: store, custody: custody, clock: clock}, nil
}

// harnessIdent resolves the OS user (the entry's account) and the explicit
// default keychain path. With no existing default keychain nothing is
// written, so the security tool can never raise a dialog (R-14.260).
func harnessIdent(ctx context.Context, run commandRunner) (string, string, error) {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "", "", cascade.New(cascade.KindUnavailable, "secrets: the OS user could not be resolved")
	}
	out, err := run(ctx, "/usr/bin/security", "default-keychain", "-d", "user")
	path := strings.Trim(strings.TrimSpace(string(out)), `"`)
	if err != nil || path == "" {
		return "", "", cascade.New(cascade.KindUnavailable, "secrets: no default user keychain resolved")
	}
	if _, err := os.Stat(path); err != nil {
		return "", "", cascade.New(cascade.KindUnavailable, "secrets: the default user keychain does not exist")
	}
	return u.Username, path, nil
}

// harnessFileOwner reports a file's owning uid.
func harnessFileOwner(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
