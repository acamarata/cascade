package runtime

// Purpose: proves the baseline record's state is read strictly (only a
// not-found read is a first boot; a corrupt or unreadable row refuses and is
// kept), that dropping a named [sync] domain is never a tightening, and
// that Accept commits the record last and only with an audit record.
// Inputs: in-memory stores wrapped to fail reads or writes; synthetic configs.
// Outputs: none (tests).
// Constraints: no keychain.
// SPORT: runtime/boot-baseline-check tests (P1-SEC-04).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestBaselineRemovedSyncNarrowingIsNotTightening(t *testing.T) {
	bc, store, _ := newTestBaselineChecker(t)
	narrowed := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "local-only"}}}
	seedBaseline(t, store, ComputeSectionsHash(narrowed), narrowed)
	dropped := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{}}}
	res := bc.CheckFile(context.Background(), dropped, BaselineFile{})
	if res.Outcome != BaselineDivergent {
		t.Fatalf("dropping memory=local-only: %s, want baseline_divergent (the domain would run its compiled class)", res.Outcome.Detail())
	}
	if res.Effective.Sync.Classes["memory"] != "local-only" {
		t.Fatalf("degraded memory class %q, want local-only pinned from the snapshot", res.Effective.Sync.Classes["memory"])
	}
	// Positive control: keeping the domain and tightening another still tightens.
	kept := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{"memory": "local-only", "blobs": "local-only"}}}
	if res := bc.CheckFile(context.Background(), kept, BaselineFile{}); res.Outcome != BaselineTightened {
		t.Fatalf("a real tightening: %s", res.Outcome.Detail())
	}
}

// readFailStore fails every Get with a non-not-found error and counts Puts.
type readFailStore struct {
	*storetest.MemStore
	puts int
}

func (s *readFailStore) Get(context.Context, string, string) ([]byte, error) {
	return nil, cascade.New(cascade.KindUnavailable, "synthetic read failure")
}

func (s *readFailStore) Put(ctx context.Context, ns, key string, v []byte) error {
	s.puts++
	return s.MemStore.Put(ctx, ns, key, v)
}

func TestBaselineCorruptRecordRefuses(t *testing.T) {
	floor := MostRestrictiveDefaults()
	bc, store, _ := newTestBaselineChecker(t)
	// A corrupt record degrades a looser file to the restrictive values.
	loose := EffectiveConfig{Policy: PolicySection{AutonomyProfile: "balanced"}, Sync: SyncSection{Classes: map[string]string{"memory": "server-primary"}},
		Conductor: ConductorSection{ExternalRoutingEnabled: true}}
	if err := store.Put(context.Background(), auditNamespace, baselineRecordKey, []byte("{")); err != nil {
		t.Fatal(err)
	}
	if res := bc.CheckFile(context.Background(), loose, BaselineFile{}); res.Outcome != BaselineCorrupt ||
		res.Effective.Policy.AutonomyProfile != "strict" || res.Effective.Sync.Classes["memory"] != "local-only" ||
		res.Effective.Conductor.ExternalRoutingEnabled {
		t.Fatalf("corrupt record, looser file: %s runs %+v, want strict/local-only/false", res.Outcome.Detail(), res.Effective)
	}
	junk := []byte("not a baseline record")
	if err := store.Put(context.Background(), auditNamespace, baselineRecordKey, junk); err != nil {
		t.Fatal(err)
	}
	res := bc.CheckFile(context.Background(), floor, BaselineFile{})
	if res.Outcome != BaselineCorrupt || !res.Outcome.Degraded() || res.Outcome.Detail() != "baseline_corrupt" {
		t.Fatalf("undecodable record: %s, want baseline_corrupt", res.Outcome.Detail())
	}
	if got, _ := store.Get(context.Background(), auditNamespace, baselineRecordKey); !bytes.Equal(got, junk) {
		t.Fatal("the corrupt record row was overwritten")
	}

	rf := &readFailStore{MemStore: storetest.NewMemStore()}
	bc2 := NewBaselineChecker(rf, NewFixedClock(fixedEpoch), nil, nil)
	if res := bc2.CheckFile(context.Background(), floor, BaselineFile{}); res.Outcome != BaselineCorrupt {
		t.Fatalf("unreadable record: %s, want baseline_corrupt", res.Outcome.Detail())
	}
	if rf.puts != 0 {
		t.Fatalf("an unreadable record led to %d writes", rf.puts)
	}
}

// failingAudit refuses every record.
type failingAudit struct{}

