package runtime

// Purpose: config.accept_loosening can never record a [secrets] threshold the
// detector refuses: the encode that precedes every baseline write fails, so
// neither the snapshot, the audit row nor the record is written, and the
// event is not published.
// Inputs: an accept proof signed by a test key, an EffectiveConfig holding an
// invalid threshold.
// Outputs: none (tests). Constraints: internal test (it uses the signing
// fixtures of baseline_signed_test.go); no keychain.
// SPORT: runtime/boot-baseline-check tests (P1-SEC-24).

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestAcceptRefusesAnInvalidThresholdWithoutWriting(t *testing.T) {
	ctx := context.Background()
	for name, mutate := range map[string]func(*EffectiveConfig){
		"invalid confidence_threshold": func(e *EffectiveConfig) { v := invalidThreshold; e.Secrets.ConfidenceThreshold = &v },
		"invalid entropy_floor":        func(e *EffectiveConfig) { v := invalidThreshold; e.Secrets.EntropyFloor = &v },
	} {
		t.Run(name, func(t *testing.T) {
			signer, pub := newTestSigner(t)
			store := storetest.NewMemStore()
			tight := EffectiveConfig{Sync: SyncSection{Classes: map[string]string{}}}
			seedBaseline(t, store, ComputeSectionsHash(tight), tight)
			rec0, _ := store.Get(ctx, auditNamespace, baselineRecordKey)
			snap0, _ := store.Get(ctx, auditNamespace, baselineSnapshotKey)
			events := &fakeEventPublisher{}
			bc := NewBaselineChecker(store, NewFixedClock(fixedEpoch), events, NewStoreAuditRecorder(store, NewFixedClock(fixedEpoch)))
			bc.SetVerifier(trustedVerifier(pub))

			// Control: the same acceptance with a valid looser config is committed.
			if err := bc.Accept(ctx, looserThanFloor, signer.proof(t, fileDigestA)); err != nil {
				t.Fatalf("control accept: %v", err)
			}
			seedBaseline(t, store, ComputeSectionsHash(tight), tight) // back to the tight baseline
			events.events = nil

			bad := looserThanFloor
			mutate(&bad)
			err := bc.Accept(ctx, bad, signer.proof(t, fileDigestA))
			if err == nil {
				t.Fatal("Accept recorded a config holding an invalid threshold")
			}
			if !cascade.HasKind(err, cascade.KindInvalidInput) || !strings.Contains(err.Error(), "fix the value") {
				t.Fatalf("Accept error = %v, want invalid_input telling the operator to fix the value", err)
			}
			rec1, _ := store.Get(ctx, auditNamespace, baselineRecordKey)
			snap1, _ := store.Get(ctx, auditNamespace, baselineSnapshotKey)
			if !bytes.Equal(rec0, rec1) || !bytes.Equal(snap0, snap1) {
				t.Fatal("a refused acceptance changed the baseline record or snapshot")
			}
			if len(events.names()) != 0 {
				t.Fatalf("a refused acceptance published %v", events.names())
			}
			if res := bc.CheckFile(ctx, bad, signedAs(fileDigestA, bad)); res.Outcome == BaselineAccepted {
				t.Fatal("the next boot accepts the invalid config")
			}
		})
	}
}
