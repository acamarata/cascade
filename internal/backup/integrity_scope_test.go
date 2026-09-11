// Purpose: pins the scope decision VerifyIntegrity's and GateReport's doc
//
//	comments make explicit — object-level re-decryption covers the id
//	argument's OWN manifest entries, never an ancestor's — split into its
//	own file so integrity_test.go stays under Art.10.3's 300-line cap.
//
// SPORT: internal.backup.integrity/ADDED (P1-E19-W4-S41-T4).

package backup

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/testkit"
)

// distinctChainedSnapshots is chainedSnapshots's shape (integrity_test.go)
// but changes the captured row's VALUE between the two CreateSnapshot
// calls, so first and second's export bytes differ by DATA —
// deterministically, not only via ExportHeader.ExportedAt's wall-clock
// race, which can coincide within the same RFC3339 second and let two
// otherwise-identical captures dedup to ONE shared object (observed
// directly: a naive version of the scope-pin test below failed
// intermittently on exactly this collision). Used only here, which needs
// a real object guaranteed to belong to the ancestor and NOT to the
// target.
func distinctChainedSnapshots(t *testing.T) (target *memTarget, pubKey ed25519.PublicKey, first, second Manifest) {
	t.Helper()
	ctx := context.Background()
	pub := setSigningKeyEnv(t)
	identity, recipient := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	target = newMemTarget()
	db := openCaptureTestDB(t)
	seedCaptureRow(t, db, "context", "k1", []byte("ancestor-only payload"))

	deps := CreateSnapshotDeps{
		Target: target, AgeRecipient: recipient,
		Clock: testkit.NewFrozenClock(time.Unix(1_700_000_300, 0)),
		Domains: map[string]Exporter{
			"context": SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()},
		},
	}
	first, err := CreateSnapshot(ctx, "scope-proof-1", deps, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot (first): %v", err)
	}

	seedCaptureRow(t, db, "context", "k1", []byte("target-only payload, deliberately different from the ancestor's"))
	deps.Domains = map[string]Exporter{
		"context": SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()},
	}
	second, err = CreateSnapshot(ctx, "scope-proof-2", deps, &first)
	if err != nil {
		t.Fatalf("CreateSnapshot (second): %v", err)
	}
	return target, pub, first, second
}

// TestIntegrityGate_ObjectScopeIsTargetSnapshotOnly pins the deliberate
// scope decision documented on VerifyIntegrity and GateReport: object-level
// re-decryption covers the id argument's OWN manifest entries, never an
// ancestor's. It corrupts a real object that belongs ONLY to the first
// (ancestor) snapshot — guaranteed disjoint from second's own objects by
// distinctChainedSnapshots's differing row content, checked below rather
// than merely assumed — and asserts VerifyIntegrity(second) still PASSES,
// exactly as documented. The same corrupted object IS still caught when it
// is the id under test: VerifyIntegrity(first) over the identical
// corruption refuses. If a future change extends object verification to
// the whole chain, this test's first assertion fails, forcing that change
// to be deliberate and documented rather than accidental scope creep.
func TestIntegrityGate_ObjectScopeIsTargetSnapshotOnly(t *testing.T) {
	target, pub, first, second := distinctChainedSnapshots(t)
	ctx := context.Background()
	opts := GateOptions{Target: target, PubKey: pub}

	ancestorOnlyKey := manifestObjectKey(t, first)
	for _, entry := range second.Entries {
		for _, ref := range entry.Refs {
			objRef, err := manifestRefToObjectRef(ref)
			if err != nil {
				t.Fatalf("manifestRefToObjectRef(%q): %v", ref.Hash, err)
			}
			if ObjectKey(objRef.Hash) == ancestorOnlyKey {
				t.Fatal("fixture assumption violated: first and second share an object despite distinct row content; scope test cannot proceed")
			}
		}
	}

	target.corrupt(ancestorOnlyKey)

	if _, _, err := VerifyIntegrity(ctx, opts, second.Snapshot); err != nil {
		t.Fatalf("VerifyIntegrity(second) after corrupting an object only the ancestor references = %v, want nil (documented scope: target snapshot's own objects only)", err)
	}
	if _, _, err := VerifyIntegrity(ctx, opts, first.Snapshot); err == nil {
		t.Fatal("VerifyIntegrity(first) over its own corrupted object = nil error, want a refusal")
	}
}
