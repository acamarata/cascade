package audit

// Purpose: the schema half of the package's tests, the closed event-kind
//   enum asserted against the ratified list rather than against itself,
//   record sealing and hash verification, fail-closed event validation,
//   ULID shape, effect fields sealed in the hash, and a log written by
//   the pre-effect code (testdata/pre-effect-log, captured at f688c0b)
//   still verifying byte for byte.
// Constraints: Art.7.1 (nothing outside t.TempDir), Art.7.3 (no wall
//   clock in a test's expectations).
// SPORT: internal.audit.Record/ADDED (tests) (P1-E09-W2-S18-T2).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ratifiedKinds is the fourteen-value enum transcribed from R-21.235's own
// text ("the kind enum grows to fourteen (adds secrets.clipboard_write,
// secrets.quarantine_flush, vault.access)"), kept separate from AllKinds so
// the assertion below compares the code against the spec instead of against
// itself. That separation is what caught the shortfall: I/S-18.T2 shipped
// eleven and this transcription had been trimmed to match it. The
// fifteenth, effect.external, is P1-SEC-33's one amendment of the set.
var ratifiedKinds = []string{
	"policy.decide", "policy.route",
	"approval.enqueue", "approval.dedup", "approval.expire",
	"config.reload",
	"approval.grant", "approval.deny",
	"elevation.attempt", "elevation.grant", "elevation.deny",
	"secrets.clipboard_write", "secrets.quarantine_flush", "vault.access",
	"effect.external",
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

func TestEffectFieldsAreSealed(t *testing.T) {
	for field, edit := range map[string][2]string{
		"effect_phase": {`"effect_phase":"intent"`, `"effect_phase":"confirmed"`},
		"effect_key":   {`"effect_key":"evidence.commit:e-1"`, `"effect_key":"evidence.commit:e-2"`},
	} {
		t.Run(field, func(t *testing.T) {
			ctx := context.Background()
			store := storetest.NewMemStore()
			el, log := newEffectLog(t, store)
			if _, err := log.Append(ctx, sampleEvent(1)); err != nil {
				t.Fatalf("Append: %v", err)
			}
			h, err := el.BeginEffect(ctx, effectReq("evidence.commit:e-1"))
			if err != nil {
				t.Fatalf("BeginEffect: %v", err)
			}
			raw, err := store.Get(ctx, namespace, recordKey(h.IntentSeq))
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Contains(raw, []byte(edit[0])) {
				t.Fatalf("stored record does not persist %s: %s", edit[0], raw)
			}
			if err := store.Put(ctx, namespace, recordKey(h.IntentSeq),
				bytes.Replace(raw, []byte(edit[0]), []byte(edit[1]), 1)); err != nil {
				t.Fatalf("Put: %v", err)
			}
			err = log.Verify(ctx)
			requireSentinel(t, err, ErrTampered)
			if want := fmt.Sprintf("record %d (", h.IntentSeq); !strings.Contains(err.Error(), want) {
				t.Fatalf("Verify error %q does not name sequence %d", err, h.IntentSeq)
			}
			_, _, serr := el.EffectState(ctx, "evidence.commit:e-1")
			requireSentinel(t, serr, ErrTampered)
		})
	}
}

// preEffectFixture is testdata/pre-effect-log/log.json: the raw audit
// namespace rows a log written by the f688c0b code left behind.
type preEffectFixture struct {
	Namespace string            `json:"namespace"`
	Rows      map[string]string `json:"rows"`
}

func TestPreEffectRecordsVerify(t *testing.T) {
	ctx := context.Background()
	data, err := os.ReadFile(filepath.Join("testdata", "pre-effect-log", "log.json"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var fx preEffectFixture
	if err := json.Unmarshal(data, &fx); err != nil || fx.Namespace != namespace {
		t.Fatalf("fixture: namespace %q, %v", fx.Namespace, err)
	}
	store := storetest.NewMemStore()
	records := 0
	for k, v := range fx.Rows {
		if err := store.Put(ctx, fx.Namespace, k, []byte(v)); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
		if !strings.HasPrefix(k, recordPrefix) {
			continue
		}
		records++
		rec, derr := decodeRecord([]byte(v))
		if derr != nil {
			t.Fatalf("fixture record %s no longer verifies: %v", k, derr)
		}
		again, merr := json.Marshal(rec)
		if merr != nil || !bytes.Equal(again, []byte(v)) || rec.EffectKey != "" || rec.EffectPhase != "" {
			t.Fatalf("fixture record %s re-encodes as %s (%v), want its stored bytes unchanged", k, again, merr)
		}
	}
	if records != 5 {
		t.Fatalf("fixture holds %d records, want 5", records)
	}
	log := New(store, testkit.NewFrozenClock(testInstant), nil)
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("Verify over the pre-effect log: %v", err)
	}
	if rec, err := log.Append(ctx, sampleEvent(6)); err != nil || rec.Seq != 6 {
		t.Fatalf("Append after the fixture = seq %d, %v; want 6", rec.Seq, err)
	}
	if err := log.Verify(ctx); err != nil {
		t.Fatalf("Verify after appending to the pre-effect log: %v", err)
	}
}

func TestEffectRecordShapeRefused(t *testing.T) {
	base := Event{Kind: KindExternalEffect, Actor: "agent:test", Action: "x"}
	for _, bad := range [][2]string{{"k:1", ""}, {"", "intent"}, {"k:1", "done"}, {"k/1", "intent"}} {
		ev := base
		ev.EffectKey, ev.EffectPhase = bad[0], bad[1]
		requireSentinel(t, validateEvent(ev), ErrInvalidEvent)
	}
	intent := Record{Seq: 1, Event: Event{EffectKey: "k:1", EffectPhase: "intent"}}
	confirmed := Record{Seq: 2, Event: Event{EffectKey: "k:1", EffectPhase: "confirmed"}}
	for name, recs := range map[string][]Record{
		"two intents":          {intent, {Seq: 2, Event: intent.Event}},
		"terminal, no intent":  {confirmed},
		"two terminal records": {intent, confirmed, {Seq: 3, Event: confirmed.Event}},
	} {
		_, _, err := replayEffects(recs)
		if err == nil {
			t.Fatalf("replayEffects accepted %s", name)
		}
		requireSentinel(t, err, ErrTampered)
	}
}
