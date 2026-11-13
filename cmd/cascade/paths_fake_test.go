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
//   DataDir seeds a controller designation once per data dir, after the
//   data dir exists, so a daemon fixture over a fresh home resolves the
//   controller role without each test seeding it and a command that must
//   not create the data dir still does not; role tests remove it with
//   removeTestDesignation.
// SPORT: cmd/cascade test-support (ADD).

import (
	"path/filepath"

	"github.com/acamarata/cascade/internal/runtime"
)

type fakeDaemonPaths struct{ root string }

func (p fakeDaemonPaths) Root() string       { return p.root }
func (p fakeDaemonPaths) ConfigPath() string { return filepath.Join(p.root, "config.toml") }
func (p fakeDaemonPaths) SocketPath() string { return filepath.Join(p.root, "daemon.sock") }
func (p fakeDaemonPaths) DataDir() string {
	dir := filepath.Join(p.root, "data")
	if p.root != "" {
		seedFixtureDesignation(dir)
	}
	return dir
}
func (p fakeDaemonPaths) LogDir() string { return filepath.Join(p.root, "logs") }
func (p fakeDaemonPaths) StorageRoot(prof runtime.Profile) string {
	return filepath.Join(p.root, "data", "storage", string(prof))
}
