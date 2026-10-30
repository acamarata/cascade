//go:build !windows

// Purpose: the clipboard re-arm registration and the CLI's audit sinks:
//
//	the re-arm runs through the daemon's one audit log (no audit.New) over
//	the pending row a killed CLI left, and `vault get --clipboard` hands its
//	writer the process audit log and that log's effect log, opened before
//	anything is read. The re-arm entry point is substituted, so no test
//	reaches the real clipboard.
//
// SPORT: cmd/cascade daemon registrations — clipboard re-arm (TEST, P1-SEC-09).
package main

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

type rearmCall struct {
	effects audit.EffectWriter
	rows    []secrets.PendingClear
}

// recordRearm substitutes the re-arm entry point for one test.
func recordRearm(t *testing.T) *[]rearmCall {
	t.Helper()
	calls := &[]rearmCall{}
	prev := rearmPendingClears
	rearmPendingClears = func(ctx context.Context, _ runtime.Clock, fx audit.EffectWriter, store secrets.PendingClearStore, _ secrets.ClipboardScheduler) error {
		rows, err := store.List(ctx)
		*calls = append(*calls, rearmCall{fx, rows})
		return err
	}
	t.Cleanup(func() { rearmPendingClears = prev })
	return calls
}

func clipboardRegistration(t *testing.T) daemonRegistration {
	t.Helper()
	for _, r := range daemonRegistrations {
		if r.Name == "clipboard-rearm" {
			return r
		}
	}
	t.Fatal("no clipboard-rearm daemon registration")
	return daemonRegistration{}
}

// TestClipboardClearRearmedAfterRestart: the row a CLI killed mid-TTL left
// reaches the daemon's re-arm at start, from the profile's pending file.
func TestClipboardClearRearmedAfterRestart(t *testing.T) {
	resetProcessFirewall(t)
	selectProcessCustody = testProcessCustody(t.TempDir())
	paths := firewallTestPaths(t)
	path, err := pendingClearsPath(paths)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ref, err := cascade.NewID()
	if err != nil {
		t.Fatal(err)
	}
	left := secrets.PendingClear{RefID: ref.String(), Platform: "darwin", WrittenAt: now, ClearDueAt: now.Add(30 * time.Second)}
	if err := secrets.NewFilePendingClearStore(path).Put(context.Background(), left); err != nil {
		t.Fatal(err)
	}
	calls := recordRearm(t)
	reg := clipboardRegistration(t)
	w := &daemonWiring{Ctx: context.Background(), Store: storetest.NewMemStore(), Clock: runtime.NewSystemClock(), Paths: paths}
	if err := reg.Wire(w); err != nil {
		t.Fatalf("clipboard-rearm Wire: %v", err)
	}
	if reg.Phase != phaseLate || len(*calls) != 1 || len((*calls)[0].rows) != 1 || (*calls)[0].rows[0].RefID != ref.String() {
		t.Fatalf("phase = %v, re-arm calls = %+v; want one late re-arm over the killed CLI's row", reg.Phase, *calls)
	}
}

// TestClipboardRearmUsesDaemonAuditLog: the re-arm's effect writer is the
// memoised effect log over the daemon's shared audit log for the store.
func TestClipboardRearmUsesDaemonAuditLog(t *testing.T) {
	resetProcessFirewall(t)
	selectProcessCustody = testProcessCustody(t.TempDir())
	paths := firewallTestPaths(t)
	store, clock := storetest.NewMemStore(), runtime.NewSystemClock()
	calls := recordRearm(t)
	if err := clipboardRegistration(t).Wire(&daemonWiring{Ctx: context.Background(), Store: store, Clock: clock, Paths: paths}); err != nil {
		t.Fatalf("Wire: %v", err)
	}
	daemonLog, err := openDaemonAuditLog(store, clock, nil, paths)
	if err != nil {
		t.Fatal(err)
	}
	want, err := openEffectLog(store, clock, nil, paths)
	if err != nil || len(*calls) != 1 || (*calls)[0].effects != audit.EffectWriter(want) || sharedAuditLog(store) != daemonLog {
		t.Fatalf("re-arm effects = %v, want the daemon log's %p (%v)", *calls, want, err)
	}
}

// TestClipboardWriteAudited: the writer is handed one clipboard audit sink
// (daemon log through audit.clipboard_effect, else the process log per
// record), never a second log and never a held store.
func TestClipboardWriteAudited(t *testing.T) {
	resetProcessFirewall(t)
	selectProcessCustody = testProcessCustody(t.TempDir())
	paths := firewallTestPaths(t)
	w := &fakeClipWriter{}
	deps, calls := clipDeps(t, okGate{}, w)
	host := hostClipboard(paths)
	host.ttl = func(context.Context) (time.Duration, error) { return 30 * time.Second, nil }
	if _, _, err := runClip(context.Background(), t, deps, host); err != nil {
		t.Fatalf("vault get --clipboard: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("writers built = %d", len(*calls))
	}
	aw, ok := (*calls)[0].aw.(*clipboardAuditSink)
	fx, fok := (*calls)[0].fx.(*clipboardAuditSink)
	if !ok || !fok || aw != fx || aw.paths != paths {
		t.Fatalf("writer sinks = (%T, %T); want one clipboard audit sink over the profile paths", (*calls)[0].aw, (*calls)[0].fx)
	}
}
