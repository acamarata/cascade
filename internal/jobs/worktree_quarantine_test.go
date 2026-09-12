package jobs

// Purpose: the quarantine path's journal/attention integration (R-21.140):
//
//	the moved-not-deleted invariant, the structured journal metadata, and
//	exactly one raised attention item, all against REAL counterparts
//	(a real journal.SQLiteStore and a real supervision.Store, both over
//	storetest.NewMemStore -- lease_events_test.go's newTestSink pattern).
//
// SPORT: jobs/worktree-manager (ADD, P1-E29-W6-S59-T3).

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

// newTestQuarantineManager wires a WorktreeManager against real
// journal/attention counterparts, mirroring lease_events_test.go's
// newTestSink exactly.
func newTestQuarantineManager(t *testing.T) (*WorktreeManager, *Store, journal.Store, *supervision.Store) {
	t.Helper()
	store := newTestStore(t)
	kv := storetest.NewMemStore()
	clock := runtime.NewFixedClock(time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC))
	j := journal.New(kv, clock, journal.DefaultNamespace)
	var n atomic.Int64
	attn := supervision.NewStore(kv, clock, nil, func() string {
		n.Add(1)
		return "attn-quarantine-1"
	}, 0)
	wm := NewWorktreeManager(store, j, attn, fakeLivenessProbe{alive: false})
	return wm, store, j, attn
}

// quarantineFixture runs one dirty-orphan Sweep and returns everything the
// two assertion tests below need, keeping each test under the funlen cap.
type quarantineFixture struct {
	lease   ResourceLease
	w       Worktree
	dest    string
	j       journal.Store
	attn    *supervision.Store
	payload quarantineJournalPayload
}

func newQuarantineFixture(t *testing.T) quarantineFixture {
	t.Helper()
	wm, store, j, attn := newTestQuarantineManager(t)
	ctx := context.Background()
	repo := newTestGitRepo(t)
	lease := ResourceLease{RepoID: repo, ScopeGlob: "**", Holder: "job-q1", Epoch: 3, State: LeaseReleased}
	mustPutLease(t, store, lease)
	w, err := wm.Create(ctx, lease, repo)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "dirty.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatalf("dirty file: %v", err)
	}

	result, err := wm.Sweep(ctx)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if len(result.Quarantined) != 1 {
		t.Fatalf("Quarantined = %v, want exactly one", result.Quarantined)
	}
	dest := quarantinePath(repo, "job-q1")

	entries, err := j.Replay(ctx, leaseEntityID(lease.RepoID, lease.ScopeGlob), journal.Cursor{}, []journal.Kind{journal.KindEscalation})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(entries) != 1 || entries[0].OperationID != "quarantined" {
		t.Fatalf("journal entries = %+v, want exactly one KindEscalation \"quarantined\" entry", entries)
	}
	var payload quarantineJournalPayload
	if err := json.Unmarshal(entries[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal journal payload: %v", err)
	}
	return quarantineFixture{lease: lease, w: w, dest: dest, j: j, attn: attn, payload: payload}
}

func TestWorktreeQuarantineMovesNeverDeletes(t *testing.T) {
	f := newQuarantineFixture(t)
	if _, statErr := os.Stat(f.dest); statErr != nil {
		t.Fatalf("quarantine destination missing: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(f.dest, "dirty.txt")); statErr != nil {
		t.Fatalf("dirty file lost during move (should be MOVED, never deleted): %v", statErr)
	}
}

func TestWorktreeQuarantineJournalsEvidenceAndRaisesAttention(t *testing.T) {
	f := newQuarantineFixture(t)
	if f.payload.Holder != "job-q1" || f.payload.Epoch != 3 || f.payload.MovedFrom != f.w.Path || f.payload.MovedTo != f.dest {
		t.Fatalf("journal payload = %+v, want holder/epoch/moved-from/moved-to matching this move", f.payload)
	}
	if f.payload.DirtyFileCnt < 1 {
		t.Fatalf("journal payload DirtyFileCnt = %d, want >= 1", f.payload.DirtyFileCnt)
	}
	if f.payload.AttentionItem == "" {
		t.Fatal("journal payload has no attention item id recorded")
	}

	items, err := f.attn.ListInScopes(context.Background(), []scope.Ref{{Kind: scope.ScopeKindProject, ID: f.lease.RepoID}}, supervision.Filter{})
	if err != nil {
		t.Fatalf("attn.ListInScopes: %v", err)
	}
	found := 0
	for _, it := range items {
		if it.SourceRef == leaseEntityID(f.lease.RepoID, f.lease.ScopeGlob) {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("attention items for this lease = %d, want exactly 1", found)
	}
}
