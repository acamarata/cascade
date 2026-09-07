package main

// Purpose: fakeDaemonPaths, the minimal runtime.PathProvider the cmd tests
//   inject instead of resolving the operator's real directories.
// Constraints: this file carries NO build tag on purpose. The type used to
//   live in daemon_unix_test.go (//go:build !windows), but the policy and
//   approval commands it is now also used to test are themselves untagged
//   and therefore exist on windows, where the test failed to compile with
//   `undefined: fakeDaemonPaths`. Tagging those tests !windows would have
//   fixed the build by deleting windows coverage of commands that ship
//   there; moving the shared helper keeps both.
// SPORT: cmd/cascade test-support (ADD).

import (
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
)

type fakeDaemonPaths struct{ root string }

func (p fakeDaemonPaths) Root() string       { return p.root }
func (p fakeDaemonPaths) ConfigPath() string { return filepath.Join(p.root, "config.toml") }
func (p fakeDaemonPaths) SocketPath() string { return filepath.Join(p.root, "daemon.sock") }
func (p fakeDaemonPaths) DataDir() string    { return filepath.Join(p.root, "data") }
func (p fakeDaemonPaths) LogDir() string     { return filepath.Join(p.root, "logs") }
func (p fakeDaemonPaths) StorageRoot(prof runtime.Profile) string {
	return filepath.Join(p.root, "data", "storage", string(prof))
}
