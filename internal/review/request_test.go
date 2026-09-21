package review

import (
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestReviewRequestBlind is the ticket's named root test: BlindRequest's
// field set is EXACTLY {CheckpointID, Rubric, Artifact, ConsequenceClass,
// Lease} -- by reflection over the live type, not a hand-maintained list a
// later field addition could silently outrun. Author identity, authoring
// lane, prior verdict and expected outcome are asserted absent by NAME,
// so a future field called e.g. "AuthorLane" fails this test even if its
// value happens to be unset in every construction path.
func TestReviewRequestBlind(t *testing.T) {
	want := map[string]bool{"CheckpointID": true, "Rubric": true, "Artifact": true, "ConsequenceClass": true, "Lease": true}
	forbidden := []string{"Author", "Identity", "Lane", "Verdict", "Outcome", "Session", "Transcript"}

	typ := reflect.TypeOf(BlindRequest{})
	if typ.NumField() != len(want) {
		t.Fatalf("BlindRequest has %d fields, want exactly %d (%v)", typ.NumField(), len(want), want)
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !want[name] {
			t.Errorf("BlindRequest has unexpected field %q", name)
		}
		for _, f := range forbidden {
			if strings.Contains(name, f) {
				t.Errorf("BlindRequest field %q names a forbidden concept (%q) -- author identity, lane, prior verdicts and expected outcome must be unreachable from this struct (R-21.156(a))", name, f)
			}
		}
	}

	// Construction-time proof, not just the type shape: a request built
	// from a Diff/Context that itself contains author-identity-shaped
	// text produces a BlindRequest whose fields still cannot NAME an
	// author -- there is no field to carry it into, regardless of what
	// the caller supplied.
	req := provider.ReviewRequest{Level: provider.ReviewCRLevelB, Diff: "diff --git a/x.go b/x.go\n+authored by alice", Context: "ticket T-4"}
	plan, err := NewPlan(req, ConsequenceNormal)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	blind := plan.Blind
	if (reflect.TypeOf(blind).NumField()) != len(want) {
		t.Fatalf("a constructed BlindRequest gained fields beyond the blind set")
	}
	// The AMD-20260916/6 checklist and the caller's Context ride in Rubric
	// (the level rubric R-21.156(a) already permits), NOT in a sixth field:
	// the blind field set is exactly the five above and stays that way.
	if !strings.Contains(blind.Rubric, "ticket T-4") {
		t.Errorf("the caller's Context did not reach the Rubric: %q", blind.Rubric)
	}
}

// TestReviewContextExclusion seeds a diff with a .claude/** hunk, an
// AGENTS.md hunk, a generated-projection hunk (a path under a `generated/`
// directory -- the segment rule, not the old substring heuristic) and one
// ordinary hunk, then asserts the filtered Artifact drops exactly the first
// three and keeps the fourth. Each excluded marker is distinctive text that
// would only appear if that hunk survived filtering -- this assertion CAN
// fail. The dialect coverage and the CR's own bypass inputs live in
// artifact_test.go.
func TestReviewContextExclusion(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/.claude/CLAUDE.md b/.claude/CLAUDE.md",
		"+SECRET_AUTHOR_BRIEFING_MARKER",
		"diff --git a/AGENTS.md b/AGENTS.md",
		"+AGENTS_MD_MARKER",
		"diff --git a/internal/harness/generated/claude.md b/internal/harness/generated/claude.md",
		"+GENERATED_PROJECTION_MARKER",
		"diff --git a/internal/review/provider.go b/internal/review/provider.go",
		"+ORDINARY_HUNK_MARKER",
	}, "\n")

	got, excluded, err := FilterArtifact(diff)
	if err != nil {
		t.Fatalf("FilterArtifact: %v", err)
	}

	for _, marker := range []string{"SECRET_AUTHOR_BRIEFING_MARKER", "AGENTS_MD_MARKER", "GENERATED_PROJECTION_MARKER"} {
		if strings.Contains(got, marker) {
			t.Errorf("filtered artifact still contains excluded marker %q:\n%s", marker, got)
		}
	}
	if !strings.Contains(got, "ORDINARY_HUNK_MARKER") {
		t.Errorf("filtered artifact dropped the ordinary (non-excluded) hunk:\n%s", got)
	}
	if len(excluded) != 3 {
		t.Errorf("excluded = %v, want all three excluded paths reported to the caller", excluded)
	}

	// Construction-time proof: NewPlan's own Artifact field reflects the
	// same filtering, never a raw pass-through, and the exclusion is
	// carried forward so the caller can be told.
	plan, err := NewPlan(provider.ReviewRequest{Level: provider.ReviewCRLevelB, Diff: diff}, ConsequenceNormal)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if strings.Contains(plan.Blind.Artifact, "SECRET_AUTHOR_BRIEFING_MARKER") {
		t.Error("BlindRequest.Artifact carries author-written .claude/** content")
	}
	if len(plan.Excluded) != 3 {
		t.Errorf("Plan.Excluded = %v, want the three excluded paths", plan.Excluded)
	}
}

