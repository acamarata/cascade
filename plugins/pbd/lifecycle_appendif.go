// Package pbd (lifecycle_appendif.go): FileJournalStore.AppendIf is
// pews.CASAppender's shipped implementation — the fix for the race this
// ticket's journal records: two concurrent `pbd claim` calls (goroutines in
// one process, or two processes sharing a root) could both replay
// StateUnclaimed and both Append a claim entry. AppendIf instead holds one
// exclusive advisory lock (lifecycle_lock_unix.go/lifecycle_lock_windows.go)
// across load, the sequence compare, and save, so only one caller's compare
// can ever see a matching count.
// Constraints: imports pkg/** and internal/pews ONLY (Art.10.2); the lock
// is never held across a user callback or network call — load/compare/save
// only.
// SPORT: plugins/pbd lifecycle_appendif (ADD) — P1-PBD-07.
package pbd

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/plugins/pbd/internal/pews"
)

// lifecycleJournalLockFile is the sidecar lock filename, sibling to
// lifecycle-journal.yaml itself — locking the journal file directly would
// contend with plain os.ReadFile/os.Rename calls made outside any lock
// (Append/Replay), which is exactly the confusion providers/sqlite's own
// sidecar-lock rationale warns against.
const lifecycleJournalLockFile = "lifecycle-journal.lock"

var _ pews.CASAppender = (*FileJournalStore)(nil)

// acquireLock is AppendIf's lock-acquire seam. Production code always
// leaves it as acquireJournalLock; a test-only failure double lives in
// lifecycle_appendif_test.go (defect class 4 — never a production flag).
var acquireLock = acquireJournalLock

// AppendIf implements pews.CASAppender. A repeated operationID for
// entityID+event returns the existing entry unchanged (idempotent replay);
// the same operationID against a different event refuses KindConflict. A
// mismatched expectedSeq refuses KindConflict and leaves the file bytes
// untouched. The lock is acquired before load and released (deferred) on
// every return path, including every error path.
func (s *FileJournalStore) AppendIf(ctx context.Context, entityID string, expectedSeq int, event pews.LifecycleEvent, operationID string, payload json.RawMessage) (pews.JournalEntry, error) {
	if cerr := ctx.Err(); cerr != nil {
		return pews.JournalEntry{}, cascade.Wrap(cascade.KindCanceled, cerr, "pbd: AppendIf")
	}
	unlock, err := acquireLock(filepath.Join(s.root, lifecycleJournalLockFile))
	if err != nil {
		return pews.JournalEntry{}, err
	}
	defer func() { _ = unlock() }()

	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return pews.JournalEntry{}, err
	}
	if doc.Entities == nil {
		doc.Entities = map[string][]lifecycleJournalEntry{}
	}
	existing := doc.Entities[entityID]
	if entry, found, cerr := findByOperationID(existing, entityID, event, operationID); cerr != nil {
		return pews.JournalEntry{}, cerr
	} else if found {
		return entry, nil
	}
	if len(existing) != expectedSeq {
		return pews.JournalEntry{}, cascade.Newf(cascade.KindConflict, "pbd: entity %q: expected seq %d, have %d", entityID, expectedSeq, len(existing))
	}
	var ts int64
	if s.clock != nil {
		ts = s.clock.Now().UnixNano()
	}
	seq := uint64(len(existing)) + 1
	entry := lifecycleJournalEntry{Seq: seq, Event: string(event), OperationID: operationID, Payload: string(payload), TSUnixNano: ts}
	doc.Entities[entityID] = append(existing, entry)
	if err := s.save(doc); err != nil {
		return pews.JournalEntry{}, err
	}
	return pews.JournalEntry{EntityID: entityID, Seq: seq, Event: event, OperationID: operationID, Payload: payload, TSUnixNano: ts}, nil
}

// findByOperationID reports whether existing already carries operationID.
// A match on the same event returns the decoded entry (found=true, no
// error): AppendIf must add nothing and return it unchanged. A match on a
// different event refuses KindConflict: an operation id is never replayed
// onto a second event.
func findByOperationID(existing []lifecycleJournalEntry, entityID string, event pews.LifecycleEvent, operationID string) (pews.JournalEntry, bool, error) {
	for _, e := range existing {
		if e.OperationID != operationID {
			continue
		}
		if e.Event != string(event) {
			return pews.JournalEntry{}, false, cascade.Newf(cascade.KindConflict, "pbd: entity %q: operation_id %q already recorded event %q, not %q", entityID, operationID, e.Event, event)
		}
		return pews.JournalEntry{EntityID: entityID, Seq: e.Seq, Event: event, OperationID: e.OperationID, Payload: json.RawMessage(e.Payload), TSUnixNano: e.TSUnixNano}, true, nil
	}
	return pews.JournalEntry{}, false, nil
}
