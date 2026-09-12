package economics

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/fleet/topology"
)

// accountRolesGolden mirrors testdata/account_roles.golden.json's shape.
type accountRolesGolden struct {
	Roles []struct {
		Role                       string  `json:"role"`
		PreserveWeeklyReserve      float64 `json:"preserve_weekly_reserve"`
		ExecutiveModelFractionSoft float64 `json:"executive_model_fraction_soft"`
		ExecutiveModelFractionHard float64 `json:"executive_model_fraction_hard"`
	} `json:"roles"`
}

// TestRoleDefaults asserts RoleDefaults against the golden fixture
// hand-transcribed from R-21.34 (never captured from this package's own
// output) -- exact numbers for all three roles.
func TestRoleDefaults(t *testing.T) {
	raw, err := os.ReadFile("testdata/account_roles.golden.json")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var golden accountRolesGolden
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("unmarshal golden: %v", err)
	}
	if len(golden.Roles) != 3 {
		t.Fatalf("golden has %d roles, want 3", len(golden.Roles))
	}
	for _, want := range golden.Roles {
		got := RoleDefaults(AccountRole(want.Role))
		if got.PreserveWeeklyReserve != want.PreserveWeeklyReserve {
			t.Fatalf("role %q PreserveWeeklyReserve = %v, want %v", want.Role, got.PreserveWeeklyReserve, want.PreserveWeeklyReserve)
		}
		if got.ExecutiveModelFractionSoft != want.ExecutiveModelFractionSoft {
			t.Fatalf("role %q ExecutiveModelFractionSoft = %v, want %v", want.Role, got.ExecutiveModelFractionSoft, want.ExecutiveModelFractionSoft)
		}
		if got.ExecutiveModelFractionHard != want.ExecutiveModelFractionHard {
			t.Fatalf("role %q ExecutiveModelFractionHard = %v, want %v", want.Role, got.ExecutiveModelFractionHard, want.ExecutiveModelFractionHard)
		}
	}
}

// TestRoleFailClosed proves an unset or unrecognised role NEVER resolves
// to executive: it resolves to workforce and reports diverged=true.
func TestRoleFailClosed(t *testing.T) {
	cases := []string{"", "unknown-role", "EXECUTIVE", "admin"}
	for _, raw := range cases {
		role, diverged := ResolveAccountRole(raw)
		if role == topology.AccountRoleExecutive {
			t.Fatalf("raw %q resolved to executive, must never be permissive", raw)
		}
		if role != topology.AccountRoleWorkforce {
			t.Fatalf("raw %q resolved to %q, want workforce", raw, role)
		}
		if !diverged {
			t.Fatalf("raw %q: expected diverged=true", raw)
		}
	}
	// A genuinely valid role never diverges.
	role, diverged := ResolveAccountRole("specialist")
	if diverged || role != topology.AccountRoleSpecialist {
		t.Fatalf("valid role %q: got role=%q diverged=%v", "specialist", role, diverged)
	}
}

// TestClampFractionCaps: a published cap of 0.40 (Limit=40, source
// provider-status) clamps the configured 0.47 hard cap down to 0.40, and
// the soft cap (already below 0.40) is unaffected; a user-estimate bucket
// carries no real ceiling and never clamps.
func TestClampFractionCaps(t *testing.T) {
	base := RoleDefaults(topology.AccountRoleExecutive)

	published := topology.Bucket{Name: topology.DimensionWeeklyModelFraction, Limit: 40, Source: topology.SourceProviderStatus, Window: topology.BucketWindowWeek, Confidence: 1.0}
	clamped := ClampFractionCaps(base, published)
	if clamped.ExecutiveModelFractionHard != 0.40 {
		t.Fatalf("effective_hard = %v, want 0.40", clamped.ExecutiveModelFractionHard)
	}
	if clamped.ExecutiveModelFractionSoft != 0.35 {
		t.Fatalf("effective_soft = %v, want min(0.35,0.40)=0.35", clamped.ExecutiveModelFractionSoft)
	}

	estimate := topology.Bucket{Name: topology.DimensionWeeklyModelFraction, Limit: 10, Source: topology.SourceUserEstimate, Window: topology.BucketWindowWeek, Confidence: 0.4}
	unclamped := ClampFractionCaps(base, estimate)
	if unclamped.ExecutiveModelFractionHard != base.ExecutiveModelFractionHard {
		t.Fatalf("user-estimate bucket clamped hard cap to %v, want unclamped %v", unclamped.ExecutiveModelFractionHard, base.ExecutiveModelFractionHard)
	}
	if unclamped.ExecutiveModelFractionSoft != base.ExecutiveModelFractionSoft {
		t.Fatalf("user-estimate bucket clamped soft cap to %v, want unclamped %v", unclamped.ExecutiveModelFractionSoft, base.ExecutiveModelFractionSoft)
	}

	// A hard cap already below the published cap is unaffected, and soft
	// clamps to the (lower) effective hard when hard itself was clamped
	// below the configured soft.
	tight := AccountPolicy{PreserveWeeklyReserve: 0.30, ExecutiveModelFractionSoft: 0.35, ExecutiveModelFractionHard: 0.47}
	verylow := topology.Bucket{Name: topology.DimensionWeeklyModelFraction, Limit: 10, Source: topology.SourceCLIObservation, Window: topology.BucketWindowWeek}
	result := ClampFractionCaps(tight, verylow)
	if result.ExecutiveModelFractionHard != 0.10 {
		t.Fatalf("effective_hard = %v, want 0.10", result.ExecutiveModelFractionHard)
	}
	if result.ExecutiveModelFractionSoft != 0.10 {
		t.Fatalf("effective_soft = %v, want min(0.35,0.10)=0.10", result.ExecutiveModelFractionSoft)
	}
}