// TestConsequenceClassPolicy asserts ConsequenceClassForLevel's mapping and
// the HOLD/fallback rules a wrong value would silently invert.
func TestConsequenceClassPolicy(t *testing.T) {
	cases := []struct {
		level            provider.ReviewCRLevel
		want             ConsequenceClass
		wantDistinct     bool
		wantHoldOnAbsent bool
	}{
		{provider.ReviewCRLevelA, ConsequenceLow, false, false},
		{provider.ReviewCRLevelB, ConsequenceNormal, true, false},
		{provider.ReviewCRLevelC, ConsequenceHigh, true, true},
	}
	for _, tc := range cases {
		got := ConsequenceClassForLevel(tc.level)
		if got != tc.want {
			t.Errorf("ConsequenceClassForLevel(%q) = %q, want %q", tc.level, got, tc.want)
		}
		if got.RequiresDistinctFamily() != tc.wantDistinct {
			t.Errorf("%q.RequiresDistinctFamily() = %v, want %v", got, got.RequiresDistinctFamily(), tc.wantDistinct)
		}
		if got.HoldsWithoutDistinctFamily() != tc.wantHoldOnAbsent {
			t.Errorf("%q.HoldsWithoutDistinctFamily() = %v, want %v", got, got.HoldsWithoutDistinctFamily(), tc.wantHoldOnAbsent)
		}
	}
	if !ConsequenceClass("bogus").RequiresDistinctFamily() {
		t.Error("an unknown ConsequenceClass must fail closed to RequiresDistinctFamily() == true")
	}
	if !ConsequenceClass("bogus").HoldsWithoutDistinctFamily() {
		t.Error("an unknown ConsequenceClass must fail closed to HoldsWithoutDistinctFamily() == true")
	}
}

// TestCheckpointIDDeterministic: the same filtered artifact always yields
// the same CheckpointID, and a different artifact yields a different one
// -- proving it is a real content digest, not a random or constant value.
func TestCheckpointIDDeterministic(t *testing.T) {
	req1 := provider.ReviewRequest{Level: provider.ReviewCRLevelA, Diff: "diff --git a/x.go b/x.go\n+one"}
	req2 := provider.ReviewRequest{Level: provider.ReviewCRLevelA, Diff: "diff --git a/x.go b/x.go\n+two"}

	a1 := mustPlan(t, provider.ReviewCRLevelA, ConsequenceLow, req1.Diff, "").Blind
	a2 := mustPlan(t, provider.ReviewCRLevelA, ConsequenceLow, req1.Diff, "").Blind
	if a1.CheckpointID != a2.CheckpointID {
		t.Errorf("CheckpointID not deterministic: %q vs %q for the same artifact", a1.CheckpointID, a2.CheckpointID)
	}
	if a1.CheckpointID == "" {
		t.Error("CheckpointID must not be empty")
	}
	b := mustPlan(t, provider.ReviewCRLevelA, ConsequenceLow, req2.Diff, "").Blind
	if a1.CheckpointID == b.CheckpointID {
		t.Error("two different artifacts produced the same CheckpointID")
	}
}

// TestSensitivityTagFrom is DELETED with the function it covered. The CR fix
// D1 removed the `sensitivity: <tier>` regex entirely: three ordinary
// spellings walked past it, and the reviewer never had the authority to
// decide a tier from a caller's free text in the first place. The replacement
// proofs are router_test.go's TestReviewProvider_LocalOnlySensitivityRefused
// (the real router refusing on the thread's mode) and
// TestReviewDispatchSensitivityIsTaskClassDefault (the dispatched tier being
// the §5.16 class default, never below it).

// TestConsequenceClassValid covers Valid's true and false branches.
func TestConsequenceClassValid(t *testing.T) {
	for _, c := range []ConsequenceClass{ConsequenceLow, ConsequenceNormal, ConsequenceHigh, ConsequenceCritical} {
		if !c.Valid() {
			t.Errorf("%q.Valid() = false, want true", c)
		}
	}
	if ConsequenceClass("bogus").Valid() {
		t.Error(`ConsequenceClass("bogus").Valid() = true, want false`)
	}
}

// TestLeaseStructurallyReadOnly asserts Lease has zero fields -- there is
// no way to construct a Lease that grants write access, because no field
// exists to set. This is the structural proof, not a runtime boolean check.
func TestLeaseStructurallyReadOnly(t *testing.T) {
	if n := reflect.TypeOf(Lease{}).NumField(); n != 0 {
		t.Fatalf("Lease has %d fields, want 0 -- a field could be used to request write access", n)
	}
	if !(Lease{}).ReadOnly() {
		t.Error("Lease{}.ReadOnly() = false, want true")
	}
}
