package audit

// Purpose: the redaction half of R-21.235. Every assertion below reads
//   the PERSISTED bytes back out of the store, never the Event the test
//   handed Append: a redactor that ran on a copy and left the stored row
//   untouched would pass an assertion on the input and fail these.
// Constraints: real SQLite store under t.TempDir, frozen clock.
// SPORT: AUDIT_REDACTION: ADD (tests).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tagRedactor substitutes one known value for a typed tag, the way the
// secrets substitution pass does. It stands in for the pass itself so
// this package's tests do not import internal/secrets, which imports this
// package.
type tagRedactor struct {
	secret string
	err    error
	calls  int
}

func (r *tagRedactor) Redact(_ context.Context, content []byte) ([]byte, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return bytes.ReplaceAll(content, []byte(r.secret), []byte("<apikey>AUDIT_KEY</apikey>")), nil
}

// storedRecord reads sequence seq straight out of the store, bypassing
// every reader this package offers, so the assertion is against the bytes
// on disk.
func storedRecord(t *testing.T, l *Log, seq uint64) []byte {
	t.Helper()
	data, err := l.store.Get(context.Background(), namespace, recordKey(seq))
	if err != nil {
		t.Fatalf("reading the persisted record: %v", err)
	}
	return data
}

func newRedactingLog(t *testing.T, redactor Redactor) *Log {
	t.Helper()
	return NewWithRedactor(newSQLiteStore(t), runtime.NewFixedClock(testInstant), nil, redactor)
}

func TestAppendRedactsActionAndExplain(t *testing.T) {
	const secret = "AKIA7YQ2XPLM4RZV6WTB"
	redactor := &tagRedactor{secret: secret}
	log := newRedactingLog(t, redactor)

	explain, err := json.Marshal(map[string]string{"reason": "rotated " + secret})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := log.Append(context.Background(), Event{
		Kind: KindPolicyDecide, Actor: "tester",
		Action: "vault.rotate " + secret, Explain: explain,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	stored := storedRecord(t, log, rec.Seq)
	if bytes.Contains(stored, []byte(secret)) {
		t.Fatalf("the persisted record carries the raw secret: %s", stored)
	}
	var readBack Record
	if uerr := json.Unmarshal(stored, &readBack); uerr != nil {
		t.Fatalf("decoding the persisted record: %v", uerr)
	}
	if !strings.Contains(readBack.Action, "<apikey>AUDIT_KEY</apikey>") {
		t.Fatalf("the persisted action was not substituted: %q", readBack.Action)
	}
	if !json.Valid(readBack.Explain) {
		t.Fatalf("redaction left explain invalid as JSON: %s", readBack.Explain)
	}
	// The stored JSON escapes '<' and '>', so the tag is asserted after
	// decoding rather than against the escaped bytes.
	var reason map[string]string
	if uerr := json.Unmarshal(readBack.Explain, &reason); uerr != nil {
		t.Fatalf("decoding the persisted explain: %v", uerr)
	}
	if !strings.Contains(reason["reason"], "<apikey>AUDIT_KEY</apikey>") {
		t.Fatalf("the persisted explain was not substituted: %q", reason["reason"])
	}
	if redactor.calls != 2 {
		t.Fatalf("the redactor ran %d times, want one call per free-text field", redactor.calls)
	}
}

func TestAppendRefusesWhenRedactionFails(t *testing.T) {
	log := newRedactingLog(t, &tagRedactor{err: errors.New("substitution unavailable")})
	_, err := log.Append(context.Background(), Event{
		Kind: KindPolicyDecide, Actor: "tester", Action: "vault.rotate",
	})
	if err == nil {
		t.Fatal("a redactor failure must refuse the append")
	}
	if !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("want a KindIntegrity refusal, got %v", err)
	}
	if _, gerr := log.store.Get(context.Background(), namespace, recordKey(1)); gerr == nil {
		t.Fatal("a refused append left a record in the store")
	}
}

func TestAppendWithoutARedactorStillAppends(t *testing.T) {
	log := New(newSQLiteStore(t), runtime.NewFixedClock(testInstant), nil)
	if _, err := log.Append(context.Background(), Event{
		Kind: KindPolicyDecide, Actor: "tester", Action: "vault.rotate",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

// TestRedactorRejectingEmptyOutputIsRefused covers the seam's own
// fail-closed branch: a redactor that returns no content for a non-empty
// field is a defect in the redactor, and the record is not written.
func TestRedactorRejectingEmptyOutputIsRefused(t *testing.T) {
	log := newRedactingLog(t, nilReturningRedactor{})
	if _, err := log.Append(context.Background(), Event{
		Kind: KindPolicyDecide, Actor: "tester", Action: "vault.rotate",
	}); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("want a KindIntegrity refusal, got %v", err)
	}
}

type nilReturningRedactor struct{}

func (nilReturningRedactor) Redact(context.Context, []byte) ([]byte, error) { return nil, nil }
