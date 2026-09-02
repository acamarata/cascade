package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
)

func newTestBaselineChecker(t *testing.T) (*BaselineChecker, *storetest.MemStore, *fakeEventPublisher) {
	t.Helper()
	store := storetest.NewMemStore()
	events := &fakeEventPublisher{}
	audit := NewStoreAuditRecorder(store, NewFixedClock(time.Unix(0, 0)))
	return NewBaselineChecker(store, NewFixedClock(time.Unix(0, 0)), events, audit), store, events
}

func TestBaselineChecker_MissingFailsClosed(t *testing.T) {
	bc, _, events := newTestBaselineChecker(t)
	looseConfig := EffectiveConfig{
		Elevation: elevationSection{AllowRemote: true, HelperPubkey: "k"},
		Sync:      SyncSection{Classes: map[string]string{"memory": "server-primary"}},
	}
	result := bc.Check(context.Background(), looseConfig)
	if result.Outcome != BaselineMissing {
		t.Fatalf("expected BaselineMissing, got %v", result.Outcome)
	}
	if result.Effective.Elevation.AllowRemote {
		t.Fatal("expected most-restrictive defaults (allow_remote=false), not the looser on-disk config")
	}
	found := false
	for _, n := range events.names() {
		if n == eventPolicyDivergent {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected config.policy.divergent event, got %v", events.names())
	}
}

func TestBaselineChecker_MatchIsOK(t *testing.T) {
	bc, store, _ := newTestBaselineChecker(t)
	cfg := EffectiveConfig{Elevation: elevationSection{AllowRemote: false}}
	hash := ComputeSectionsHash(cfg)
	seedBaseline(t, store, hash, cfg)

	result := bc.Check(context.Background(), cfg)
	if result.Outcome != BaselineOK {
		t.Fatalf("expected BaselineOK, got %v", result.Outcome)
	}
	if result.Effective.Elevation != cfg.Elevation {
		t.Fatal("expected the on-disk config to be used unchanged")
	}
}

func TestBaselineChecker_DivergentLooserFailsClosed(t *testing.T) {
	bc, store, events := newTestBaselineChecker(t)
	tight := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "local-only"}}}
	hash := ComputeSectionsHash(tight)
	seedBaseline(t, store, hash, tight)

	loose := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "server-primary"}}}
	result := bc.Check(context.Background(), loose)
	if result.Outcome != BaselineDivergent {
		t.Fatalf("expected BaselineDivergent, got %v", result.Outcome)
	}
	if result.ExpectedHash != hash {
		t.Fatalf("expected ExpectedHash=%s, got %s", hash, result.ExpectedHash)
	}
	if result.ActualHash != ComputeSectionsHash(loose) {
		t.Fatal("ActualHash mismatch")
	}
	if result.Effective.Sync.Classes["memory"] != "" {
		t.Fatal("expected most-restrictive defaults, not the looser on-disk config")
	}
	found := false
	for _, n := range events.names() {
		if n == eventPolicyDivergent {
			found = true
		}
	}
	if !found {
		t.Fatal("expected config.policy.divergent event")
	}
}

func TestBaselineChecker_TighterProceedsAndRewritesBaseline(t *testing.T) {
	bc, store, _ := newTestBaselineChecker(t)
	loose := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "server-primary"}}}
	hash := ComputeSectionsHash(loose)
	seedBaseline(t, store, hash, loose)

	tighter := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "local-only"}}}
	result := bc.Check(context.Background(), tighter)
	if result.Outcome != BaselineTightened {
		t.Fatalf("expected BaselineTightened, got %v", result.Outcome)
	}
	if result.Effective.Sync.Classes["memory"] != "local-only" {
		t.Fatal("expected the on-disk (tighter) config to be used, not defaults")
	}

	// Baseline record rewritten to the new hash.
	data, err := store.Get(context.Background(), auditNamespace, baselineRecordKey)
	if err != nil {
		t.Fatal(err)
	}
	var rec baselineRecordV1
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.SectionsHash != ComputeSectionsHash(tighter) {
		t.Fatalf("baseline not rewritten to the tighter hash: got %s", rec.SectionsHash)
	}
}

