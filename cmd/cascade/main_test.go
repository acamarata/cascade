// Purpose: TestMain for the cmd/cascade test binary, which points HOME at
//
//	a throwaway directory for every test in the package.
//
// Constraints: this is not a convenience. Several tests here (hooks_test.go's
//
//	TestBuildRPCServer_RegistersCompletionGatePack,
//	daemon_unix_scheduler_dag_test.go, daemon_unix_scheduler_resume_test.go)
//	drive the REAL daemon composition root, buildRPCServer, over an
//	otherwise fully injected/temp-dir *runtime.PathProvider. buildRPCServer
//	unconditionally calls transport.RegisterSocketMCP, which builds the
//	real response marshaler (internal/mcp/firewall.go's
//	NewDefaultResponseMarshaler) — that opens THIS PROCESS's vault at the
//	data directory resolved from $HOME, with no way for a caller to inject
//	a different one (internal/mcp/transport's own main_test.go documents
//	the identical mechanism for its package). On a host with an OS
//	keychain — the darwin dev machine — custody takes the keychain branch
//	and creates nothing, so this looked clean locally. On a linux host
//	(every CI runner) there is no keychain, custody falls back to the
//	encrypted file vault, and that CREATES $HOME/.cascade/data/vault.key —
//	an Art.7.1 violation the redirected-HOME CI job catches (it failed with
//	"the suite left 1 entr(y/ies) under the redirected HOME: [.cascade]"),
//	while being invisible to every local run on a darwin machine.
//	TestMain rather than a per-test t.Setenv because the leak belongs to
//	the composition root itself, so every test in this package that builds
//	it has it, including ones not yet written — the exact rationale
//	internal/mcp/transport/main_test.go already applies to its own suite.
//
// SPORT: cmd/cascade test-support (ADD).
package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "cascade-cmd-home")
	if err != nil {
		panic("cmd/cascade tests: could not create a throwaway HOME: " + err.Error())
	}
	if err := os.Setenv("HOME", home); err != nil {
		panic("cmd/cascade tests: could not redirect HOME: " + err.Error())
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
