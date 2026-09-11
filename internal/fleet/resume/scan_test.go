// Purpose: task 2 and task 7's error-path tests — classification against
//   real journal state, including a real torn-tail truncation (which is
//   this design's mechanism for "unrecognized kind": journal.Store's own
//   Recover treats any undecodable entry, including one carrying a kind
//   value outside the closed enum, as the start of a torn tail and
//   removes it before Replay ever runs — see entryKeyFor's doc comment).
// Constraints: Art.7.1; corruption seeded via the raw provider.Store,
//   never through journal's own Append (which validates Kind at write
//   time and would refuse an invalid one outright).
// SPORT: internal.fleet.resume.ResumeManager/ADDED (tests) (P1-E13-W3-S27-T2).

package resume

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// entryKeyFor reconstructs journal package's private per-entity entry key
// scheme ("e:" + entityID + "\x00" + 20-digit zero-padded seq,
// internal/fleet/journal/store.go's entryKey/keyDelim/seqDigits) so this
// external test package can seed a raw corrupted record the exact way
// internal/fleet/journal/replay_test.go's own TestJournalChecksumVerifiedOnRead
// does from inside the package. This duplicates an unexported format by
// necessity (resume_test lives outside package journal) — it is coupled
// to that format and would need updating if journal's key scheme ever
// changes; documented here rather than silently relied upon.
func entryKeyFor(entityID string, seq uint64) string {
	return fmt.Sprintf("e:%s\x00%020d", entityID, seq)
}

// seedCorruptEntry appends one good entry (seq 1) through its own,
// throwaway journal.Store instance, then overwrites what would be seq
// 2's slot with an undecodable record directly through the raw
// provider.Store, bypassing Append's own validation entirely. Using a
// throwaway Store for the seed (rather than the one the caller's Manager
// will use) matters: journal.SQLiteStore memoizes its torn-tail recovery
// report per (instance, entity) the first time either is touched
// (store.go's s.recovered map) — seeding through the SAME instance the
// Manager later uses would memoize "clean" before the corruption exists,
// so the Manager's own Recover call would never re-scan and would report
// Truncated:0 from stale cache, exactly the trap
// internal/fleet/journal/replay_test.go's own
// TestJournalChecksumVerifiedOnRead works around by opening a fresh
// SQLiteStore for its post-corruption assertions.
func seedCorruptEntry(t *testing.T, raw provider.Store, entityID string) {
	t.Helper()
	ctx := context.Background()
	seed := journal.New(raw, testkit.NewFrozenClock(testInstant), journal.DefaultNamespace)
	if _, err := seed.Append(ctx, entityID, journal.KindIntent, "op-good", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("seed good entry: %v", err)
	}
	bad := []byte(`{"entity_id":"` + entityID + `","seq":2,"kind":9,"operation_id":"op-bad","checksum":"deadbeef"}`)
	if err := raw.Put(ctx, journal.DefaultNamespace, entryKeyFor(entityID, 2), bad); err != nil {
		t.Fatalf("seed corrupt entry: %v", err)
	}
	// Bump the head pointer past seq 2 so scanAndTruncate's forward walk
	// actually reaches it (Get would otherwise stop at KindNotFound
	// right after the good seq-1 entry).
	head := []byte(`{"seq":2}`)
	if err := raw.Put(ctx, journal.DefaultNamespace, "h:"+entityID+"\x00", head); err != nil {
		t.Fatalf("seed head: %v", err)
	}
}

func TestResumeUnknownKindFailClosed(t *testing.T) {
	_, raw, _ := newRealStore(t)
	seedCorruptEntry(t, raw, "task-unknown-kind")
	store := journal.New(raw, testkit.NewFrozenClock(testInstant), journal.DefaultNamespace)

	var calls []fakeFanOutCall
	mgr, err := New(store, fakeFanOut(&calls, nil, nil), nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v, want nil (per-entity failures are reported in Outcomes)", err)
	}
	if len(report.Outcomes) != 1 || report.Outcomes[0].Classification != ClassTerminal {
		t.Fatalf("Outcomes = %+v, want exactly one ClassTerminal outcome", report.Outcomes)
	}
	if report.Outcomes[0].Err == nil {
		t.Fatal("Terminal outcome carries a nil error, want a typed error")
	}
	if len(calls) != 0 {
		t.Fatalf("fanOut called for an unrecognized-kind cursor, want 0 calls (never promoted to resumable)")
	}
}

func TestResumePartialCheckpoint(t *testing.T) {
	_, raw, _ := newRealStore(t)
	seedCorruptEntry(t, raw, "task-torn-tail")
	store := journal.New(raw, testkit.NewFrozenClock(testInstant), journal.DefaultNamespace)

	var calls []fakeFanOutCall
	mgr, err := New(store, fakeFanOut(&calls, nil, nil), nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Attention) != 1 {
		t.Fatalf("Attention = %+v, want exactly one item (truncated tail is never treated as no work)", report.Attention)
	}
	if len(report.Outcomes) != 1 || report.Outcomes[0].Classification != ClassTerminal {
		t.Fatalf("Outcomes = %+v, want exactly one ClassTerminal outcome", report.Outcomes)
	}
	if !cascade.HasKind(report.Outcomes[0].Err, cascade.KindIntegrity) {
		t.Fatalf("Outcomes[0].Err = %v, want KindIntegrity", report.Outcomes[0].Err)
	}
}

