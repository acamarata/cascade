// Purpose: the cmd/cascade test binary's custody hook. init() sets
//
//	daemonCustodyHook so every custody selection the daemon composition root
//	makes (the chat scrub pipeline and the bridge vault) is forced onto the
//	encrypted file vault with a Runner that fails, so no test in this binary
//	(including the composeDaemon/platformDaemonRun tests that predate the
//	hook) can reach the operator's login keychain through either site.
//
// Constraints: test-only. TestDaemonLifecycleLint fails if any non-test file
//
//	assigns daemonCustodyHook, so production always runs with it nil. The
//	recorder is reset by the test that reads it; other tests only feed it.
//
// SPORT: cmd/cascade test-support (ADD, daemon custody hook).
package main

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func init() {
	daemonCustodyHook = testDaemonCustodyHook
}

// testCustodyRunnerCalls counts calls to the hook's Runner. With the file
// vault forced it must stay at zero: a call means something tried to run the
// platform keychain helper.
var testCustodyRunnerCalls atomic.Int64

// testCustodyCall is one custody selection the hook rewrote.
type testCustodyCall struct {
	site string
	cfg  secrets.Config
}

// testCustodyLog records every selection the hook rewrote, in order.
var testCustodyLog struct {
	mu    sync.Mutex
	calls []testCustodyCall
}

// testDaemonCustodyHook keeps cfg's service label and directory and forces
// the file vault with a failing Runner, recording the rewritten config.
func testDaemonCustodyHook(site string, cfg secrets.Config) secrets.Config {
	out := secrets.Config{
		Service:        cfg.Service,
		Dir:            cfg.Dir,
		ForceFileVault: true,
		Runner:         testFailingCustodyRunner,
	}
	testCustodyLog.mu.Lock()
	defer testCustodyLog.mu.Unlock()
	testCustodyLog.calls = append(testCustodyLog.calls, testCustodyCall{site: site, cfg: out})
	return out
}

// testFailingCustodyRunner is the platform-helper runner the hook installs.
// It never runs anything; it counts the attempt and fails it.
func testFailingCustodyRunner(context.Context, string, ...string) ([]byte, error) {
	testCustodyRunnerCalls.Add(1)
	return nil, cascade.New(cascade.KindUnavailable, "test: no platform keychain helper in cmd/cascade tests")
}

// resetTestCustodyLog clears the recorder and the runner counter.
func resetTestCustodyLog() {
	testCustodyLog.mu.Lock()
	defer testCustodyLog.mu.Unlock()
	testCustodyLog.calls = nil
	testCustodyRunnerCalls.Store(0)
}

// testCustodyCalls returns a copy of the recorded selections.
func testCustodyCalls() []testCustodyCall {
	testCustodyLog.mu.Lock()
	defer testCustodyLog.mu.Unlock()
	return append([]testCustodyCall(nil), testCustodyLog.calls...)
}
