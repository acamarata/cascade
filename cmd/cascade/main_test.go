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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sweepStaleCmdHomes removes cascade-cmd-home* dirs left behind by previous
// runs of this test binary that never reached the os.RemoveAll below (killed
// by a test timeout, an interrupted CI runner, or a panic that unwound past
// TestMain). This can't happen on every exit path — Go doesn't run this code
// on SIGKILL — so it self-heals on the NEXT run instead: before claiming a
// fresh throwaway HOME, clear out anything a prior run abandoned. Without
// this, each interrupted run permanently leaks its GOMODCACHE-sized HOME.
func sweepStaleCmdHomes() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "cascade-cmd-home") {
			continue
		}
		_ = os.RemoveAll(filepath.Join(os.TempDir(), e.Name()))
	}
}

func TestMain(m *testing.M) {
	sweepStaleCmdHomes()

	home, err := os.MkdirTemp("", "cascade-cmd-home")
	if err != nil {
		panic("cmd/cascade tests: could not create a throwaway HOME: " + err.Error())
	}

	// Pin GOPATH/GOMODCACHE to their real, persistent values *before*
	// redirecting HOME. Otherwise any `go` subprocess spawned during the
	// suite falls back to Go's HOME-relative default ($HOME/go) once HOME
	// points at this throwaway dir, and re-downloads the entire module
	// cache into it every single run — that's what turned a leaked empty
	// directory into a ~463MB leak per interrupted run.
	goCacheEnv := map[string]string{"GOPATH": "", "GOMODCACHE": ""}
	for key := range goCacheEnv {
		if v := os.Getenv(key); v != "" {
			goCacheEnv[key] = v
			continue
		}
		out, err := exec.Command("go", "env", key).Output()
		if err == nil {
			goCacheEnv[key] = strings.TrimSpace(string(out))
		}
	}

	if err := os.Setenv("HOME", home); err != nil {
		panic("cmd/cascade tests: could not redirect HOME: " + err.Error())
	}
	for key, val := range goCacheEnv {
		if val == "" {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			panic("cmd/cascade tests: could not pin " + key + ": " + err.Error())
		}
	}

	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