func TestClassify_FanOutFullyCompleted_NothingToResume(t *testing.T) {
	req, _ := json.Marshal(provider.ModelRequest{TaskID: "t1", TaskClass: "chat", Inputs: []provider.ChatMessage{{Role: "user", Content: "hi"}}})
	cursorPayload, _ := json.Marshal(resumeCursorPayload{T: "cursor", TaskID: "t1", Legs: 1, Request: req})
	donePayload, _ := json.Marshal(legPayload{LegIndex: 0, JobID: "job-1", Attempt: 1})
	entries := []journal.Entry{
		{EntityID: "t1", Seq: 1, Kind: journal.KindResumeCursor, Payload: cursorPayload},
		{EntityID: "t1", Seq: 2, Kind: journal.KindFanOutLegDone, Payload: donePayload},
	}
	cur, attention, err := classify(entries)
	if err != nil || attention != nil || cur != nil {
		t.Fatalf("classify(fully completed) = (%+v, %+v, %v), want (nil, nil, nil)", cur, attention, err)
	}
}

func TestClassify_FanOutUndecodableRequest_UnrecognizedShape(t *testing.T) {
	// A JSON array is well-formed JSON (so the outer resumeCursorPayload
	// marshals fine) but cannot unmarshal into provider.ModelRequest (a
	// struct) — the shape json.Unmarshal genuinely rejects, unlike a
	// non-JSON literal, which would break the OUTER envelope's own
	// marshal before this test ever reaches classifyFanOut's decode.
	cursorPayload, _ := json.Marshal(resumeCursorPayload{T: "cursor", TaskID: "t1", Legs: 2, Request: json.RawMessage(`[1,2,3]`)})
	entries := []journal.Entry{{EntityID: "t1", Seq: 1, Kind: journal.KindResumeCursor, Payload: cursorPayload}}
	_, _, err := classify(entries)
	if err != ErrUnrecognizedShape {
		t.Fatalf("classify(undecodable request) err = %v, want ErrUnrecognizedShape", err)
	}
}

func TestResumeAmbiguousOutcomeHeld(t *testing.T) {
	store, _, _ := newRealStore(t)
	ctx := context.Background()
	notIdempotent, _ := json.Marshal(intentPayload{ActionID: "act-1", Idempotent: false, TaskID: "t-ambiguous"})
	if _, err := store.Append(ctx, "t-ambiguous", journal.KindIntent, "op-1", notIdempotent); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	var calls []fakeFanOutCall
	mgr, err := New(store, fakeFanOut(&calls, nil, nil), nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Outcomes) != 1 || report.Outcomes[0].Classification != ClassUnknownOutcome {
		t.Fatalf("Outcomes = %+v, want exactly one ClassUnknownOutcome", report.Outcomes)
	}
	if !cascade.HasKind(report.Outcomes[0].Err, cascade.KindConflict) {
		t.Fatalf("Outcomes[0].Err = %v, want KindConflict (ErrAmbiguousOutcome)", report.Outcomes[0].Err)
	}

	// Held, never auto-replayed: a second Run over the SAME unacknowledged
	// intent reaches the identical classification again, never promoting
	// it to resumable on its own.
	report2, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(report2.Outcomes) != 1 || report2.Outcomes[0].Classification != ClassUnknownOutcome {
		t.Fatalf("second Run Outcomes = %+v, want the SAME held classification, never auto-replayed", report2.Outcomes)
	}
}

func TestResumeIdempotentActionsOnlyRequeued(t *testing.T) {
	store, _, _ := newRealStore(t)
	ctx := context.Background()
	idempotent, _ := json.Marshal(intentPayload{ActionID: "act-2", Idempotent: true, TaskID: "t-idempotent"})
	if _, err := store.Append(ctx, "t-idempotent", journal.KindIntent, "op-1", idempotent); err != nil {
		t.Fatalf("seed intent: %v", err)
	}

	var calls []fakeFanOutCall
	mgr, err := New(store, fakeFanOut(&calls, nil, nil), nil, nil, nil, nil, "darwin")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := mgr.Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Outcomes) != 1 || report.Outcomes[0].Classification != ClassResumable {
		t.Fatalf("Outcomes = %+v, want exactly one ClassResumable (auto re-queued)", report.Outcomes)
	}

	entries, err := store.Replay(ctx, "t-idempotent", journal.Cursor{EntityID: "t-idempotent", Seq: 0}, []journal.Kind{journal.KindIntent})
	if err != nil {
		t.Fatalf("Replay after re-queue: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Intent entries after re-queue = %d, want 2 (original + fresh re-queue)", len(entries))
	}
}
