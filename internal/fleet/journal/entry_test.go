package journal

// Purpose: seal/verifyEntry/decodeEntry and validateAppendInput's
//
//	fail-closed branches.
//
// SPORT: internal.fleet.journal.Entry/ADDED (tests) (P1-E13-W3-S27-T1).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	sqlite "github.com/acamarata/cascade/providers/sqlite"
)

func TestJournalStorageFailure(t *testing.T) {
	ctx := context.Background()
	pstore := newSQLiteStore(t)
	clock := testkit.NewFrozenClock(testInstant)
	store := New(pstore, clock, DefaultNamespace)

	if _, err := store.Append(ctx, "t", KindIntent, "op-1", nil); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	if err := pstore.(*sqlite.Driver).Close(); err != nil {
		t.Fatalf("closing the underlying store: %v", err)
	}

	_, err := store.Append(ctx, "t", KindAck, "op-2", nil)
	if err == nil {
		t.Fatal("Append on a closed store returned nil error, want a typed failure")
	}
	if _, ok := cascade.KindOf(err); !ok {
		t.Fatalf("Append on a closed store: err = %v, not a pkg/cascade taxonomy error", err)
	}
}

func TestJournalHeadRecordCorrupted(t *testing.T) {
	ctx := context.Background()
	store, pstore, _ := newTestStore(t)

	if err := pstore.Put(ctx, DefaultNamespace, headKey("e"), []byte("not json")); err != nil {
		t.Fatalf("seeding corrupted head: %v", err)
	}
	if _, err := store.Append(ctx, "e", KindNodeStream, "op-1", nil); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("Append over a corrupted head record: err = %v, want KindIntegrity", err)
	}
}

func sampleUnsealed() Entry {
	return Entry{
		EntityID:    "task-1",
		Seq:         1,
		Kind:        KindIntent,
		OperationID: "op-1",
		Payload:     json.RawMessage(`{"a":1}`),
		TSUnixNano:  1_700_000_000,
	}
}

func TestJournalSealAndVerify(t *testing.T) {
	e, err := seal(sampleUnsealed())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if e.Checksum == "" {
		t.Fatal("seal did not set Checksum")
	}
	if err := verifyEntry(e); err != nil {
		t.Fatalf("verifyEntry on a freshly sealed entry: %v", err)
	}
}

func TestJournalVerifyDetectsTampering(t *testing.T) {
	e, err := seal(sampleUnsealed())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	e.OperationID = "op-2" // mutate after sealing
	err = verifyEntry(e)
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("verifyEntry on a tampered entry: err = %v, want KindIntegrity", err)
	}
}

func TestJournalVerifyRejectsInvalidKind(t *testing.T) {
	e, err := seal(sampleUnsealed())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	e.Kind = Kind(200)
	e.Checksum = "" // must not matter: kind is checked before checksum
	err = verifyEntry(e)
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("verifyEntry with an out-of-range kind: err = %v, want KindInvalidInput", err)
	}
}

func TestJournalDecodeEntryRoundTrip(t *testing.T) {
	e, err := seal(sampleUnsealed())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := decodeEntry(data)
	if err != nil {
		t.Fatalf("decodeEntry: %v", err)
	}
	if got.EntityID != e.EntityID || got.Seq != e.Seq || got.Kind != e.Kind ||
		got.OperationID != e.OperationID || got.TSUnixNano != e.TSUnixNano ||
		got.Checksum != e.Checksum || string(got.Payload) != string(e.Payload) {
		t.Fatalf("decodeEntry round-trip = %+v, want %+v", got, e)
	}
}

func TestJournalDecodeEntryRejectsGarbage(t *testing.T) {
	if _, err := decodeEntry([]byte("not json")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeEntry(garbage): err = %v, want KindIntegrity", err)
	}
}

func TestJournalTryDecodeEntry(t *testing.T) {
	e, err := seal(sampleUnsealed())
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	data, _ := json.Marshal(e)
	if _, ok := tryDecodeEntry(data); !ok {
		t.Fatal("tryDecodeEntry on a valid entry reported ok == false")
	}
	if _, ok := tryDecodeEntry([]byte("garbage")); ok {
		t.Fatal("tryDecodeEntry on garbage reported ok == true")
	}
}

func TestJournalValidateAppendInput(t *testing.T) {
	valid := json.RawMessage(`{"x":1}`)
	cases := []struct {
		name        string
		entityID    string
		kind        Kind
		operationID string
		payload     json.RawMessage
		wantKind    cascade.Kind
	}{
		{"missing entity id", "", KindIntent, "op", valid, cascade.KindInvalidInput},
		{"missing operation id", "e", KindIntent, "", valid, cascade.KindInvalidInput},
		{"invalid kind", "e", Kind(0), "op", valid, cascade.KindInvalidInput},
		{"bad json payload", "e", KindIntent, "op", json.RawMessage(`{bad`), cascade.KindInvalidInput},
		{"valid", "e", KindIntent, "op", valid, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateAppendInput(c.entityID, c.kind, c.operationID, c.payload)
			if c.wantKind == 0 {
				if err != nil {
					t.Fatalf("validateAppendInput: %v, want nil", err)
				}
				return
			}
			if !cascade.HasKind(err, c.wantKind) {
				t.Fatalf("validateAppendInput: err = %v, want kind %v", err, c.wantKind)
			}
		})
	}
}
