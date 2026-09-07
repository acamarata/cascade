package journal

// Purpose: the closed eight-kind enum asserted against R-21.216's own
//
//	text rather than against AllKinds itself, plus Entry.Time and
//	Kind.String/Valid.
//
// Constraints: Art.7.1 (nothing outside t.TempDir elsewhere in this
//
//	package's tests), Art.7.3 (no wall clock in a test's expectations).
//
// SPORT: internal.fleet.journal.Store/ADDED (tests) (P1-E13-W3-S27-T1).

import (
	"testing"
	"time"
)

func TestJournalCloseIsNoop(t *testing.T) {
	store, _, _ := newTestStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v, want nil", err)
	}
}

// ratifiedKinds is R-21.216's own enumeration, transcribed independently
// of AllKinds so the assertion below compares the code against the ruling
// text instead of against itself.
var ratifiedKinds = []string{
	"intent", "ack", "checkpoint", "escalation", "resume-cursor",
	"fan-out-leg-started", "fan-out-leg-done", "node-stream",
}

func TestJournalKindEnumIsClosed(t *testing.T) {
	all := AllKinds()
	if len(all) != len(ratifiedKinds) {
		t.Fatalf("AllKinds() has %d kinds, R-21.216 ratifies %d", len(all), len(ratifiedKinds))
	}
	for i, k := range all {
		if k.String() != ratifiedKinds[i] {
			t.Errorf("AllKinds()[%d] = %q, want %q", i, k.String(), ratifiedKinds[i])
		}
		if !k.Valid() {
			t.Errorf("AllKinds()[%d] (%s) reports Valid() == false", i, k)
		}
	}
}

func TestJournalKindZeroValueInvalid(t *testing.T) {
	var zero Kind
	if zero.Valid() {
		t.Fatal("zero Kind reports Valid() == true, want false (fail closed)")
	}
	if zero.String() != "invalid-kind" {
		t.Errorf("zero Kind.String() = %q, want %q", zero.String(), "invalid-kind")
	}
}

func TestJournalKindBeyondEnumInvalid(t *testing.T) {
	beyond := Kind(uint8(KindNodeStream) + 1)
	if beyond.Valid() {
		t.Fatal("Kind beyond the closed enum reports Valid() == true, want false")
	}
}

func TestJournalEntryTime(t *testing.T) {
	instant := time.Unix(1_700_000_000, 123).UTC()
	e := Entry{TSUnixNano: instant.UnixNano()}
	if got := e.Time(); !got.Equal(instant) {
		t.Fatalf("Entry.Time() = %v, want %v", got, instant)
	}
}
