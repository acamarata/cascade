package audit

// Purpose: the schema half of the package's tests, the closed event-kind
//   enum asserted against the ratified list rather than against itself,
//   record sealing and hash verification, fail-closed event validation,
//   and ULID shape.
// Constraints: Art.7.1 (nothing outside t.TempDir), Art.7.3 (no wall
//   clock in a test's expectations).
// SPORT: internal.audit.Record/ADDED (tests) (P1-E09-W2-S18-T2).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ratifiedKinds is the eleven-value enum transcribed from the ticket
// contract's own list, kept separate from AllKinds so the assertion below
// compares the code against the spec instead of against itself.
var ratifiedKinds = []string{
	"policy.decide", "policy.route",
	"approval.enqueue", "approval.dedup", "approval.expire",
	"config.reload",
	"approval.grant", "approval.deny",
	"elevation.attempt", "elevation.grant", "elevation.deny",
}

func TestAuditKindEnumIsClosed(t *testing.T) {
	if len(AllKinds) != len(ratifiedKinds) {
		t.Fatalf("AllKinds has %d kinds, the ratified enum has %d", len(AllKinds), len(ratifiedKinds))
	}
	got := make(map[string]bool, len(AllKinds))
	for _, k := range AllKinds {
		if !k.Valid() {
			t.Errorf("AllKinds contains %q, which Valid rejects", string(k))
		}
		got[string(k)] = true
	}
	for _, want := range ratifiedKinds {
		if !got[want] {
			t.Errorf("ratified kind %q is missing from AllKinds", want)
		}
	}
	for _, bad := range []string{"", "policy", "POLICY.DECIDE", "policy.decide ", "anything.else"} {
		if Kind(bad).Valid() {
			t.Errorf("Kind(%q).Valid() = true, want false: the enum must be closed", bad)
		}
	}
}

func TestAuditSealAndVerify(t *testing.T) {
	rec := Record{Seq: 1, ID: "01ABC", TSUnixNano: 42, Event: Event{
		Kind: KindPolicyDecide, Actor: "user", Action: "read",
	}}
	sealed, err := seal(rec)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if sealed.Hash == "" {
		t.Fatal("seal left the hash empty")
	}
	if err := verify(sealed); err != nil {
		t.Fatalf("verify of a freshly sealed record: %v", err)
	}
	// Sealing is deterministic: the same record hashes the same way on
	// every run, which is what lets a later read detect a change.
	again, err := seal(rec)
	if err != nil {
		t.Fatalf("seal (second): %v", err)
	}
	if again.Hash != sealed.Hash {
		t.Fatalf("seal is not deterministic: %q then %q", sealed.Hash, again.Hash)
	}
	for name, mutate := range map[string]func(*Record){
		"actor":    func(r *Record) { r.Actor = "someone-else" },
		"verdict":  func(r *Record) { r.Verdict = "allow" },
		"sequence": func(r *Record) { r.Seq = 99 },
		"time":     func(r *Record) { r.TSUnixNano = 43 },
		"prevhash": func(r *Record) { r.PrevHash = "deadbeef" },
	} {
		altered := sealed
		mutate(&altered)
		if err := verify(altered); err == nil {
			t.Errorf("verify accepted a record with an altered %s", name)
		} else if !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Errorf("altered %s: kind = %v, want KindIntegrity", err, name)
		}
	}
}

func TestAuditDecodeRecordRejectsGarbage(t *testing.T) {
	if _, err := decodeRecord([]byte("{not json")); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeRecord of garbage: %v, want KindIntegrity", err)
	}
	if _, err := decodeRecord([]byte(`{"seq":1,"hash":"nope"}`)); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("decodeRecord of a record with a wrong hash: %v, want KindIntegrity", err)
	}
}

func TestAuditValidateEventFailsClosed(t *testing.T) {
	valid := Event{Kind: KindPolicyDecide, Actor: "user", Action: "read"}
	if err := validateEvent(valid); err != nil {
		t.Fatalf("a valid event was refused: %v", err)
	}
	cases := map[string]Event{
		"unknown kind":       {Kind: Kind("policy.invent"), Actor: "u", Action: "a"},
		"empty kind":         {Actor: "u", Action: "a"},
		"missing actor":      {Kind: KindPolicyDecide, Action: "a"},
		"missing action":     {Kind: KindPolicyDecide, Actor: "u"},
		"oversize actor":     {Kind: KindPolicyDecide, Actor: strings.Repeat("x", maxFieldBytes+1), Action: "a"},
		"control character":  {Kind: KindPolicyDecide, Actor: "u\nadmin", Action: "a"},
		"non-json explain":   {Kind: KindPolicyDecide, Actor: "u", Action: "a", Explain: json.RawMessage("{oops")},
		"non-json snapshot":  {Kind: KindPolicyDecide, Actor: "u", Action: "a", PolicySnapshot: json.RawMessage("nope")},
		"control in outcome": {Kind: KindPolicyDecide, Actor: "u", Action: "a", Outcome: "done\x00"},
	}
	for name, ev := range cases {
		err := validateEvent(ev)
		if err == nil {
			t.Errorf("%s: validateEvent accepted it", name)
			continue
		}
		if !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: kind = %v, want KindInvalidInput", name, err)
		}
	}
}

func TestAuditNewIDShape(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		id, err := newID(at)
		if err != nil {
			t.Fatalf("newID: %v", err)
		}
		if len(id) != 26 {
			t.Fatalf("id %q is %d characters, want 26", id, len(id))
		}
		if i := strings.IndexFunc(id, func(r rune) bool { return !strings.ContainsRune(crockford, r) }); i >= 0 {
			t.Fatalf("id %q has a non-Crockford character at offset %d", id, i)
		}
		if seen[id] {
			t.Fatalf("newID returned %q twice for the same instant", id)
		}
		seen[id] = true
	}
}

func TestAuditHashParamsIsDeterministic(t *testing.T) {
	a := HashParams([]byte("token=hunter2"))
	if a != HashParams([]byte("token=hunter2")) {
		t.Fatal("HashParams is not deterministic")
	}
	if a == HashParams([]byte("token=hunter3")) {
		t.Fatal("HashParams collided on different inputs")
	}
	if strings.Contains(a, "hunter2") {
		t.Fatalf("HashParams leaked its input: %q", a)
	}
}

func TestAuditRecordTime(t *testing.T) {
	rec := Record{TSUnixNano: time.Unix(1_700_000_000, 500).UnixNano()}
	if got := rec.Time(); got.UnixNano() != rec.TSUnixNano {
		t.Fatalf("Time() = %v, want the recorded instant", got)
	}
}
