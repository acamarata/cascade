// Purpose: RegisterVerificationJob/RegisterConfiguredVerificationJobs
// scheduler-registration tests, mcp.go's outcomeIndex "verified" status
// distinction, EffectiveVerifyCronSpec's default, and
// decodeManifestPubKey's refusal/success shapes. Split from verify_test.go
// under the repo-wide 300-line-per-file cap (R-14.117) -- no behavior
// lives here that verify.go's own contract does not already describe.
// SPORT: internal.backup.verify/ADD (tests) (P1-E19-W4-S42-T4).
package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/events/scheduler"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestRegisterVerificationJob_HonorsVerifyDisabled proves the per-target
// toggle: a policy with VerifyDisabled true registers no scheduler job at
// all (RegisterRunnable is never called), while an otherwise-identical
// enabled policy does.
func TestRegisterVerificationJob_HonorsVerifyDisabled(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	clock := testkit.NewFrozenClock(time.Unix(1_700_000_400, 0))
	bus := events.New(store, clock)
	sched := scheduler.New(store, "verify-sched-test", clock, bus, "owner-verify", 1000*time.Hour)
	if err := sched.SetActionGate(&allowGate{}, testGateSubject(), "backup.verify.dispatch"); err != nil {
		t.Fatalf("SetActionGate: %v", err)
	}
	target := TargetRecord{Name: "toggled", Kind: TargetKindFS, FSRoot: t.TempDir()}
	disabled := TargetPolicy{Target: "toggled", CronSpec: "0 3 * * *", Domains: []string{"config"}, VerifyDisabled: true}
	if err := RegisterVerificationJob(ctx, sched, store, "verify-sched-test", target, disabled, clock, nil, func(string) string { return "" }, nil); err != nil {
		t.Fatalf("RegisterVerificationJob (disabled): %v", err)
	}
	if err := sched.RegisterRunnable(verifyJobOwner("toggled"), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("a disabled policy left the owner name unregistered, so a real Runnable can still claim it: %v", err)
	}
}

// TestOutcomeIndex_VerifiedDistinctFromSuccess proves mcp.go's
// outcomeIndex (backup list/cascade_backup_list's status column) reports
// "verified" -- not merely "success" -- once a snapshot's MOST RECENT
// outcome is a successful verification fire, and that OutcomeEffectiveKind
// really drives that distinction (a create-only history stays "success").
func TestOutcomeIndex_VerifiedDistinctFromSuccess(t *testing.T) {
	when := time.Unix(1_700_000_500, 0)
	createOnly := []Outcome{{Target: "t", Snapshot: "snap-1", When: when, Success: true}}
	if got := outcomeIndex(createOnly)["snap-1"]; got != "success" {
		t.Fatalf("create-only outcomeIndex = %q, want %q", got, "success")
	}
	verified := append(createOnly, Outcome{
		Target: "t", Snapshot: "snap-1", When: when.Add(time.Minute), Success: true, Kind: OutcomeKindVerify,
	})
	if got := outcomeIndex(verified)["snap-1"]; got != "verified" {
		t.Fatalf("create-then-verify outcomeIndex = %q, want %q", got, "verified")
	}
	failed := append(verified, Outcome{
		Target: "t", Snapshot: "snap-1", When: when.Add(2 * time.Minute), Success: false, Kind: OutcomeKindVerify,
	})
	if got := outcomeIndex(failed)["snap-1"]; got != "failed" {
		t.Fatalf("create-verify-then-failed-verify outcomeIndex = %q, want %q", got, "failed")
	}
}

// TestEffectiveVerifyCronSpec proves the policy-record default (24h,
// carried as DefaultVerifyCronSpec, never a config.toml key): an empty
// VerifyCronSpec resolves to it, a non-empty one is returned unchanged.
func TestEffectiveVerifyCronSpec(t *testing.T) {
	if got := (TargetPolicy{}).EffectiveVerifyCronSpec(); got != DefaultVerifyCronSpec {
		t.Fatalf("EffectiveVerifyCronSpec() over a zero-value policy = %q, want %q", got, DefaultVerifyCronSpec)
	}
	custom := TargetPolicy{VerifyCronSpec: "@every 6h"}
	if got := custom.EffectiveVerifyCronSpec(); got != "@every 6h" {
		t.Fatalf("EffectiveVerifyCronSpec() over an explicit spec = %q, want %q", got, "@every 6h")
	}
}

