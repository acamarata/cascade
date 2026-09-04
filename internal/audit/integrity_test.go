package audit

// Purpose: the two properties that make this an audit log rather than a
//   log. First: a record altered, replaced, or removed BEHIND the public
//   API is detected on the next read, not silently accepted. Second: a
//   record about a secret does not contain the secret.
// Constraints: Art.7.1, Art.7.3. Every tamper below is applied through
//   the raw store, deliberately bypassing this package's own write path,
//   because that is exactly the attack being modelled.
// SPORT: internal.audit.Log/ADDED (tests) (P1-E09-W2-S18-T2).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// seedThree appends three records and returns the log with its raw store.
func seedThree(t *testing.T) (*Log, provider.Store, []Record) {
	t.Helper()
	ctx := context.Background()
	log, store, _, _ := newTestLog(t)
	var recs []Record
	for i := 1; i <= 3; i++ {
		rec, err := log.Append(ctx, sampleEvent(i))
		if err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
		recs = append(recs, rec)
	}
	return log, store, recs
}

// assertTamperDetected proves every read surface refuses, with the
// integrity Kind, and hands back nothing.
func assertTamperDetected(ctx context.Context, t *testing.T, log *Log, id string) {
	t.Helper()
	page, err := log.Query(ctx, Filter{})
	if err == nil {
		t.Fatalf("Query accepted a tampered log and returned %d records", len(page.Records))
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Errorf("Query: %v, want KindIntegrity", err)
	}
	if len(page.Records) != 0 {
		t.Errorf("Query returned %d records from a tampered log", len(page.Records))
	}
	if verr := log.Verify(ctx); !cascade.HasKind(verr, cascade.KindIntegrity) {
		t.Errorf("Verify: %v, want KindIntegrity", verr)
	}
	if id == "" {
		return
	}
	if _, eerr := log.Explain(ctx, id); !cascade.HasKind(eerr, cascade.KindIntegrity) {
		t.Errorf("Explain of the tampered record: %v, want KindIntegrity", eerr)
	}
}

// TestAuditDetectsAlteredRecord edits a stored record's field without
// touching its hash. This is the crude rewrite.
func TestAuditDetectsAlteredRecord(t *testing.T) {
	ctx := context.Background()
	log, store, recs := seedThree(t)

	raw, err := store.Get(ctx, namespace, recordKey(2))
	if err != nil {
		t.Fatalf("reading record 2: %v", err)
	}
	altered := strings.Replace(string(raw), `"actor":"user"`, `"actor":"root"`, 1)
	if altered == string(raw) {
		t.Fatal("the test did not actually alter the record")
	}
	if err := store.Put(ctx, namespace, recordKey(2), []byte(altered)); err != nil {
		t.Fatalf("writing the altered record: %v", err)
	}
	assertTamperDetected(ctx, t, log, recs[1].ID)
}

// TestAuditDetectsResealedRecord replaces a record with a different one
// that carries a correct hash of its own. Per-record hashing alone would
// accept this; the chain is what refuses it.
func TestAuditDetectsResealedRecord(t *testing.T) {
	ctx := context.Background()
	log, store, recs := seedThree(t)

	forged := recs[1]
	forged.Actor = "root"
	forged.Verdict = "allow"
	sealedForgery, err := seal(forged)
	if err != nil {
		t.Fatalf("sealing the forgery: %v", err)
	}
	data, err := json.Marshal(sealedForgery)
	if err != nil {
		t.Fatalf("encoding the forgery: %v", err)
	}
	if verr := verify(sealedForgery); verr != nil {
		t.Fatalf("the forgery is not self-consistent, so this test proves nothing: %v", verr)
	}
	if err := store.Put(ctx, namespace, recordKey(2), data); err != nil {
		t.Fatalf("writing the forgery: %v", err)
	}
	assertTamperDetected(ctx, t, log, "")
}

// TestAuditDetectsRemovedRecord deletes a record from the middle of the
// log, which leaves a gap in the sequence.
func TestAuditDetectsRemovedRecord(t *testing.T) {
	ctx := context.Background()
	log, store, _ := seedThree(t)

	if err := store.Delete(ctx, namespace, recordKey(2)); err != nil {
		t.Fatalf("deleting record 2: %v", err)
	}
	assertTamperDetected(ctx, t, log, "")
}

// TestAuditDetectsTruncatedTail deletes the newest record, which leaves no
// gap at all. Only the tail pointer catches this one.
func TestAuditDetectsTruncatedTail(t *testing.T) {
	ctx := context.Background()
	log, store, _ := seedThree(t)

	if err := store.Delete(ctx, namespace, recordKey(3)); err != nil {
		t.Fatalf("deleting record 3: %v", err)
	}
	if err := log.Verify(ctx); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Verify after truncation: %v, want KindIntegrity", err)
	}
	page, err := log.Query(ctx, Filter{})
	if err == nil {
		t.Fatalf("Query accepted a truncated log and returned %d records", len(page.Records))
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Query after truncation: %v, want KindIntegrity", err)
	}
}

