// Purpose: RunVerification's fail-closed detection proof (RED on a real
// corrupted stored object, GREEN on a real well-formed one -- never a
// digest the artifact itself supplied), the real attention-routing proof
// (a genuine *supervision.Store receives and retains the pushed item),
// the "does NOT fire on success" proof, the §22 VERIFIED-state Outcome
// proof, and the mutation proof that RegisterConfiguredVerificationJobs
// is actually reachable from the daemon's real composition root.
// SPORT: internal.backup.verify/ADD (tests) (P1-E19-W4-S42-T4).
package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/provider"
)

// var _ assertion: *supervision.Store really does satisfy AttentionSink --
// no package-local reimplementation of Push.
var _ AttentionSink = (*supervision.Store)(nil)

// verifyFixture builds one real, on-disk fs-target snapshot (real
// CreateSnapshot, real age/ed25519 crypto, real files under t.TempDir())
// and returns the TargetRecord, the created Manifest, and the object's
// absolute on-disk path (for RED's direct filesystem corruption).
func verifyFixture(t *testing.T) (record TargetRecord, manifest Manifest, objectPath string) {
	t.Helper()
	ctx := context.Background()
	setSigningKeyEnv(t)
	identity, recipient := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)

	root := t.TempDir()
	record = TargetRecord{Name: "primary", Kind: TargetKindFS, FSRoot: root}
	driver, err := BuildTarget(ctx, record, nil, func(string) string { return "" })
	if err != nil {
		t.Fatalf("BuildTarget: %v", err)
	}
	db := openCaptureTestDB(t)
	seedCaptureRow(t, db, "ns", "k1", []byte("real verification-fixture payload"))
	deps := CreateSnapshotDeps{
		Target: driver, AgeRecipient: recipient,
		Clock: testkit.NewFrozenClock(time.Unix(1_700_000_200, 0)),
		Domains: map[string]Exporter{
			"context": SQLiteCapture{DB: db, Domain: storage.DomainContext, Dir: t.TempDir()},
		},
	}
	manifest, err = CreateSnapshot(ctx, "proof-1", deps, nil)
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	key := manifestObjectKey(t, manifest)
	objectPath = filepath.Join(root, filepath.FromSlash(key))
	return record, manifest, objectPath
}

// fakeAttentionSink counts pushes and records what it received, for the
// "does not fire on success" negative proof (RunVerification_GreenPath
// below) where a real Store is unnecessary ceremony.
type fakeAttentionSink struct {
	pushed []supervision.AttentionItem
}

func (f *fakeAttentionSink) Push(_ context.Context, item supervision.AttentionItem) (supervision.AttentionItem, error) {
	item.ID = "fake-id"
	f.pushed = append(f.pushed, item)
	return item, nil
}

// TestRunVerification_DetectsRealCorruption is the RED half of the
// can-it-detect-corruption proof: a real fs object, corrupted on the real
// filesystem (never against a digest the artifact itself supplied -- the
// pubkey/manifest live independently of the tampered object), makes
// RunVerification fail, record a failing Outcome, and push exactly one
// real attention item with the correct structured payload.
func TestRunVerification_DetectsRealCorruption(t *testing.T) {
	ctx := context.Background()
	record, manifest, objectPath := verifyFixture(t)

	original, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%s): %v", objectPath, err)
	}
	corrupted := append([]byte{}, original...)
	corrupted[0] ^= 0xFF
	if err := os.WriteFile(objectPath, corrupted, 0o600); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_300, 0))
	bus := events.New(store, clock)
	attn := supervision.NewStore(store, clock, bus, supervision.NewSystemIDGenerator(), 0)

	report, err := RunVerification(ctx, store, "verify-test", record, nil, func(string) string { return "" }, clock, attn)
	if err == nil {
		t.Fatal("RunVerification over a corrupted object = nil error, want a failure")
	}
	if report.Verified {
		t.Fatal("report.Verified = true over a corrupted object")
	}
	if report.Snapshot != manifest.Snapshot {
		t.Fatalf("report.Snapshot = %q, want %q", report.Snapshot, manifest.Snapshot)
	}

	assertFailingVerifyOutcomeRecorded(ctx, t, store, record.Name)
	assertAttentionItemPushed(ctx, t, attn, record.Name, manifest.Snapshot)
}

// assertFailingVerifyOutcomeRecorded checks the exactly-one recorded
// Outcome for target is a failing, OutcomeKindVerify record. Split out of
// TestRunVerification_DetectsRealCorruption under Art.10.3's 50-line
// function cap (funlen counts test functions too).
func assertFailingVerifyOutcomeRecorded(ctx context.Context, t *testing.T, store provider.Store, target string) {
	t.Helper()
	outcomes, lerr := ListOutcomes(ctx, store, "verify-test", target)
	if lerr != nil {
		t.Fatalf("ListOutcomes: %v", lerr)
	}
	if len(outcomes) != 1 {
		t.Fatalf("ListOutcomes returned %d outcomes, want 1", len(outcomes))
	}
	if outcomes[0].Success {
		t.Fatal("recorded Outcome.Success = true for a corrupted verification")
	}
	if outcomes[0].OutcomeEffectiveKind() != OutcomeKindVerify {
		t.Fatalf("recorded Outcome.Kind = %q, want %q", outcomes[0].Kind, OutcomeKindVerify)
	}
}

