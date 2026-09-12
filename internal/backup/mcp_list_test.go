// Purpose: real-target tests for ListSnapshots/listTargetSnapshots (mcp.go)
// -- the shared read path `backup list` and cascade_backup_list both call.
// mcp_test.go's own tests inject a fake SnapshotListFunc and never drive
// this file's real Store/Target logic directly.
// Inputs: a real fs-backed target with a real signed snapshot, a real
// in-memory outcome Store.
// Outputs: the decrypted, outcome-annotated SnapshotSummary rows.
// SPORT: internal.backup.mcp/ADD (tests) (P1-E19-W4-S42-T3).

package backup

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
)

const mcpListTestNamespace = "mcp-list-test"

// TestListSnapshotsRealTarget proves ListSnapshots decrypts a real manifest
// off a real target and reports "unknown" when no Outcome has been
// recorded for it yet.
func TestListSnapshotsRealTarget(t *testing.T) {
	target, _, m, _, _ := restoreFixture(t)
	store := storetest.NewMemStore()

	rows, err := ListSnapshots(context.Background(), store, mcpListTestNamespace,
		[]NamedTarget{{Name: "primary", Target: target}})
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListSnapshots = %d rows, want 1", len(rows))
	}
	if rows[0].ID != m.Snapshot || rows[0].Target != "primary" || rows[0].Outcome != "unknown" {
		t.Fatalf("row = %+v, want snapshot %q, target primary, outcome unknown", rows[0], m.Snapshot)
	}
	if rows[0].ObjectCount == 0 {
		t.Error("row.ObjectCount = 0, want the real manifest's object count")
	}
}

// TestListSnapshotsWithRecordedOutcome proves a recorded Outcome is joined
// onto its matching snapshot row -- success and failure both render, not
// just the zero value.
func TestListSnapshotsWithRecordedOutcome(t *testing.T) {
	target, _, m, _, _ := restoreFixture(t)
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_400, 0))
	if err := RecordOutcome(context.Background(), store, mcpListTestNamespace, Outcome{
		Target: "primary", Snapshot: string(m.Snapshot), When: clock.Now(), Success: true,
	}); err != nil {
		t.Fatalf("RecordOutcome: %v", err)
	}

	rows, err := ListSnapshots(context.Background(), store, mcpListTestNamespace,
		[]NamedTarget{{Name: "primary", Target: target}})
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(rows) != 1 || rows[0].Outcome != "success" {
		t.Fatalf("rows = %+v, want one row with outcome success", rows)
	}
}

// TestListSnapshotsEmptyTargets proves zero targets is success with zero
// rows, never an error or a nil-vs-empty-slice ambiguity.
func TestListSnapshotsEmptyTargets(t *testing.T) {
	rows, err := ListSnapshots(context.Background(), storetest.NewMemStore(), mcpListTestNamespace, nil)
	if err != nil {
		t.Fatalf("ListSnapshots(no targets): %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListSnapshots(no targets) = %d rows, want 0", len(rows))
	}
}

// TestListSnapshotsTargetListFailure proves a target whose manifest
// listing fails surfaces that failure, never silently reporting zero rows
// for a target that is actually broken.
func TestListSnapshotsTargetListFailure(t *testing.T) {
	setSigningKeyEnv(t)
	identity, _ := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	_, err := ListSnapshots(context.Background(), storetest.NewMemStore(), mcpListTestNamespace,
		[]NamedTarget{{Name: "broken", Target: failingListTarget{}}})
	if err == nil {
		t.Fatal("ListSnapshots with a failing target.List succeeded, want the failure surfaced")
	}
}

// failingListTarget is a minimal Target whose List always errors, used
// only to prove ListSnapshots propagates that failure rather than masking
// it. Put/Get/Delete are never called on this path.
type failingListTarget struct{}

func (failingListTarget) Put(context.Context, string, io.Reader) error { return nil }
func (failingListTarget) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, errFailingListTarget
}
func (failingListTarget) List(context.Context, string) ([]string, error) {
	return nil, errFailingListTarget
}
func (failingListTarget) Delete(context.Context, string) error { return nil }

var errFailingListTarget = &testTargetError{"injected list failure"}

type testTargetError struct{ msg string }

func (e *testTargetError) Error() string { return e.msg }
