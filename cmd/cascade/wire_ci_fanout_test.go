//go:build !windows

package main

import (
	"database/sql"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/daemon"
)

// countCIFanouts wraps registerCIFanout so each call registers on the
// real dispatcher and records it; restored when the test ends.
func countCIFanouts(t *testing.T) func() []*ci.Dispatcher {
	t.Helper()
	orig := registerCIFanout
	var mu sync.Mutex
	var seen []*ci.Dispatcher
	registerCIFanout = func(d *ci.Dispatcher, db *sql.DB, origins ci.DelegationOrigin, n ci.Notifier, fallback ci.AttentionPusher) {
		mu.Lock()
		seen = append(seen, d)
		mu.Unlock()
		orig(d, db, origins, n, fallback)
	}
	t.Cleanup(func() { registerCIFanout = orig })
	return func() []*ci.Dispatcher {
		mu.Lock()
		defer mu.Unlock()
		return append([]*ci.Dispatcher(nil), seen...)
	}
}

// TestCIFanoutWiredInDaemon: the production composition registers the CI
// fanout, and with it exactly one OnTerminal callback, on the daemon's one
// dispatcher, and reports the ci-fanout subsystem as started.
func TestCIFanoutWiredInDaemon(t *testing.T) {
	calls := countCIFanouts(t)
	m, _ := composeNotify(t)
	got := calls()
	if len(got) != 1 {
		t.Fatalf("composeDaemon registered the CI fanout %d times, want exactly 1", len(got))
	}
	if got[0] == nil {
		t.Fatal("the CI fanout was registered on a nil dispatcher")
	}
	st, ok := subsystemState(m, ciFanoutSubsystem)
	if !ok {
		t.Fatalf("subsystem %q not on the manifest", ciFanoutSubsystem)
	}
	if st.State != daemon.SubsystemRunning {
		t.Fatalf("subsystem %q state %v (%s), want running", ciFanoutSubsystem, st.State, st.Detail)
	}
}

// TestCIFanoutOriginNoRecord: a job with no delegation record has no origin.
func TestCIFanoutOriginNoRecord(t *testing.T) {
	var o jobDelegationOrigin
	if _, _, found, err := o.OriginForJob(t.Context(), ""); err != nil || found {
		t.Fatalf("OriginForJob(\"\") = found %v, err %v; want not found", found, err)
	}
}