// TestPreserveWeeklyReserveRange asserts topology.ValidateReserve's own
// boundary behaviour (this package calls it, never re-declares it) --
// both boundary values and one value on each side.
func TestPreserveWeeklyReserveRange(t *testing.T) {
	cases := []struct {
		v       float64
		wantErr bool
	}{
		{0.0, false},
		{0.60, false},
		{0.30, false},
		{-0.01, true},
		{0.61, true},
	}
	for _, c := range cases {
		got, err := topology.ValidateReserve(c.v)
		if (err != nil) != c.wantErr {
			t.Fatalf("ValidateReserve(%v): err=%v, wantErr=%v", c.v, err, c.wantErr)
		}
		if got < 0.0 || got > 0.60 {
			t.Fatalf("ValidateReserve(%v) returned %v, out of [0,0.60]", c.v, got)
		}
	}
}

// TestResolveExecutiveAccountSingle: a single account with no executive
// role resolves as executive (R-21.110b); the stored role is untouched.
func TestResolveExecutiveAccountSingle(t *testing.T) {
	accounts := []topology.Account{{ID: "acct-only", Role: topology.AccountRoleWorkforce}}
	id, ok := ResolveExecutiveAccount(accounts)
	if !ok || id != "acct-only" {
		t.Fatalf("ResolveExecutiveAccount = (%q,%v), want (acct-only,true)", id, ok)
	}
	if accounts[0].Role != topology.AccountRoleWorkforce {
		t.Fatal("ResolveExecutiveAccount must never rewrite the stored role")
	}

	// Several accounts, one already executive: that one wins regardless
	// of order.
	multi := []topology.Account{
		{ID: "acct-1", Role: topology.AccountRoleWorkforce},
		{ID: "acct-2", Role: topology.AccountRoleExecutive},
	}
	id, ok = ResolveExecutiveAccount(multi)
	if !ok || id != "acct-2" {
		t.Fatalf("ResolveExecutiveAccount(multi) = (%q,%v), want (acct-2,true)", id, ok)
	}

	// Several accounts, none executive: the first in file order wins.
	noExec := []topology.Account{
		{ID: "acct-first", Role: topology.AccountRoleWorkforce},
		{ID: "acct-second", Role: topology.AccountRoleSpecialist},
	}
	id, ok = ResolveExecutiveAccount(noExec)
	if !ok || id != "acct-first" {
		t.Fatalf("ResolveExecutiveAccount(noExec) = (%q,%v), want (acct-first,true)", id, ok)
	}

	if _, ok := ResolveExecutiveAccount(nil); ok {
		t.Fatal("ResolveExecutiveAccount(nil) must report ok=false")
	}
}

// TestExecutiveRoleLadderAtHardReserve: at hard reserve the ladder is
// exactly [executive-overflow, critic]; every other state has no ladder.
func TestExecutiveRoleLadderAtHardReserve(t *testing.T) {
	ladder := ExecutiveRoleLadder(ReserveStateHard)
	want := []string{executiveOverflowClass, string(topology.LaneClassCritic)}
	if len(ladder) != len(want) || ladder[0] != want[0] || ladder[1] != want[1] {
		t.Fatalf("ExecutiveRoleLadder(hard) = %v, want %v", ladder, want)
	}
	for _, s := range []ReserveState{ReserveStateAbundant, ReserveStateSoft} {
		if got := ExecutiveRoleLadder(s); got != nil {
			t.Fatalf("ExecutiveRoleLadder(%v) = %v, want nil", s, got)
		}
	}
}

