// Purpose: TargetPolicy persistence + validation (cron spec parsed through
// C/S-04.T4's own scheduler.ParseSpec, non-empty domain set) and Outcome
// bookkeeping (append-only, chronologically sorted per target).
// SPORT: internal.backup.policy/ADD (tests) (P1-E19-W4-S42-T1).
package backup

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

const testPolicyNamespace = "backup-policy-test"

func TestTargetPolicy_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	pol := TargetPolicy{Target: "nas", CronSpec: "@every 6h", Domains: []string{"context", "memory"}}
	if err := PutPolicy(ctx, store, testPolicyNamespace, pol); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	got, err := GetPolicy(ctx, store, testPolicyNamespace, "nas")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if got.CronSpec != pol.CronSpec || len(got.Domains) != 2 {
		t.Fatalf("GetPolicy round-trip = %+v, want %+v", got, pol)
	}
	list, err := ListPolicies(ctx, store, testPolicyNamespace)
	if err != nil {
		t.Fatalf("ListPolicies: %v", err)
	}
	if len(list) != 1 || list[0].Target != "nas" {
		t.Fatalf("ListPolicies = %+v, want exactly [nas]", list)
	}
}

func TestTargetPolicy_Validate_BadCronSpec(t *testing.T) {
	pol := TargetPolicy{Target: "nas", CronSpec: "not a cron spec", Domains: []string{"context"}}
	if err := pol.Validate(); err == nil {
		t.Fatal("Validate(bad cron spec) = nil, want an error")
	}
}

func TestTargetPolicy_Validate_NoDomains(t *testing.T) {
	pol := TargetPolicy{Target: "nas", CronSpec: "@every 6h"}
	if err := pol.Validate(); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Validate(no domains) = %v, want KindInvalidInput", err)
	}
}

// TestTargetPolicy_Validate_VerifyCronSpec proves S-42.T4's addition: an
// empty VerifyCronSpec (the DefaultVerifyCronSpec case) passes, a valid
// explicit one passes, and an unparseable one fails closed through the
// identical scheduler.ParseSpec CronSpec already uses.
func TestTargetPolicy_Validate_VerifyCronSpec(t *testing.T) {
	base := TargetPolicy{Target: "nas", CronSpec: "@every 6h", Domains: []string{"context"}}
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate(empty VerifyCronSpec) = %v, want nil", err)
	}
	base.VerifyCronSpec = "@every 24h"
	if err := base.Validate(); err != nil {
		t.Fatalf("Validate(valid VerifyCronSpec) = %v, want nil", err)
	}
	base.VerifyCronSpec = "not a cron spec"
	if err := base.Validate(); err == nil {
		t.Fatal("Validate(bad VerifyCronSpec) = nil, want an error")
	}
}

// TestTargetPolicy_RoundTrip_VerifyFields proves VerifyCronSpec/
// VerifyDisabled persist and round-trip through PutPolicy/GetPolicy
// exactly like every other field.
func TestTargetPolicy_RoundTrip_VerifyFields(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	pol := TargetPolicy{
		Target: "nas-verify", CronSpec: "@every 6h", Domains: []string{"context"},
		VerifyCronSpec: "@every 12h", VerifyDisabled: true,
	}
	if err := PutPolicy(ctx, store, testPolicyNamespace, pol); err != nil {
		t.Fatalf("PutPolicy: %v", err)
	}
	got, err := GetPolicy(ctx, store, testPolicyNamespace, "nas-verify")
	if err != nil {
		t.Fatalf("GetPolicy: %v", err)
	}
	if got.VerifyCronSpec != "@every 12h" || !got.VerifyDisabled {
		t.Fatalf("GetPolicy round-trip = %+v, want VerifyCronSpec=@every 12h VerifyDisabled=true", got)
	}
}

func TestOutcome_RecordAndList_ChronologicalOrder(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	base := time.Unix(1_700_000_000, 0).UTC()

	if err := RecordOutcome(ctx, store, testPolicyNamespace, Outcome{
		Target: "nas", When: base.Add(2 * time.Hour), Success: true, Snapshot: "snap-2",
	}); err != nil {
		t.Fatalf("RecordOutcome (2nd chronologically): %v", err)
	}
	if err := RecordOutcome(ctx, store, testPolicyNamespace, Outcome{
		Target: "nas", When: base, Success: false, ErrorText: "elevation required",
	}); err != nil {
		t.Fatalf("RecordOutcome (1st chronologically): %v", err)
	}
	// A different target's outcome must never appear in "nas"'s listing.
	if err := RecordOutcome(ctx, store, testPolicyNamespace, Outcome{
		Target: "b2", When: base, Success: true, Snapshot: "other-target",
	}); err != nil {
		t.Fatalf("RecordOutcome (other target): %v", err)
	}

	outs, err := ListOutcomes(ctx, store, testPolicyNamespace, "nas")
	if err != nil {
		t.Fatalf("ListOutcomes: %v", err)
	}
	if len(outs) != 2 {
		t.Fatalf("ListOutcomes(nas) = %+v, want exactly 2", outs)
	}
	if outs[0].Success || outs[0].ErrorText == "" {
		t.Fatalf("ListOutcomes[0] = %+v, want the failed (elevation-required) fire first", outs[0])
	}
	if !outs[1].Success || outs[1].Snapshot != "snap-2" {
		t.Fatalf("ListOutcomes[1] = %+v, want the successful fire second", outs[1])
	}
}

func TestGetPolicy_NotFound(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if _, err := GetPolicy(ctx, store, testPolicyNamespace, "absent"); !cascade.HasKind(err, cascade.KindNotFound) {
		t.Fatalf("GetPolicy(absent) = %v, want KindNotFound", err)
	}
}

func TestRecordOutcome_RequiresTargetAndWhen(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := RecordOutcome(ctx, store, testPolicyNamespace, Outcome{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("RecordOutcome(empty) = %v, want KindInvalidInput", err)
	}
}
