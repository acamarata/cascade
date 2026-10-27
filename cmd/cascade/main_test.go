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
//	It also wraps selectProcessCustody, the one selector every host vault
//	selection in cmd/cascade goes through (internal/build refuses any other
//	non-test secrets.SelectCustody reference here), before any test runs.
//	The wrapper counts each selection that arrives without the file vault
//	forced (one that would have probed host custody), attaches a spy runner
//	that records any platform helper exec, and only then forces the file
//	vault. A guarded selection that returns any backend but the file vault
//	is recorded too, so a host backend reached without an exec (linux's
//	D-Bus secret service) still counts. After m.Run the count must be zero
//	or the run fails (TestNoHostKeychainInvocation is the in-run half).
//	Custody selected inside other packages from a secrets.Config built here
//	(the bridge vault, node keystores) is outside this spy.
//
// SPORT: cmd/cascade test-support (ADD).
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/secrets"
)

// sweepStaleCmdHomes removes cascade-cmd-home* dirs left behind by previous
// runs of this test binary that never reached the os.RemoveAll below (killed
// by a test timeout, an interrupted CI runner, or a panic that unwound past
// TestMain). This can't happen on every exit path — Go doesn't run this code
// on SIGKILL — so it self-heals on the NEXT run instead: before claiming a
// fresh throwaway HOME, clear out anything a prior run abandoned. Without
// this, each interrupted run permanently leaks its GOMODCACHE-sized HOME.
func sweepStaleCmdHomes() {
	sweepStaleHomes(os.TempDir(), "cascade-cmd-home", time.Now(), staleHomeAge)
}

// staleHomeAge is how long a throwaway HOME must sit untouched before a
// later run treats it as abandoned. A live run's HOME is written to as it
// goes, and other runs (several lanes, a ship) share the temp dir, so a
// younger directory is someone else's and is left alone.
const staleHomeAge = 3 * time.Hour

// sweepStaleHomes removes the directories in dir that start with prefix and
// were last modified more than maxAge before now.
func sweepStaleHomes(dir, prefix string, now time.Time, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

// TestSweepStaleHomesSparesFreshDirs: only the old directory goes; a fresh
// one and an unrelated old one stay.
func TestSweepStaleHomesSparesFreshDirs(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	old, fresh, other := filepath.Join(dir, "cascade-cmd-home1"), filepath.Join(dir, "cascade-cmd-home2"), filepath.Join(dir, "unrelated")
	for _, d := range []string{old, fresh, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := now.Add(-4 * time.Hour)
	for _, d := range []string{old, other} {
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
	}
	sweepStaleHomes(dir, "cascade-cmd-home", now, staleHomeAge)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("the old dir survived: %v", err)
	}
	for _, d := range []string{fresh, other} {
		if _, err := os.Stat(d); err != nil {
			t.Fatalf("%s was removed: %v", filepath.Base(d), err)
		}
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

	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, home); err != nil {
			panic("cmd/cascade tests: could not redirect " + key + ": " + err.Error())
		}
	}
	selectProcessCustody = guardHostCustody(selectProcessCustody)
	doctorApprovalKeyCustody = guardReadOnlyCustody(doctorApprovalKeyCustody, &callLog{})
	doctorSelectCustody = func(dir string) elevation.Custody { return elevation.Selector{DataDir: dir}.Select() }
	for key, val := range goCacheEnv {
		if val == "" {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			panic("cmd/cascade tests: could not pin " + key + ": " + err.Error())
		}
	}

	code := m.Run()
	if n, seen := hostCustodyAttempts(); n > 0 {
		fmt.Fprintf(os.Stderr, "cmd/cascade tests: %d attempt(s) to reach host custody: %v\n", n, seen)
		code = 1
	}
	_ = os.RemoveAll(home)
	os.Exit(code)
}

// hostCustodySpy records every platform helper exec this test binary
// attempts through a guarded selection: each one is a reach for the host
// keychain or secret service.
var hostCustodySpy struct {
	mu   sync.Mutex
	seen []string
}

// interceptedHostSelections counts guarded selections that arrived without
// the file vault forced: production configs the guard redirected. It proves
// the guard sits on the production path; it is not a failure.
var interceptedHostSelections atomic.Int64

// recordHostCustody notes one attempt.
func recordHostCustody(what string) {
	hostCustodySpy.mu.Lock()
	defer hostCustodySpy.mu.Unlock()
	hostCustodySpy.seen = append(hostCustodySpy.seen, what)
}

// hostCustodyAttempts reports the attempts so far, including the daemon
// custody hook's runner calls.
func hostCustodyAttempts() (int, []string) {
	hostCustodySpy.mu.Lock()
	defer hostCustodySpy.mu.Unlock()
	seen := append([]string(nil), hostCustodySpy.seen...)
	if n := testCustodyRunnerCalls.Load(); n > 0 {
		seen = append(seen, fmt.Sprintf("daemon custody hook runner x%d", n))
	}
	return len(seen), seen
}

// spyCustodyRunner is the platform-helper runner every guarded selection
// carries (darwin runs /usr/bin/security through it). It never runs
// anything: it records the attempt and fails it.
func spyCustodyRunner(_ context.Context, name string, _ ...string) ([]byte, error) {
	recordHostCustody("exec " + filepath.Base(name))
	return nil, errors.New("test: no platform keychain helper in cmd/cascade tests")
}

// guardHostCustody wraps next, the selector TestMain found: it counts a
// selection that does not force the file vault BEFORE forcing it, attaches
// the spy runner, then forces the file vault in the caller's directory.
// Any custody other than the file vault coming back is recorded as an
// attempt: that catches a host backend the exec spy cannot see (linux's
// secret service connects over D-Bus, not through the runner).
func guardHostCustody(next func(secrets.Config) (secrets.Custody, error)) func(secrets.Config) (secrets.Custody, error) {
	return func(cfg secrets.Config) (secrets.Custody, error) {
		if !cfg.ForceFileVault {
			interceptedHostSelections.Add(1)
		}
		cfg.Runner = spyCustodyRunner
		cfg.ForceFileVault = true
		custody, err := next(cfg)
		if err == nil && custody != nil && custody.Name() != guardedCustodyName {
			recordHostCustody("selected " + custody.Name())
		}
		return custody, err
	}
}

// guardedCustodyName is the only backend a guarded selection may return.
const guardedCustodyName = "file-vault"

// testProcessCustody is a guarded selector over a fixed directory, for a
// test that wants its own vault.
func testProcessCustody(dir string) func(secrets.Config) (secrets.Custody, error) {
	guarded := guardHostCustody(secrets.SelectCustody)
	return func(cfg secrets.Config) (secrets.Custody, error) {
		cfg.Dir = dir
		return guarded(cfg)
	}
}
