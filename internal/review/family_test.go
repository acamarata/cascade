package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: the CR fix D2 proofs -- the cross-family gate the draft left
//   fail-open. Split out of reviewer_test.go only because the two together
//   exceed Art.10.3's 300-line file cap.
// SPORT: internal/review.family-tests (ADD, P1-E25-W5-S52-T4).

// TestCRCSameFamilyAtHighConsequenceRefuses is the CR fix D2(b) proof and the
// test the CR's own mutation must kill. The CR's input: two families
// REGISTERED (so the pre-dispatch gate passes) but both CR-C passes routed to
// the SAME provider -- the normal case, since ModelRequest carries no
// family-steering field. The draft returned findings, no error, and recorded
// distinct=false: fail-open. Now the review HOLDS post-dispatch, returns no
// findings, raises the ErrNoEligibleReviewerFamily escalation and names both
// families. Forcing record.ReviewerFamilyDistinct = true in crc.go (the CR's
// mutation) skips the refusal and turns this red.
func TestCRCSameFamilyAtHighConsequenceRefuses(t *testing.T) {
	for _, consequence := range []ConsequenceClass{ConsequenceHigh, ConsequenceCritical} {
		t.Run(string(consequence), func(t *testing.T) {
			exec := &fakeExecutor{responses: []provider.ModelResponse{
				{Output: `{"approved":true,"findings":[]}`, Selection: provider.Selection{Provider: "anthropic-acc1"}},
				{Output: `{"approved":true,"verdict":"approve","dissent":"","findings":[{"severity":"nit","file":"x.go","line":1,"message":"m"}]}`,
					Selection: provider.Selection{Provider: "anthropic-acc1"}},
			}}
			plan := mustPlan(t, provider.ReviewCRLevelC, consequence, "diff --git a/x.go b/x.go\n+x", "")

			report, record, err := runCRC(t, plan, exec, threeAccountRegistry(), nil)
			if err == nil {
				t.Fatal("CR-C completed with both passes on one family: the cross-family gate is fail-open")
			}
			if !errors.Is(err, ErrNoEligibleReviewerFamily) {
				t.Errorf("error = %v, want the ErrNoEligibleReviewerFamily escalation", err)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindElevationRequired {
				t.Errorf("error kind = %v (ok=%v), want KindElevationRequired", kind, ok)
			}
			if strings.Count(err.Error(), "anthropic") < 2 {
				t.Errorf("the refusal %q does not name BOTH observed families", err.Error())
			}
			if len(report.Findings) != 0 || report.Verdict != "" {
				t.Errorf("a held review still returned a report: %+v", report)
			}
			if record.ReviewerFamilyDistinct {
				t.Error("record.ReviewerFamilyDistinct = true after both passes landed on one family")
			}
			if record.DistinctnessReason != distinctnessObservedSame {
				t.Errorf("DistinctnessReason = %q, want %q", record.DistinctnessReason, distinctnessObservedSame)
			}
		})
	}
}

// TestSinglePassDistinctnessIsUnobservable is the CR fix D2(a) proof: CR-A and
// CR-B make ONE dispatch, so there is no second family to compare against and
// the flag must be false with the "unobservable" reason. The draft derived it
// from registry CAPABILITY, so a CR-B dispatch that landed on the author's own
// family was recorded as cross-family verified -- a false ledger row.
func TestSinglePassDistinctnessIsUnobservable(t *testing.T) {
	for _, level := range []provider.ReviewCRLevel{provider.ReviewCRLevelA, provider.ReviewCRLevelB} {
		t.Run(string(level), func(t *testing.T) {
			exec := &fakeExecutor{responses: []provider.ModelResponse{
				{Output: `{"approved":true,"findings":[]}`, Selection: provider.Selection{Provider: "anthropic-acc1"}},
			}}
			plan := mustPlan(t, level, ConsequenceClassForLevel(level), "diff --git a/x.go b/x.go\n+x", "")
			_, record, err := Review(context.Background(), plan, level, exec, twoFamilyRegistry(), nil)
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			if record.ReviewerFamilyDistinct {
				t.Errorf("%s recorded ReviewerFamilyDistinct = true from a single dispatch: "+
					"two REGISTERED families are not evidence that two were USED", level)
			}
			if record.DistinctnessReason != distinctnessUnobservable {
				t.Errorf("%s DistinctnessReason = %q, want %q", level, record.DistinctnessReason, distinctnessUnobservable)
			}
		})
	}
}

// TestCRCNoSelectionIsNotDistinct: an executor that names no provider proves
// nothing, so distinctness stays false with its own reason rather than
// defaulting to "different".
func TestCRCNoSelectionIsNotDistinct(t *testing.T) {
	reg := threeAccountRegistry()
	none := resolveFamily(context.Background(), reg, provider.Selection{})
	known := resolveFamily(context.Background(), reg, provider.Selection{Provider: "anthropic-acc1"})
	if distinct, reason := observedDistinct(none, known); distinct || reason != distinctnessNoSelection {
		t.Errorf("observedDistinct(none, anthropic-acc1) = (%v, %q), want (false, %q)", distinct, reason, distinctnessNoSelection)
	}
	// And an unresolvable name has its own reason, never "different".
	ghost := resolveFamily(context.Background(), reg, provider.Selection{Provider: "ghost-acc"})
	if distinct, reason := observedDistinct(ghost, known); distinct || reason != distinctnessUnresolvedFamily {
		t.Errorf("observedDistinct(ghost-acc, anthropic-acc1) = (%v, %q), want (false, %q)",
			distinct, reason, distinctnessUnresolvedFamily)
	}
}

// threeAccountRegistry is the DEFAULT fleet shape, and the confirming review's
// fail-open input: TWO Anthropic accounts sharing one DRIVER, plus one OpenAI
// account. eligibleFamilies counts two FAMILIES here (so the pre-dispatch gate
// passes), while the two Anthropic accounts are one family between them.
func threeAccountRegistry() fakeRegistry {
	return fakeRegistry{infos: []provider.ProviderInfo{
		{Name: "anthropic-acc1", Driver: "anthropic"},
		{Name: "anthropic-acc2", Driver: "anthropic"},
		{Name: "openai-acc1", Driver: "openai-compat"},
	}}
}

// crcExecutor queues the propose/challenge pair CR-C dispatches, each landing
// on the named registry PROVIDER (an account name -- what
// internal/conductor's filters_dispatch.go actually puts in
// Selection.Provider, never a driver).
func crcExecutor(proposeAccount, challengeAccount string) *fakeExecutor {
	return &fakeExecutor{responses: []provider.ModelResponse{
		{Output: `{"approved":true,"findings":[]}`, Selection: provider.Selection{Provider: proposeAccount}},
		{Output: `{"approved":true,"verdict":"approve","dissent":"d","findings":[{"severity":"nit","file":"x.go","line":1,"message":"m"}]}`,
			Selection: provider.Selection{Provider: challengeAccount}},
	}}
}

// runCRC calls CRC with the registry the family comparison resolves names
// through. It exists so every case below reads as one line.
func runCRC(t *testing.T, plan Plan, exec provider.ModelExecutor, reg fakeRegistry, events EventPublisher) (CRCReport, VerdictRecord, error) {
	t.Helper()
	return CRC(context.Background(), plan, exec, reg, events)
}

// TestCRCFamilyIsTheDriverNotTheAccountName is the confirming review's first
// blocking input. The router reports Selection.Provider as the registry
// provider NAME (an account: "anthropic-acc1"), while the family gate counts
// ProviderInfo.Driver. Comparing the NAMES read the default two-Anthropic-
// account fleet as two families: the High HOLD never fired, findings were
// returned and the verdict recorded reviewer_family_distinct=true -- a false
// ledger row and the exact fail-open D2 exists to close. Both directions are
// pinned here, so a name-compare and a "distinct unless proven same" mutation
// each turn one subtest red.
func TestCRCFamilyIsTheDriverNotTheAccountName(t *testing.T) {
	plan := mustPlan(t, provider.ReviewCRLevelC, ConsequenceHigh, "diff --git a/x.go b/x.go\n+x", "")

	t.Run("two accounts on one driver: refused", func(t *testing.T) {
		exec := crcExecutor("anthropic-acc1", "anthropic-acc2")
		report, record, err := runCRC(t, plan, exec, threeAccountRegistry(), nil)
		if err == nil {
			t.Fatalf("two accounts of ONE driver were accepted as distinct families: %+v", report)
		}
		if !errors.Is(err, ErrNoEligibleReviewerFamily) {
			t.Errorf("error = %v, want the ErrNoEligibleReviewerFamily escalation", err)
		}
		if record.ReviewerFamilyDistinct || record.DistinctnessReason != distinctnessObservedSame {
			t.Errorf("record = %+v, want distinct=false with reason %q", record, distinctnessObservedSame)
		}
		if len(report.Findings) != 0 {
			t.Errorf("a held review still returned findings: %+v", report.Findings)
		}
		if strings.Contains(err.Error(), "anthropic-acc1") {
			t.Errorf("the refusal %q names the ACCOUNT, not the family", err.Error())
		}
		if strings.Count(err.Error(), "anthropic") < 2 {
			t.Errorf("the refusal %q does not name BOTH observed families", err.Error())
		}
	})

	t.Run("two accounts on two drivers: proceeds", func(t *testing.T) {
		exec := crcExecutor("anthropic-acc1", "openai-acc1")
		report, record, err := runCRC(t, plan, exec, threeAccountRegistry(), nil)
		if err != nil {
			t.Fatalf("two DIFFERENT drivers were refused: %v", err)
		}
		if !record.ReviewerFamilyDistinct || record.DistinctnessReason != distinctnessObservedDifferent {
			t.Errorf("record = %+v, want distinct=true with reason %q", record, distinctnessObservedDifferent)
		}
		if len(report.Findings) == 0 || report.Verdict == "" {
			t.Errorf("a proceeding review returned no report: %+v", report)
		}
	})
}

// TestCRCUnresolvableAccountNameIsNotDistinct: a Selection naming a provider
// the registry cannot resolve to a driver proves nothing about families, so it
// fails CLOSED -- not "distinct because the two strings differ".
func TestCRCUnresolvableAccountNameIsNotDistinct(t *testing.T) {
	plan := mustPlan(t, provider.ReviewCRLevelC, ConsequenceHigh, "diff --git a/x.go b/x.go\n+x", "")
	exec := crcExecutor("ghost-acc", "openai-acc1")
	_, record, err := runCRC(t, plan, exec, threeAccountRegistry(), nil)
	if err == nil {
		t.Fatal("an unresolvable provider name was accepted as its own distinct family")
	}
	if record.ReviewerFamilyDistinct {
		t.Error("record.ReviewerFamilyDistinct = true from a name no registry could resolve")
	}
	if !strings.Contains(err.Error(), "ghost-acc") {
		t.Errorf("the refusal %q does not name the unresolvable provider", err.Error())
	}
}

// TestCRCNormalConsequenceFallbackIsPublished is the confirming review's
// should-fix on the documented review.CRC entry (D6): at ConsequenceNormal two
// passes on ONE family may proceed, but never SILENTLY. The name comparison
// made this fallback invisible for the default fleet -- two accounts read as
// two families, so no fallback was recorded and no event was published.
func TestCRCNormalConsequenceFallbackIsPublished(t *testing.T) {
	plan := mustPlan(t, provider.ReviewCRLevelC, ConsequenceNormal, "diff --git a/x.go b/x.go\n+x", "")
	events := &recordingEvents{}
	report, record, err := runCRC(t, plan, crcExecutor("anthropic-acc1", "anthropic-acc2"), threeAccountRegistry(), events)
	if err != nil {
		t.Fatalf("CRC at consequence normal: %v", err)
	}
	if len(report.Findings) == 0 {
		t.Error("a normal-consequence fallback returned no findings: it proceeds, it does not hold")
	}
	if !record.SameFamilyFallback {
		t.Error("record.SameFamilyFallback = false after both passes landed on one family")
	}
	fbs := events.fallbacks()
	if len(fbs) != 1 {
		t.Fatalf("published %d fallback events, want exactly 1 -- a silent same-family fallback is the defect", len(fbs))
	}
	if fbs[0].Level != provider.ReviewCRLevelC || fbs[0].ConsequenceClass != ConsequenceNormal {
		t.Errorf("fallback event = %+v, want it to name CR-C at consequence normal", *fbs[0])
	}

	// And the same fact survives the Review path, whose pre-dispatch gate
	// saw two families and must not overwrite what the dispatch observed.
	_, viaReview, err := Review(context.Background(), plan, provider.ReviewCRLevelC,
		crcExecutor("anthropic-acc1", "anthropic-acc2"), threeAccountRegistry(), &recordingEvents{})
	if err != nil {
		t.Fatalf("Review at consequence normal: %v", err)
	}
	if !viaReview.SameFamilyFallback {
		t.Error("Review discarded the observed same-family fallback the dispatch recorded")
	}
}
