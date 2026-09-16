package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Purpose (this file): the journal leg's admission rules, asserted on what
//   reached the STORE rather than on the call that was made — an append that
//   happened is the only evidence the record landed.
// SPORT: internal/nodes tests (ADD) — P1-E17-W4-S37-T2.

// recordingSink is a journal store that remembers what it was handed.
type recordingSink struct {
	entities  []string
	operation []string
	payloads  []json.RawMessage
	err       error
}

func (s *recordingSink) AppendNodeStream(_ context.Context, entityID, operationID string, payload json.RawMessage) error {
	if s.err != nil {
		return s.err
	}
	s.entities = append(s.entities, entityID)
	s.operation = append(s.operation, operationID)
	s.payloads = append(s.payloads, payload)
	return nil
}

// streamHarness wires a stream leg over a live attempt register, with the
// dispatch already at its first attempt.
func streamHarness(t *testing.T) (JournalStreamDeps, *recordingSink, uint64) {
	t.Helper()
	reg := NewAttemptRegister()
	attempt := reg.Next("d1")
	sink := &recordingSink{}
	return JournalStreamDeps{Attempts: reg, Sink: sink}, sink, attempt
}

// goodRecord is a record every rule admits, so each case below can vary one
// field and prove that field is what was refused.
func goodRecord(attempt uint64) JournalRecord {
	return JournalRecord{
		DispatchID:  "d1",
		Attempt:     attempt,
		EntityID:    "job-7",
		OperationID: "op-1",
		Payload:     json.RawMessage(`{"step":"build","status":"ok"}`),
	}
}

// TestAStreamedRecordLandsCarryingItsAttempt is the happy path, asserted on
// STORE STATE: the entry is in the sink, under the right entity, and it
// carries the fencing identity a reader needs to tell two attempts apart.
func TestAStreamedRecordLandsCarryingItsAttempt(t *testing.T) {
	deps, sink, attempt := streamHarness(t)

	if err := StreamJournalRecord(context.Background(), deps, goodRecord(attempt)); err != nil {
		t.Fatalf("StreamJournalRecord: %v", err)
	}
	if len(sink.payloads) != 1 {
		t.Fatalf("the store holds %d entries, want 1", len(sink.payloads))
	}
	if sink.entities[0] != "job-7" || sink.operation[0] != "op-1" {
		t.Errorf("appended under entity %q operation %q", sink.entities[0], sink.operation[0])
	}

	var got streamedEntry
	if err := json.Unmarshal(sink.payloads[0], &got); err != nil {
		t.Fatalf("the appended entry is not JSON: %v", err)
	}
	if got.Attempt != attempt || got.DispatchID != "d1" {
		t.Errorf("entry = %+v, want it stamped with dispatch d1 attempt %d", got, attempt)
	}
	// The node's own record survives byte for byte; re-encoding it would
	// change what a replay shows.
	if string(got.Record) != `{"step":"build","status":"ok"}` {
		t.Errorf("the node's record was rewritten: %s", got.Record)
	}
}

// TestASupersededAttemptCannotAppendToTheJournal is the fencing rule on the
// journal leg. A partitioned-but-alive node keeps streaming; if its records
// still appended, the entity's history would interleave two attempts'
// accounts of the same work and read as one confused sequence.
func TestASupersededAttemptCannotAppendToTheJournal(t *testing.T) {
	deps, sink, first := streamHarness(t)
	// The controller gives up on the first attempt and mints a second.
	second := deps.Attempts.Next("d1")
	if second == first {
		t.Fatal("the register minted the same attempt twice")
	}

	err := StreamJournalRecord(context.Background(), deps, goodRecord(first))
	if err == nil {
		t.Fatal("a record from the superseded attempt was appended")
	}
	if !errors.Is(err, ErrStaleAttempt) {
		t.Errorf("error = %v, want it to wrap ErrStaleAttempt", err)
	}
	if len(sink.payloads) != 0 {
		t.Errorf("the store holds %d entries for a superseded attempt", len(sink.payloads))
	}

	// And the replacement attempt still works, so this fences rather than
	// wedging the dispatch.
	if err := StreamJournalRecord(context.Background(), deps, goodRecord(second)); err != nil {
		t.Fatalf("the current attempt was refused: %v", err)
	}
}

