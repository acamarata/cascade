package jobs

// Purpose: EvidenceLedger.Append's audit-effect wrapping (P1-SEC-21): a
//
//	failing audit sink leaves no row, a repeated idempotency key writes no
//	new audit record, a lost acknowledgement is settled from the row probe,
//	and effect keys round-trip, stay injective and refuse non-canonical
//	spellings. The crash legs are in evidence_effect_crash_test.go.
//
// Constraints: stored state is asserted, never events; no sleeps.
// SPORT: jobs/completion-gate/ADD (tests) (P1-SEC-21).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

var effectsTestNow = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)

func evidenceRowCount(t *testing.T, store *Store) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + tableEvidence).Scan(&n); err != nil {
		t.Fatalf("count evidence rows: %v", err)
	}
	return n
}

func TestEvidenceAppendNoRowWithoutAuditRecord(t *testing.T) {
	l, store, w := newLedgerFixture(t, effectsTestNow)
	w.err = cascade.New(cascade.KindUnavailable, "injected audit failure")
	for _, idem := range []string{"idem-1", "idem-2"} {
		_, err := l.Append(context.Background(), passRecord(EvidenceBuild, idem), baseAuth())
		if !cascade.HasKind(err, cascade.KindUnavailable) {
			t.Fatalf("Append(%s) with a failing audit sink = %v, want the sink's error", idem, err)
		}
	}
	if n := evidenceRowCount(t, store); n != 0 {
		t.Fatalf("evidence rows = %d, want 0 when BeginEffect fails", n)
	}
	if len(w.confirmed)+len(w.failed) != 0 {
		t.Fatalf("settled %d/%d handles for effects that never began", len(w.confirmed), len(w.failed))
	}
}

func TestEvidenceAppendConfirmsEveryRow(t *testing.T) {
	l, store, w := newLedgerFixture(t, effectsTestNow)
	rec, err := l.Append(context.Background(), passRecord(EvidenceBuild, "idem-1"), baseAuth())
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if want := evidenceEffectKey("job-1", "idem-1", 0); want != "jobs.evidence:job-1:idem-1" ||
		len(w.calls) != 1 || w.calls[0].Key != want || len(w.confirmed) != 1 || w.confirmed[0].Key != want {
		t.Fatalf("effect calls = %+v confirmed = %+v, want one intent and one confirm for %q", w.calls, w.confirmed, want)
	}
	if w.calls[0].ParamsHash == "" {
		t.Fatal("the intent carries no params hash binding it to the row's content")
	}
	if rec.Seq != 1 || evidenceRowCount(t, store) != 1 || len(w.failed) != 0 {
		t.Fatalf("seq = %d rows = %d failed = %d, want 1, 1, 0", rec.Seq, evidenceRowCount(t, store), len(w.failed))
	}
}

func TestEvidenceNoOpAppendsNoAudit(t *testing.T) {
	l, store, w := newLedgerFixture(t, effectsTestNow)
	ctx := context.Background()
	first, err := l.Append(ctx, passRecord(EvidenceBuild, "idem-1"), baseAuth())
	if err != nil {
		t.Fatalf("first Append: %v", err)
	}
	again, err := l.Append(ctx, passRecord(EvidenceBuild, "idem-1"), baseAuth())
	if err != nil {
		t.Fatalf("repeated Append: %v", err)
	}
	if again.Seq != first.Seq || evidenceRowCount(t, store) != 1 {
		t.Fatalf("repeat returned seq %d (first %d), rows = %d; want the existing row", again.Seq, first.Seq, evidenceRowCount(t, store))
	}
	if len(w.calls) != 1 || len(w.confirmed) != 1 || len(w.failed) != 0 {
		t.Fatalf("audit calls = %d confirmed = %d failed = %d after a repeat, want 1, 1, 0",
			len(w.calls), len(w.confirmed), len(w.failed))
	}
}