// TestRegisterConfiguredVerificationJobs mirrors
// TestRegisterConfiguredBackupJobs exactly (schedule_test.go): an orphan
// target (no matching policy) is reported skipped, never silently
// dropped, and the real Scheduler.Activate report shows the one
// registered verification job under its own "backup:verify:" owner.
func TestRegisterConfiguredVerificationJobs(t *testing.T) {
	ctx := context.Background()
	sched, clock, store := newTestScheduler(t)

	nas := TargetRecord{Name: "nas", Kind: TargetKindFS, FSRoot: t.TempDir()}
	if err := PutTarget(ctx, store, testScheduleNamespace, nas); err != nil {
		t.Fatalf("PutTarget(nas): %v", err)
	}
	nasPolicy := TargetPolicy{Target: "nas", CronSpec: "@every 6h", Domains: []string{"context"}}
	if err := PutPolicy(ctx, store, testScheduleNamespace, nasPolicy); err != nil {
		t.Fatalf("PutPolicy(nas): %v", err)
	}
	orphan := TargetRecord{Name: "orphan", Kind: TargetKindFS, FSRoot: t.TempDir()}
	if err := PutTarget(ctx, store, testScheduleNamespace, orphan); err != nil {
		t.Fatalf("PutTarget(orphan): %v", err)
	}

	skipped, err := RegisterConfiguredVerificationJobs(ctx, sched, store, testScheduleNamespace, clock, nil, nil, nil)
	if err != nil {
		t.Fatalf("RegisterConfiguredVerificationJobs: %v", err)
	}
	if len(skipped) != 1 || skipped[0] != "orphan" {
		t.Fatalf("RegisterConfiguredVerificationJobs skipped = %v, want exactly [orphan]", skipped)
	}

	report, err := sched.Activate(ctx)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if len(report.Scheduled) != 1 || report.Scheduled[0].Owner != verifyJobOwner("nas") {
		t.Fatalf("Activate report.Scheduled = %+v, want exactly the nas verify job", report.Scheduled)
	}
}

// TestRegisterVerificationJob_TargetPolicyMismatch proves the same
// fail-closed input check RegisterBackupJob has: a policy naming a
// different target than the record refuses before touching the scheduler.
func TestRegisterVerificationJob_TargetPolicyMismatch(t *testing.T) {
	ctx := context.Background()
	sched, clock, store := newTestScheduler(t)
	target := TargetRecord{Name: "a", Kind: TargetKindFS, FSRoot: t.TempDir()}
	mismatched := TargetPolicy{Target: "b", CronSpec: "@every 6h", Domains: []string{"context"}}
	if err := RegisterVerificationJob(ctx, sched, store, testScheduleNamespace, target, mismatched, clock, nil, nil, nil); err == nil {
		t.Fatal("RegisterVerificationJob with a mismatched policy target = nil error, want a refusal")
	}
}

// TestDecodeManifestPubKey covers the three real refusal/success shapes:
// not-yet-populated (empty, KindNotFound -- no snapshot created yet),
// malformed base64/wrong-length (KindIntegrity), and a genuine key.
func TestDecodeManifestPubKey(t *testing.T) {
	if _, err := decodeManifestPubKey(""); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("decodeManifestPubKey(\"\") = %v, want KindNotFound", err)
	}
	if _, err := decodeManifestPubKey("not-valid-base64!!"); err == nil {
		t.Fatal("decodeManifestPubKey(malformed) = nil error, want a refusal")
	}
	if _, err := decodeManifestPubKey("AAAA"); err == nil {
		t.Fatal("decodeManifestPubKey(wrong-length) = nil error, want a refusal")
	}
	pub := setSigningKeyEnv(t)
	encoded := base64.StdEncoding.EncodeToString(pub)
	got, err := decodeManifestPubKey(encoded)
	if err != nil {
		t.Fatalf("decodeManifestPubKey(valid): %v", err)
	}
	if !bytes.Equal(got, pub) {
		t.Fatalf("decodeManifestPubKey round-trip = %x, want %x", got, pub)
	}
}