// assertAttentionItemPushed checks the real *supervision.Store holds
// exactly one KindError item scoped to target, decoding as a complete
// VerificationFailurePayload naming snapshot.
func assertAttentionItemPushed(ctx context.Context, t *testing.T, attn *supervision.Store, target string, snapshot SnapshotID) {
	t.Helper()
	items, ierr := attn.ListInScopes(ctx, []supervision.ScopeRef{{Kind: scope.ScopeKindGlobal, ID: target}}, supervision.Filter{})
	if ierr != nil {
		t.Fatalf("ListInScopes: %v", ierr)
	}
	if len(items) != 1 {
		t.Fatalf("attention queue holds %d item(s) for target %q, want exactly 1", len(items), target)
	}
	if items[0].Kind != supervision.KindError {
		t.Fatalf("pushed item Kind = %q, want %q", items[0].Kind, supervision.KindError)
	}
	var payload VerificationFailurePayload
	if derr := json.Unmarshal([]byte(items[0].SourceRef), &payload); derr != nil {
		t.Fatalf("attention item SourceRef does not decode as VerificationFailurePayload: %v (%s)", derr, items[0].SourceRef)
	}
	if payload.Target != target || payload.SnapshotID != string(snapshot) || payload.FailureReason == "" || payload.JournalRef == "" {
		t.Fatalf("attention item payload incomplete: %+v", payload)
	}
}

// TestRunVerification_GreenPath is the GREEN half of the pair: the
// IDENTICAL fixture, unmodified, verifies successfully, records a
// successful (§22 VERIFIED) Outcome with real chunk counts, and never
// pushes an attention item.
func TestRunVerification_GreenPath(t *testing.T) {
	ctx := context.Background()
	record, manifest, _ := verifyFixture(t)

	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_300, 0))
	sink := &fakeAttentionSink{}

	report, err := RunVerification(ctx, store, "verify-test", record, nil, func(string) string { return "" }, clock, sink)
	if err != nil {
		t.Fatalf("RunVerification over a well-formed snapshot: %v", err)
	}
	if !report.Verified {
		t.Fatal("report.Verified = false over a well-formed snapshot")
	}
	if report.CheckedChunks == 0 {
		t.Fatal("report.CheckedChunks = 0 over a snapshot with real object content")
	}
	if report.Snapshot != manifest.Snapshot {
		t.Fatalf("report.Snapshot = %q, want %q", report.Snapshot, manifest.Snapshot)
	}
	if len(sink.pushed) != 0 {
		t.Fatalf("a successful verification pushed %d attention item(s), want 0", len(sink.pushed))
	}

	outcomes, lerr := ListOutcomes(ctx, store, "verify-test", record.Name)
	if lerr != nil {
		t.Fatalf("ListOutcomes: %v", lerr)
	}
	if len(outcomes) != 1 || !outcomes[0].Success || outcomes[0].OutcomeEffectiveKind() != OutcomeKindVerify {
		t.Fatalf("ListOutcomes = %+v, want exactly one successful verify Outcome", outcomes)
	}
	if outcomes[0].CheckedChunks != report.CheckedChunks {
		t.Fatalf("recorded Outcome.CheckedChunks = %d, want %d", outcomes[0].CheckedChunks, report.CheckedChunks)
	}
}

// TestRunVerification_NoSnapshotYetIsNotAFailure proves a target with no
// snapshot at all is a benign no-op, not a corruption report: no Outcome,
// no attention push, no error.
func TestRunVerification_NoSnapshotYetIsNotAFailure(t *testing.T) {
	ctx := context.Background()
	setSigningKeyEnv(t)
	identity, _ := newTestAgeKeypair(t)
	t.Setenv(AgeIdentityEnvVar, identity)
	record := TargetRecord{Name: "empty", Kind: TargetKindFS, FSRoot: t.TempDir()}
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_300, 0))
	sink := &fakeAttentionSink{}

	report, err := RunVerification(ctx, store, "verify-test", record, nil, func(string) string { return "" }, clock, sink)
	if err != nil {
		t.Fatalf("RunVerification over an empty target: %v", err)
	}
	if report.Verified {
		t.Fatal("report.Verified = true over an empty target")
	}
	if len(sink.pushed) != 0 {
		t.Fatalf("an empty target pushed %d attention item(s), want 0", len(sink.pushed))
	}
	outcomes, lerr := ListOutcomes(ctx, store, "verify-test", record.Name)
	if lerr != nil {
		t.Fatalf("ListOutcomes: %v", lerr)
	}
	if len(outcomes) != 0 {
		t.Fatalf("ListOutcomes = %d records over an empty target, want 0", len(outcomes))
	}
}

// The remaining RegisterVerificationJob/RegisterConfiguredVerificationJobs,
// outcomeIndex, EffectiveVerifyCronSpec, and decodeManifestPubKey tests
// live in verify_schedule_test.go (split under the 300-line file cap).