func TestEvidenceAppendRowFailureFailsTheIntent(t *testing.T) {
	l, store, w := newLedgerFixture(t, effectsTestNow)
	if _, err := store.db.Exec(`CREATE TRIGGER refuse_evidence BEFORE INSERT ON ` + tableEvidence +
		` BEGIN SELECT RAISE(ABORT, 'injected insert failure'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if _, err := l.Append(context.Background(), passRecord(EvidenceBuild, "idem-1"), baseAuth()); err == nil {
		t.Fatal("Append over a refusing table = nil error")
	}
	if len(w.calls) != 1 || len(w.failed) != 1 || len(w.confirmed) != 0 || evidenceRowCount(t, store) != 0 {
		t.Fatalf("calls = %d failed = %d confirmed = %d rows = %d, want 1, 1, 0, 0",
			len(w.calls), len(w.failed), len(w.confirmed), evidenceRowCount(t, store))
	}
	if _, err := store.db.Exec(`DROP TRIGGER refuse_evidence`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	// The failed key stays begun for good, so the retry uses the next attempt key.
	rec, err := l.Append(context.Background(), passRecord(EvidenceBuild, "idem-1"), baseAuth())
	if err != nil || rec.Seq != 1 {
		t.Fatalf("retry after a failed attempt = %+v, %v; want seq 1", rec, err)
	}
	if got := w.calls[1].Key; got != "jobs.evidence:job-1:idem-1:1" {
		t.Fatalf("retry key = %q, want the :1 attempt key", got)
	}
}

func TestEvidenceEffectKeyRoundTrip(t *testing.T) {
	long := strings.Repeat("k", 200)
	ctx := context.Background()
	el, err := audit.NewEffectLog(audit.New(storetest.NewMemStore(), runtime.NewFixedClock(effectsTestNow), nil))
	if err != nil {
		t.Fatalf("NewEffectLog: %v", err)
	}
	for _, tc := range []struct{ job, idem string }{
		{"job-1", "idem-1"}, {"job:1", "a/b c_d:e"}, {"j", "ünï\x00"}, {"job-1", long},
	} {
		for attempt := 0; attempt < 3; attempt++ {
			key := evidenceEffectKey(tc.job, tc.idem, attempt)
			if _, err := el.BeginEffect(ctx, audit.EffectRequest{Kind: audit.KindPolicyDecide, Actor: "jobs.evidence",
				Action: "evidence.append:build", Key: key}); err != nil {
				t.Fatalf("the effect log refused key %q (%d bytes): %v", key, len(key), err)
			}
			got, ok := parseEvidenceEffectKey(key)
			if !ok || (got.hash != "") != (len(tc.idem) > 100) {
				t.Fatalf("parse(%q) = %+v, %v", key, got, ok)
			}
			if got.hash == "" && (got.job != tc.job || got.idem != tc.idem) {
				t.Fatalf("parse(%q) = %+v, want %q/%q", key, got, tc.job, tc.idem)
			}
		}
	}
	for _, bad := range []string{"jobs.evidence:onlyjob", "jobs.evidence:a:b:0", "jobs.evidence:a:b:x", "jobs.evidence:_zz:b", "other:a:b", "jobs.evidence-h:"} {
		if got, ok := parseEvidenceEffectKey(bad); ok {
			t.Errorf("parse(%q) = %+v, want it refused", bad, got)
		}
	}
}

func TestRecoverEffectsLeavesForeignAndSettlesHashedKeys(t *testing.T) {
	l, _, w := newLedgerFixture(t, effectsTestNow)
	ctx := context.Background()
	long := strings.Repeat("k", 200)
	if _, err := l.Append(ctx, passRecord(EvidenceBuild, long), baseAuth()); err != nil {
		t.Fatalf("Append with a long key: %v", err)
	}
	hashedKey := w.calls[0].Key
	if !strings.HasPrefix(hashedKey, evidenceEffectHashPrefix) {
		t.Fatalf("long idempotency key produced %q, want the hashed form", hashedKey)
	}
	w.confirmed = nil
	pending := []audit.EffectHandle{
		{Key: "approval.grant:x", IntentSeq: 1},
		{Key: hashedKey, IntentSeq: 2},
		{Key: evidenceEffectKey("job-1", "never-written", 0), IntentSeq: 3},
	}
	if err := l.RecoverEffects(ctx, pending); err != nil {
		t.Fatalf("RecoverEffects: %v", err)
	}
	if len(w.confirmed) != 1 || w.confirmed[0].Key != hashedKey || len(w.failed) != 1 || w.failed[0].IntentSeq != 3 {
		t.Fatalf("confirmed = %+v failed = %+v; want the hashed key confirmed, the unwritten one failed, the foreign one untouched",
			w.confirmed, w.failed)
	}
	bad := []audit.EffectHandle{{Key: "jobs.evidence:onlyjob", IntentSeq: 9}}
	if err := l.RecoverEffects(ctx, bad); !cascade.HasKind(err, cascade.KindIntegrity) {
		t.Fatalf("RecoverEffects with a malformed key = %v, want KindIntegrity", err)
	}
}

func TestEvidenceEffectKeyInjectiveAndCanonical(t *testing.T) {
	// A NUL byte must not let two distinct pairs share a hashed key.
	long := strings.Repeat("c", 150)
	a := evidenceEffectKey("a\x00b", long, 0)
	b := evidenceEffectKey("a", "b\x00"+long, 0)
	if a == b || !strings.HasPrefix(a, evidenceEffectHashPrefix) {
		t.Fatalf("keys %q and %q: want distinct hashed keys for distinct pairs", a, b)
	}
	if evidenceEffectKey("a:b", "c", 0) == evidenceEffectKey("a", "b:c", 0) {
		t.Fatal("job and key colons collide")
	}
	good := evidenceEffectKey("job-1", "idem-1", 0)
	hash := strings.TrimPrefix(a, evidenceEffectHashPrefix)
	for _, bad := range []string{
		"jobs.evidence:_6aob-1:idem-1",   // noncanonical: "_6a" decodes to "j", spelled as "j" by the minter
		"jobs.evidence:job_2d1:idem-1",   // "_2d" is "-", which the minter writes unescaped
		"jobs.evidence:job-1:idem-1:01",  // attempt written with a leading zero
		"jobs.evidence:job-1:idem-1:0",   // attempt 0 is never suffixed
		"jobs.evidence:job-1:idem-1:1:2", // two suffixes
		evidenceEffectHashPrefix + strings.ToUpper(hash),
		evidenceEffectHashPrefix + hash[:60],
		evidenceEffectHashPrefix + "zz" + hash[2:],
		"jobs.evidence:job-1:idem-1:16",               // beyond the last attempt the minter reaches
		"jobs.evidence:a:" + strings.Repeat("b", 112), // the minter spells this pair as a hash
	} {
		if got, ok := parseEvidenceEffectKey(bad); ok {
			t.Errorf("parse(%q) = %+v, want it refused", bad, got)
		}
	}
	if _, ok := parseEvidenceEffectKey(good); !ok {
		t.Fatalf("parse(%q) refused the minter's own key", good)
	}
}

func TestEvidenceInsertErrorProbesTheRowBeforeFailing(t *testing.T) {
	l, _, w := newLedgerFixture(t, effectsTestNow)
	ctx := context.Background()
	lost := cascade.New(cascade.KindUnavailable, "commit acknowledgement lost")

	// Row absent: the intent fails and the insert error is returned.
	rec := passRecord(EvidenceBuild, "idem-absent")
	h := audit.EffectHandle{Key: evidenceEffectKey("job-1", "idem-absent", 0), IntentSeq: 1}
	if _, err := l.settleFailedInsert(ctx, rec, h, lost); err != lost {
		t.Fatalf("settleFailedInsert with no row = %v, want the insert error", err)
	}
	if len(w.failed) != 1 || len(w.confirmed) != 0 {
		t.Fatalf("failed = %d confirmed = %d, want 1, 0 for an absent row", len(w.failed), len(w.confirmed))
	}

	// Row present (the commit persisted before the error): Confirmed, never Failed.
	if _, err := l.Append(ctx, passRecord(EvidenceBuild, "idem-present"), baseAuth()); err != nil {
		t.Fatalf("Append: %v", err)
	}
	w.failed, w.confirmed = nil, nil
	h2 := audit.EffectHandle{Key: evidenceEffectKey("job-1", "idem-present", 1), IntentSeq: 2}
	got, err := l.settleFailedInsert(ctx, passRecord(EvidenceBuild, "idem-present"), h2, lost)
	if err != nil || got.IdempotencyKey != "idem-present" {
		t.Fatalf("settleFailedInsert with the row present = %+v, %v; want the row and no error", got, err)
	}
	if len(w.confirmed) != 1 || len(w.failed) != 0 {
		t.Fatalf("failed = %d confirmed = %d, want 0, 1 for a persisted row", len(w.failed), len(w.confirmed))
	}
}

func TestEvidenceInsertErrorWithOtherAttemptsRowFailsTheIntent(t *testing.T) {
	l, _, w := newLedgerFixture(t, effectsTestNow)
	ctx := context.Background()
	winner := passRecord(EvidenceBuild, "idem-race")
	winner.ResultHash = "winner-result"
	if _, err := l.Append(ctx, winner, baseAuth()); err != nil {
		t.Fatalf("winner Append: %v", err)
	}
	w.failed, w.confirmed = nil, nil
	loser := passRecord(EvidenceBuild, "idem-race")
	loser.ResultHash = "loser-result"
	h := audit.EffectHandle{Key: evidenceEffectKey("job-1", "idem-race", 1), IntentSeq: 2}
	got, err := l.settleFailedInsert(ctx, loser, h, cascade.New(cascade.KindUnavailable, "primary key conflict"))
	if err != nil || got.ResultHash != "winner-result" {
		t.Fatalf("settleFailedInsert = %+v, %v; want the winner's row and no error", got, err)
	}
	if len(w.failed) != 1 || len(w.confirmed) != 0 {
		t.Fatalf("failed = %d confirmed = %d, want 1, 0: the loser must not confirm a row it did not write",
			len(w.failed), len(w.confirmed))
	}
}

func TestEvidenceInsertErrorWhenProbeFailsLeavesTheIntentOpen(t *testing.T) {
	l, store, w := newLedgerFixture(t, effectsTestNow)
	if err := store.db.Close(); err != nil {
		t.Fatalf("close jobs db: %v", err)
	}
	insertErr := cascade.New(cascade.KindUnavailable, "insert failed")
	h := audit.EffectHandle{Key: evidenceEffectKey("job-1", "idem-1", 0), IntentSeq: 1}
	_, err := l.settleFailedInsert(context.Background(), passRecord(EvidenceBuild, "idem-1"), h, insertErr)
	if err == nil || !strings.Contains(err.Error(), "insert failed") {
		t.Fatalf("settleFailedInsert with a dead probe = %v, want the joined insert error", err)
	}
	if len(w.failed) != 0 || len(w.confirmed) != 0 {
		t.Fatalf("failed = %d confirmed = %d, want the intent left open for RecoverEffects", len(w.failed), len(w.confirmed))
	}
}