// TestAuditDetectsTruncatedBytes cuts a stored record's value short.
func TestAuditDetectsTruncatedBytes(t *testing.T) {
	ctx := context.Background()
	log, store, _ := seedThree(t)

	raw, err := store.Get(ctx, namespace, recordKey(1))
	if err != nil {
		t.Fatalf("reading record 1: %v", err)
	}
	if err := store.Put(ctx, namespace, recordKey(1), raw[:len(raw)/2]); err != nil {
		t.Fatalf("writing the truncated record: %v", err)
	}
	assertTamperDetected(ctx, t, log, "")
}

// TestAuditDetectsCorruptHeadPointer covers the head pointer itself being
// unreadable, which must refuse rather than fall back to "no tail known".
func TestAuditDetectsCorruptHeadPointer(t *testing.T) {
	ctx := context.Background()
	log, store, _ := seedThree(t)

	if err := store.Put(ctx, namespace, headKey, []byte("{broken")); err != nil {
		t.Fatalf("corrupting the head pointer: %v", err)
	}
	if err := log.Verify(ctx); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Verify with a corrupt head pointer: %v, want KindIntegrity", err)
	}
	if _, err := New(store, nil, nil).Append(ctx, sampleEvent(9)); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Append with a corrupt head pointer: %v, want KindIntegrity", err)
	}
}

// TestAuditDetectsCorruptIndexEntry covers the id index pointing somewhere
// it should not.
func TestAuditDetectsCorruptIndexEntry(t *testing.T) {
	ctx := context.Background()
	log, store, recs := seedThree(t)

	if err := store.Put(ctx, namespace, indexKey(recs[0].ID), []byte("not-a-number")); err != nil {
		t.Fatalf("corrupting the index: %v", err)
	}
	if _, err := log.Explain(ctx, recs[0].ID); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Explain with a corrupt index: %v, want KindIntegrity", err)
	}
	if err := store.Put(ctx, namespace, indexKey(recs[0].ID), []byte("99")); err != nil {
		t.Fatalf("repointing the index: %v", err)
	}
	if _, err := log.Explain(ctx, recs[0].ID); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Explain via an index pointing at nothing: %v, want KindIntegrity", err)
	}
	if err := store.Put(ctx, namespace, indexKey(recs[0].ID), []byte("2")); err != nil {
		t.Fatalf("repointing the index: %v", err)
	}
	if _, err := log.Explain(ctx, recs[0].ID); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Explain via an index pointing at the wrong record: %v, want KindIntegrity", err)
	}
}

// secretValue is the canary. It is what a vault access is ABOUT, and it
// must not appear anywhere in the record that audits that access, in the
// bytes stored for it, or in the notification published about it.
const secretValue = "hunter2-canary-do-not-store"

// TestAuditRecordDoesNotLeakWhatItAudits captures the real output of a
// record describing a sensitive operation and asserts the sensitive value
// is absent from every one of them.
func TestAuditRecordDoesNotLeakWhatItAudits(t *testing.T) {
	ctx := context.Background()
	log, store, bus, _ := newTestLog(t)

	rec, err := log.Append(ctx, Event{
		Kind:           KindElevationGrant,
		Actor:          "alice",
		Action:         "vault.get",
		ParamsHash:     HashParams([]byte("name=deploy-key&value=" + secretValue)),
		RiskLevel:      "high",
		Verdict:        "allow",
		Explain:        json.RawMessage(`{"profile":"strict","reason":"elevation granted"}`),
		PolicySnapshot: json.RawMessage(`{"version":7}`),
		Outcome:        "granted",
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	assertNoLeakAcrossSurfaces(ctx, t, log, store, bus, rec)
	if rec.ParamsHash == "" || strings.Contains(rec.ParamsHash, secretValue) {
		t.Error("the parameter digest is missing or is not a digest")
	}
}

// assertNoLeakAcrossSurfaces checks every API surface for the secretValue.
func assertNoLeakAcrossSurfaces(
	ctx context.Context, t *testing.T,
	log *Log, store provider.Store, bus *events.Bus, rec Record,
) {
	t.Helper()
	stored, err := store.Get(ctx, namespace, recordKey(rec.Seq))
	if err != nil {
		t.Fatalf("reading the stored record: %v", err)
	}
	page, err := log.Query(ctx, Filter{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	explanation, err := log.Explain(ctx, rec.ID)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	queryJSON, err := json.Marshal(page)
	if err != nil {
		t.Fatalf("encoding the query result: %v", err)
	}
	explainJSON, err := json.Marshal(explanation)
	if err != nil {
		t.Fatalf("encoding the explanation: %v", err)
	}
	surfaces := map[string][]byte{
		"the stored record": stored,
		"the query result":  queryJSON,
		"the explanation":   explainJSON,
		"the notification":  busPayloads(ctx, t, bus),
	}
	for name, data := range surfaces {
		if strings.Contains(string(data), secretValue) {
			t.Errorf("%s contains the audited secret", name)
		}
	}
}

// busPayloads concatenates every payload the bus carries, so one
// Contains check covers the whole notification surface.
func busPayloads(ctx context.Context, t *testing.T, bus *events.Bus) []byte {
	t.Helper()
	published, err := bus.Replay(ctx, namespace, 0)
	if err != nil {
		t.Fatalf("bus Replay: %v", err)
	}
	var all []byte
	for _, ev := range published {
		all = append(all, ev.Payload...)
		all = append(all, []byte(ev.Source)...)
	}
	return all
}