// TestEffectiveLaneClassExecutiveOverflow: executive/executive-xhigh on a
// workforce account derives executive-overflow at base 6.5; every other
// combination is unchanged.
func TestEffectiveLaneClassExecutiveOverflow(t *testing.T) {
	workforce := topology.Account{ID: "acct-w", Role: topology.AccountRoleWorkforce}
	executiveAcct := topology.Account{ID: "acct-e", Role: topology.AccountRoleExecutive}

	for _, lc := range []topology.LaneClass{topology.LaneClassExecutive, topology.LaneClassExecutiveXHigh} {
		lane := topology.Lane{LaneClass: lc, BaseShadowPrice: 12.0}
		class, price := EffectiveLaneClass(lane, workforce)
		if class != executiveOverflowClass || price != executiveOverflowBasePrice {
			t.Fatalf("EffectiveLaneClass(%v, workforce) = (%q,%v), want (%q,%v)", lc, class, price, executiveOverflowClass, executiveOverflowBasePrice)
		}
	}

	// Same lane class on the executive account itself: unchanged.
	lane := topology.Lane{LaneClass: topology.LaneClassExecutive, BaseShadowPrice: 12.0}
	class, price := EffectiveLaneClass(lane, executiveAcct)
	if class != string(topology.LaneClassExecutive) || price != 12.0 {
		t.Fatalf("EffectiveLaneClass(executive lane, executive account) = (%q,%v), want unchanged", class, price)
	}

	// A non-executive lane class on a workforce account: unchanged.
	criticLane := topology.Lane{LaneClass: topology.LaneClassCritic, BaseShadowPrice: 8.0}
	class, price = EffectiveLaneClass(criticLane, workforce)
	if class != string(topology.LaneClassCritic) || price != 8.0 {
		t.Fatalf("EffectiveLaneClass(critic lane, workforce) = (%q,%v), want unchanged", class, price)
	}
}

// TestExecutiveOverflowEligibleOnlyAtHardReserve proves the boundary
// exactly: true only at hard, false at soft and abundant.
func TestExecutiveOverflowEligibleOnlyAtHardReserve(t *testing.T) {
	if !ExecutiveOverflowEligible(ReserveStateHard) {
		t.Fatal("ExecutiveOverflowEligible(hard) = false, want true")
	}
	if ExecutiveOverflowEligible(ReserveStateSoft) {
		t.Fatal("ExecutiveOverflowEligible(soft) = true, want false")
	}
	if ExecutiveOverflowEligible(ReserveStateAbundant) {
		t.Fatal("ExecutiveOverflowEligible(abundant) = true, want false")
	}
}

// TestComputeReserveStateBoundaries asserts the exact R-21.31 barrier-band
// boundaries: abundant strictly above reserve+0.10, soft at the
// reserve+0.10 boundary itself, hard at and below reserve. Also asserts
// executive_model_fraction_soft/hard play no part in this computation --
// ComputeReserveState's signature does not even accept them.
func TestComputeReserveStateBoundaries(t *testing.T) {
	const reserve = 0.20
	cases := []struct {
		remaining float64
		want      ReserveState
	}{
		{0.31, ReserveStateAbundant}, // > reserve+0.10 (0.30)
		{0.30, ReserveStateSoft},     // == reserve+0.10 boundary -> soft
		{0.21, ReserveStateSoft},     // inside the band
		{0.20, ReserveStateHard},     // == reserve boundary -> hard
		{0.0, ReserveStateHard},
	}
	for _, c := range cases {
		got := ComputeReserveState(c.remaining, reserve)
		if got != c.want {
			t.Fatalf("ComputeReserveState(%v, %v) = %v, want %v", c.remaining, reserve, got, c.want)
		}
	}
}

// TestReserveStateValid covers ReserveState.Valid's closed membership.
func TestReserveStateValid(t *testing.T) {
	for _, s := range []ReserveState{ReserveStateAbundant, ReserveStateSoft, ReserveStateHard} {
		if !s.Valid() {
			t.Fatalf("ReserveState(%q).Valid() = false, want true", s)
		}
	}
	if ReserveState("bogus").Valid() {
		t.Fatal("ReserveState(\"bogus\").Valid() = true, want false")
	}
}

// TestReserveAdjustedRemainingAndActiveProjectCount proves the two
// project-share INPUTS this ticket ships (never the division or refusal,
// which live once in AO/S-79.T4): ReserveAdjustedRemaining floors at 0,
// and ActiveProjectCount is recomputed on start/stop and never negative.
func TestReserveAdjustedRemainingAndActiveProjectCount(t *testing.T) {
	barrier := topology.Bucket{RemainingFraction: 0.5}
	if got := ReserveAdjustedRemaining(barrier, 0.2); got != 0.3 {
		t.Fatalf("ReserveAdjustedRemaining = %v, want 0.3", got)
	}
	if got := ReserveAdjustedRemaining(barrier, 0.9); got != 0 {
		t.Fatalf("ReserveAdjustedRemaining floor = %v, want 0", got)
	}

	var count ActiveProjectCount
	count.ProjectStarted()
	count.ProjectStarted()
	if got := count.Count(); got != 2 {
		t.Fatalf("Count after two starts = %d, want 2", got)
	}
	count.ProjectStopped()
	if got := count.Count(); got != 1 {
		t.Fatalf("Count after one stop = %d, want 1", got)
	}
	count.ProjectStopped()
	count.ProjectStopped() // extra stop must never go negative
	if got := count.Count(); got != 0 {
		t.Fatalf("Count after extra stop = %d, want 0 (never negative)", got)
	}
}