// TestAnUnnumberedRecordIsRefused proves attempt 0 is not treated as
// "unfenced". A node built before fencing existed would send exactly that,
// and admitting it would defeat the guarantee for every record.
func TestAnUnnumberedRecordIsRefused(t *testing.T) {
	deps, sink, _ := streamHarness(t)
	rec := goodRecord(0)

	if err := StreamJournalRecord(context.Background(), deps, rec); err == nil {
		t.Fatal("a record carrying no attempt number was appended")
	}
	if len(sink.payloads) != 0 {
		t.Error("an unnumbered record reached the store")
	}
}

// TestAStreamedRecordCarryingAStaticKeyNeverReachesTheStore is the
// red-team rule on this leg: the journal is durable and readable, so a
// secret that lands in it is a secret at rest, not one in flight.
func TestAStreamedRecordCarryingAStaticKeyNeverReachesTheStore(t *testing.T) {
	deps, sink, attempt := streamHarness(t)
	rec := goodRecord(attempt)
	rec.Payload = json.RawMessage(`{"env":{"OPENAI_API_KEY":"sk-livekeymaterial"}}`)

	err := StreamJournalRecord(context.Background(), deps, rec)
	if err == nil {
		t.Fatal("a record carrying a static key was appended to the journal")
	}
	if len(sink.payloads) != 0 {
		t.Fatal("a record carrying a static key reached the store")
	}
}

// TestARecordMissingAnIdentifierIsRefused covers the fields the journal
// cannot be appended without, one at a time.
func TestARecordMissingAnIdentifierIsRefused(t *testing.T) {
	for _, tc := range []struct {
		why    string
		mutate func(*JournalRecord)
	}{
		{"no dispatch id", func(r *JournalRecord) { r.DispatchID = "" }},
		{"no entity id", func(r *JournalRecord) { r.EntityID = "  " }},
		{"no operation id", func(r *JournalRecord) { r.OperationID = "" }},
	} {
		deps, sink, attempt := streamHarness(t)
		rec := goodRecord(attempt)
		tc.mutate(&rec)

		if err := StreamJournalRecord(context.Background(), deps, rec); err == nil {
			t.Errorf("a record with %s was appended", tc.why)
		}
		if len(sink.payloads) != 0 {
			t.Errorf("a record with %s reached the store", tc.why)
		}
	}
}

// TestAnUndecodableRecordIsRefusedBeforeTheStore proves the store is never
// handed something it would reject or, worse, accept as an opaque blob.
func TestAnUndecodableRecordIsRefusedBeforeTheStore(t *testing.T) {
	deps, sink, attempt := streamHarness(t)
	rec := goodRecord(attempt)
	rec.Payload = json.RawMessage(`{not json`)

	if err := StreamJournalRecord(context.Background(), deps, rec); err == nil {
		t.Fatal("an undecodable record was appended")
	}
	if len(sink.payloads) != 0 {
		t.Error("an undecodable record reached the store")
	}
}

// TestTheStreamLegRefusesWhenUnwired proves a half-built stream fails as a
// typed error rather than a nil-pointer panic mid-dispatch.
func TestTheStreamLegRefusesWhenUnwired(t *testing.T) {
	if err := StreamJournalRecord(context.Background(), JournalStreamDeps{}, goodRecord(1)); err == nil {
		t.Fatal("a stream leg with no sink appended a record")
	}
	deps := JournalStreamDeps{Sink: &recordingSink{}}
	if err := StreamJournalRecord(context.Background(), deps, goodRecord(1)); err == nil {
		t.Fatal("a stream leg with no attempt register appended a record")
	}
}

// TestAStoreFailureIsReported proves a failed append is surfaced rather
// than swallowed, so the dispatch does not report a journal it never wrote.
func TestAStoreFailureIsReported(t *testing.T) {
	deps, sink, attempt := streamHarness(t)
	sink.err = errors.New("disk full")

	err := StreamJournalRecord(context.Background(), deps, goodRecord(attempt))
	if err == nil {
		t.Fatal("a failed append reported success")
	}
	if !strings.Contains(err.Error(), "disk full") {
		t.Errorf("error = %v, want the store failure", err)
	}
}