func (failingAudit) Record(context.Context, string, map[string]interface{}) error {
	return errors.New("synthetic audit failure")
}

func TestBaselineAcceptCommitsRecordLast(t *testing.T) {
	signer, pub := newTestSigner(t)
	tight := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{}}}
	for name, c := range map[string]struct {
		failAt int
		audit  func(*failingPutStore) AuditRecorder
	}{
		"snapshot write fails": {1, func(s *failingPutStore) AuditRecorder { return NewStoreAuditRecorder(s, NewFixedClock(fixedEpoch)) }},
		"audit write fails":    {2, func(s *failingPutStore) AuditRecorder { return NewStoreAuditRecorder(s, NewFixedClock(fixedEpoch)) }},
		"no audit recorder":    {0, func(*failingPutStore) AuditRecorder { return nil }},
		"audit refuses":        {0, func(*failingPutStore) AuditRecorder { return failingAudit{} }},
	} {
		store := &failingPutStore{MemStore: storetest.NewMemStore(), failAt: c.failAt}
		seedBaseline(t, store.MemStore, ComputeSectionsHash(tight), tight)
		old, _ := store.Get(context.Background(), auditNamespace, baselineRecordKey)
		bc := NewBaselineChecker(store, NewFixedClock(fixedEpoch), nil, c.audit(store))
		bc.SetVerifier(trustedVerifier(pub))
		if err := bc.Accept(context.Background(), looserThanFloor, signer.proof(t, fileDigestA)); err == nil {
			t.Fatalf("%s: Accept reported success", name)
		}
		if now, _ := store.Get(context.Background(), auditNamespace, baselineRecordKey); !bytes.Equal(now, old) {
			t.Fatalf("%s: the record changed; the old baseline must stay in force", name)
		}
		if res := bc.CheckFile(context.Background(), looserThanFloor, signedAs(fileDigestA, looserThanFloor)); res.Outcome == BaselineAccepted {
			t.Fatalf("%s: the next boot accepts the change", name)
		}
	}
}

func TestAuditRecordsSurviveAcrossBoots(t *testing.T) {
	signer, pub := newTestSigner(t)
	store := storetest.NewMemStore()
	clock := NewFixedClock(fixedEpoch)
	events := &fakeEventPublisher{}
	for i, digest := range []string{fileDigestA, fileDigestB} {
		// Each boot builds a fresh checker and recorder: the recorder's counter restarts at 1.
		bc := NewBaselineChecker(store, clock, events, NewStoreAuditRecorder(store, clock))
		bc.SetVerifier(trustedVerifier(pub))
		if err := bc.Accept(context.Background(), looserThanFloor, signer.proof(t, digest)); err != nil {
			t.Fatalf("boot %d accept: %v", i+1, err)
		}
		clock.Advance(time.Second)
	}
	it, err := store.Scan(context.Background(), auditNamespace, auditKindBaselineAccepted+"/")
	if err != nil {
		t.Fatal(err)
	}
	var digests []string
	for it.Next(context.Background()) {
		var rec auditRecord
		if err := json.Unmarshal(it.Value(), &rec); err != nil {
			t.Fatal(err)
		}
		digests = append(digests, fmt.Sprint(rec.Fields["config_sha256"]))
		if keys := fmt.Sprint(rec.Fields["restart_required_keys"]); keys != "[conductor.external_routing_enabled]" {
			t.Fatalf("audit restart_required_keys = %s", keys)
		}
	}
	if len(digests) != 2 || digests[0] != fileDigestA || digests[1] != fileDigestB {
		t.Fatalf("baseline_accepted records after two boots = %v, want both, in order", digests)
	}
	n := 0
	for _, name := range events.names() {
		if name == eventBaselineAccepted {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d %s events, want 2", n, eventBaselineAccepted)
	}
}

func TestLoosenedSinceBootUsesBootValues(t *testing.T) {
	bc, _, _ := newTestBaselineChecker(t)
	boot := looserThanFloor
	bc.boot = &boot
	// The process booted with these values: nothing is pending a restart.
	if got := bc.LoosenedSinceBoot(looserThanFloor); len(got) != 0 {
		t.Fatalf("accepting the boot values reports %v, want none", got)
	}
	// A value looser than the boot values is reported.
	looser := looserThanFloor
	looser.Conductor.SpillEnabled = true
	if got := fmt.Sprint(bc.LoosenedSinceBoot(looser)); got != "[conductor.spill_enabled]" {
		t.Fatalf("a loosening past the boot values reports %s, want [conductor.spill_enabled]", got)
	}
}