func TestBaselineChecker_MismatchWithoutSnapshotFailsClosed(t *testing.T) {
	// Legacy record with a hash but no adjacent snapshot (e.g. written by
	// a hypothetical pre-snapshot version): direction cannot be proven,
	// so this must fail closed even if the new config is, in fact,
	// tighter.
	bc, store, _ := newTestBaselineChecker(t)
	rec := baselineRecordV1{Version: 1, SectionsHash: "deadbeef", Sections: baselineGuardedSections}
	data, _ := json.Marshal(rec)
	if err := store.Put(context.Background(), auditNamespace, baselineRecordKey, data); err != nil {
		t.Fatal(err)
	}

	result := bc.Check(context.Background(), EffectiveConfig{})
	if result.Outcome != BaselineDivergent {
		t.Fatalf("expected BaselineDivergent (no snapshot to prove tightening), got %v", result.Outcome)
	}
}

func TestBaselineChecker_UnknownVersionToleratedWithWarning(t *testing.T) {
	bc, store, _ := newTestBaselineChecker(t)
	cfg := EffectiveConfig{Elevation: elevationSection{AllowRemote: false}}
	hash := ComputeSectionsHash(cfg)

	// Hand-built v2 record with an unknown "sig" field.
	raw := map[string]interface{}{
		"v":             2,
		"sections_hash": hash,
		"sections":      baselineGuardedSections,
		"sig":           "deadbeefdeadbeef",
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), auditNamespace, baselineRecordKey, data); err != nil {
		t.Fatal(err)
	}

	result := bc.Check(context.Background(), cfg)
	if result.Outcome != BaselineOK {
		t.Fatalf("expected BaselineOK (hash still matches), got %v", result.Outcome)
	}
	if result.VersionWarning == "" {
		t.Fatal("expected a non-empty VersionWarning for schema_version > 1")
	}
}

func TestComputeSectionsHash_Deterministic(t *testing.T) {
	cfg := EffectiveConfig{
		Policy:    PolicySection{AutonomyProfile: "locked"},
		Sync:      SyncSection{Classes: map[string]string{"b": "synced", "a": "local-only"}},
		Elevation: elevationSection{AllowRemote: false},
	}
	h1 := ComputeSectionsHash(cfg)
	h2 := ComputeSectionsHash(cfg)
	if h1 != h2 {
		t.Fatalf("hash not deterministic: %s != %s", h1, h2)
	}
	// Map iteration order must not affect the hash (sortedSyncClasses).
	cfg2 := cfg
	cfg2.Sync.Classes = map[string]string{"a": "local-only", "b": "synced"}
	if ComputeSectionsHash(cfg2) != h1 {
		t.Fatal("hash must be independent of map iteration order")
	}
}

func TestComputeSectionsHash_DifferentConfigsDifferentHash(t *testing.T) {
	a := EffectiveConfig{Policy: PolicySection{AutonomyProfile: "locked"}}
	b := EffectiveConfig{Policy: PolicySection{AutonomyProfile: "autonomous"}}
	if ComputeSectionsHash(a) == ComputeSectionsHash(b) {
		t.Fatal("expected different hashes for different configs")
	}
}

func TestMostRestrictiveDefaults_ElevationSafe(t *testing.T) {
	d := MostRestrictiveDefaults()
	if d.Elevation.AllowRemote {
		t.Fatal("expected allow_remote=false in most-restrictive defaults")
	}
}

func seedBaseline(t *testing.T, store *storetest.MemStore, hash string, cfg EffectiveConfig) {
	t.Helper()
	rec := baselineRecordV1{Version: 1, SectionsHash: hash, Sections: baselineGuardedSections}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), auditNamespace, baselineRecordKey, data); err != nil {
		t.Fatal(err)
	}
	snap, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), auditNamespace, baselineSnapshotKey, snap); err != nil {
		t.Fatal(err)
	}
}
