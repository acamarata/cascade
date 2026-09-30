// Package pbd (lifecycle_appendif_test.go): AppendIf's own unit tests,
// direct against FileJournalStore — the sequence-compare contract
// (TestAppendIfStaleSequenceConflicts) and the two injected-failure modes
// (TestAppendIfLockErrors). Claim-level, end-to-end CAS behavior
// (concurrent goroutines, cross-process, idempotent operation ids) lives in
// lifecycle_cas_test.go.
// SPORT: plugins/pbd lifecycle_appendif (ADD) — P1-PBD-07.
package pbd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// TestAppendIfStaleSequenceConflicts is acceptance[3]: expectedSeq below
// and above the entity's live entry count each refuse KindConflict and
// leave the file bytes unchanged; the matching expectedSeq appends
// (positive case, defect class 1's paired non-empty assertion).
func TestAppendIfStaleSequenceConflicts(t *testing.T) {
	root := t.TempDir()
	js := NewFileJournalStore(root, nil)
	ctx := context.Background()
	const id = "P1-E00-W0-S00-T1"

	if _, err := js.AppendIf(ctx, id, 0, pews.EventClaim, "op-1", nil); err != nil {
		t.Fatalf("AppendIf(seq 0, first): %v", err)
	}
	journalPath := filepath.Join(root, lifecycleJournalFile)
	before, rerr := os.ReadFile(journalPath)
	if rerr != nil {
		t.Fatalf("reading journal after first append: %v", rerr)
	}

	_, errBelow := js.AppendIf(ctx, id, 0, pews.EventStep, "op-below", nil)
	assertAppendIfConflict(t, errBelow, id, 0, 1, "stale seq below")
	_, errAbove := js.AppendIf(ctx, id, 5, pews.EventStep, "op-above", nil)
	assertAppendIfConflict(t, errAbove, id, 5, 1, "stale seq above")
	after, rerr := os.ReadFile(journalPath)
	if rerr != nil {
		t.Fatalf("reading journal after refused appends: %v", rerr)
	}
	if string(before) != string(after) {
		t.Errorf("journal bytes changed after refused AppendIf calls:\nbefore=%s\nafter=%s", before, after)
	}

	entry, err := js.AppendIf(ctx, id, 1, pews.EventStep, "op-2", nil)
	if err != nil {
		t.Fatalf("AppendIf(matching seq): %v", err)
	}
	if entry.Seq != 2 {
		t.Errorf("AppendIf(matching seq).Seq = %d, want 2", entry.Seq)
	}
}

// assertAppendIfConflict asserts err is exactly AppendIf's stale-sequence
// refusal for entityID: Kind KindConflict AND the precise "expected seq %d,
// have %d" message — identity, not Kind alone (cascade's *Error.Is compares
// Kind only, so it cannot tell a "below" refusal from an "above" one, or
// from any other KindConflict).
func assertAppendIfConflict(t *testing.T, err error, entityID string, expectedSeq, have int, label string) {
	t.Helper()
	wantMsg := fmt.Sprintf("pbd: entity %q: expected seq %d, have %d", entityID, expectedSeq, have)
	cerr, ok := err.(*cascade.Error)
	if !ok || cerr.Kind != cascade.KindConflict || cerr.Msg != wantMsg {
		t.Errorf("AppendIf(%s): err = %v, want Kind=KindConflict Msg=%q", label, err, wantMsg)
	}
}

// TestAppendIfLockErrors is acceptance[5]: an injected lock-acquire error
// and an unreadable journal file each return the error and append nothing
// (never treated as an empty journal); the lock is released on every path,
// proven by a following AppendIf in the same test succeeding.
func TestAppendIfLockErrors(t *testing.T) {
	root := t.TempDir()
	js := NewFileJournalStore(root, nil)
	ctx := context.Background()
	const id = "P1-E00-W0-S00-T2"

	original := acquireLock
	const injectedLockMsg = "injected: lock acquisition failed"
	acquireLock = func(string) (func() error, error) {
		return nil, cascade.New(cascade.KindUnavailable, injectedLockMsg)
	}
	_, lockErr := js.AppendIf(ctx, id, 0, pews.EventClaim, "op-locked", nil)
	if cerr, ok := lockErr.(*cascade.Error); !ok || cerr.Kind != cascade.KindUnavailable || cerr.Msg != injectedLockMsg {
		t.Errorf("AppendIf(injected lock error): err = %v, want Kind=KindUnavailable Msg=%q", lockErr, injectedLockMsg)
	}
	acquireLock = original
	if entries, rerr := js.Replay(ctx, id); rerr != nil || len(entries) != 0 {
		t.Errorf("Replay after injected lock error = %+v, %v, want empty, nil (nothing appended)", entries, rerr)
	}

	if runtime.GOOS != "windows" {
		journalPath := filepath.Join(root, lifecycleJournalFile)
		if werr := os.WriteFile(journalPath, []byte("entities: {}\n"), 0o644); werr != nil {
			t.Fatalf("seeding unreadable journal: %v", werr)
		}
		if cerr := os.Chmod(journalPath, 0o000); cerr != nil {
			t.Fatalf("chmod 000: %v", cerr)
		}
		t.Cleanup(func() { _ = os.Chmod(journalPath, 0o644) })
		if os.Geteuid() != 0 {
			wantMsg := fmt.Sprintf("pbd: reading %q", journalPath)
			_, readErr := js.AppendIf(ctx, id, 0, pews.EventClaim, "op-unreadable", nil)
			if cerr, ok := readErr.(*cascade.Error); !ok || cerr.Kind != cascade.KindInternal || cerr.Msg != wantMsg {
				t.Errorf("AppendIf(unreadable journal): err = %v, want Kind=KindInternal Msg=%q (never treated as empty, never confused with a lock error)", readErr, wantMsg)
			}
			if err := os.Chmod(journalPath, 0o644); err != nil {
				t.Fatalf("restoring perms: %v", err)
			}
			if entries, rerr := js.Replay(ctx, id); rerr != nil || len(entries) != 0 {
				t.Errorf("Replay after unreadable-journal error = %+v, %v, want empty, nil", entries, rerr)
			}
		}
	}

	// The lock was released on every prior path: a normal AppendIf now succeeds.
	if _, err := js.AppendIf(ctx, id, 0, pews.EventClaim, "op-recovers", nil); err != nil {
		t.Fatalf("AppendIf after prior failures: %v, want success (lock must be released on every path)", err)
	}
}
