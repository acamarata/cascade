// Purpose: Review, this package's own richer dispatch orchestration entry
//   point (P1-E25-W5-S52-T4): the CR-A/CR-B single dispatch vs the CR-C
//   two-pass adversarial split (crc.go), the R-21.156 family gate, and
//   VerdictRecord -- the reviewer_family_distinct flag this ticket publishes
//   for AH/S-69.T2's (not yet built) ledger.
// Inputs: a Plan, the requested level, a provider.ModelExecutor, a
//   provider.ProviderRegistryReader and a (possibly nil)
//   EventPublisher.
// Outputs: a provider.ReviewResponse, a VerdictRecord, or a typed error
//   (ErrNoEligibleReviewerFamily on a HOLD, pre- or post-dispatch).
// Constraints: ReviewerFamilyDistinct is set ONLY from the provider families
//   the router actually chose, never from what the registry merely offers
//   (CR fix D2). There is no family-steering field on provider.ModelRequest,
//   so a pre-dispatch PREFERENCE cannot exist here and is not faked: the
//   registry query can prove "no distinct family is possible" (a HOLD), and
//   only the observed Selections can prove "the two passes did differ".
// SPORT: internal/review.reviewer/ADD (P1-E25-W5-S52-T4).

package review

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ErrNoEligibleReviewerFamily is returned (never a same-family fallback)
// when consequence_class is High or Critical and no distinct reviewer family
// is available -- either because fewer than two are registered at all
// (pre-dispatch), or because both CR-C passes actually landed on the same
// family (post-dispatch, crc.go). It wraps KindElevationRequired: this
// taxonomy's HUMAN_APPROVAL_REQUIRED escalation (no literal
// "HUMAN_APPROVAL_REQUIRED" kind exists in this tree's real error taxonomy;
// KindElevationRequired is its classified equivalent, recorded as
// contradiction C in this ticket's PCI).
var ErrNoEligibleReviewerFamily = cascade.New(cascade.KindElevationRequired,
	"internal/review: no eligible distinct reviewer model family for this consequence class -- review HOLDS, raising human-approval escalation rather than falling back to the same family")

// The fixed DistinctnessReason strings. Each states what was OBSERVED, so a
// ledger row can never read as a stronger claim than the evidence behind it.
const (
	// distinctnessUnobservable is CR-A's and CR-B's reason: one dispatch,
	// so there is no second family to compare against. The flag is false
	// because nothing was proven, NOT because something failed.
	distinctnessUnobservable = "single pass, distinctness unobservable"
	// distinctnessObservedSame: both CR-C passes landed on one family.
	distinctnessObservedSame = "both passes landed on the same provider family"
	// distinctnessObservedDifferent: the two passes landed on two families.
	distinctnessObservedDifferent = "the two passes landed on different provider families"
	// distinctnessNoSelection: the executor reported no provider for at
	// least one pass, so distinctness is unproven, never assumed.
	distinctnessNoSelection = "at least one pass reported no selected provider, so distinctness is unproven"
	// distinctnessUnresolvedFamily: at least one pass named a provider
	// the registry cannot map to a driver, so its FAMILY is unknown. Two
	// unknown names are not two families, and this fails closed.
	distinctnessUnresolvedFamily = "at least one pass's provider could not be resolved to a provider family, " +
		"so distinctness is unproven"
)

// VerdictRecord is what this ticket publishes for AH/S-69.T2's (not yet
// built) ledger: the consequence class a dispatch resolved and whether it
// actually landed on a distinct model family. pkg/provider.ReviewResponse
// (O/S-33.T1's frozen ABI) has no field for either -- Provider.Review (the
// ABI path) computes and then discards this value; a same-binary caller
// (AH/S-69.T2, once built) calls Review or CRC directly, as this package's
// own tests do, to read it. Listed as the ABI gap in this ticket's PCI.
type VerdictRecord struct {
	// ConsequenceClass is the class this dispatch resolved under.
	ConsequenceClass ConsequenceClass
	// ReviewerFamilyDistinct reports whether the passes that ACTUALLY RAN
	// resolved to different provider families. It is derived from
	// provider.Selection.Provider and from nothing else: the registry's
	// capability to offer two families is not evidence that two were used,
	// and for a single-pass level (CR-A, CR-B) this is always false with
	// DistinctnessReason == distinctnessUnobservable.
	ReviewerFamilyDistinct bool
	// DistinctnessReason states, in one of this file's fixed strings, WHY
	// ReviewerFamilyDistinct holds the value it does.
	DistinctnessReason string
	// SameFamilyFallback reports whether this dispatch proceeded on a
	// same-family fallback because no distinct family was eligible, on a
	// consequence class low enough that this fallback is still legal
	// (R-21.156(b): only below "high"). Every such fallback is published
	// through EventPublisher -- never silent.
	SameFamilyFallback bool
}

// eligibleFamilies returns the distinct provider "families" (driver kinds,
// provider.ProviderInfo.Driver) currently registered -- the real registry
// query J/S-19/J/S-20 publish (pkg/provider.ProviderRegistryReader).
func eligibleFamilies(ctx context.Context, reg provider.ProviderRegistryReader) ([]string, error) {
	infos, err := reg.ListProviders(ctx)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "internal/review: list provider families")
	}
	seen := make(map[string]bool, len(infos))
	families := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Driver == "" || seen[info.Driver] {
			continue
		}
		seen[info.Driver] = true
		families = append(families, info.Driver)
	}
	return families, nil
}

// familyGate runs the R-21.156/R-21.191 PRE-dispatch check: below "normal"
// it is a no-op; at or above it queries the registry and, on fewer than two
// distinct families, either HOLDS (High/Critical: ErrNoEligibleReviewerFamily,
// zero dispatch attempted) or records AND PUBLISHES a same-family fallback
// (Normal). Two or more registered families prove only that a distinct pick
// is POSSIBLE, so this function never sets ReviewerFamilyDistinct -- that
// flag comes from the observed Selections alone (CR fix D2(a)).
func familyGate(ctx context.Context, reg provider.ProviderRegistryReader, level provider.ReviewCRLevel,
	consequence ConsequenceClass, events EventPublisher) (VerdictRecord, error) {
	record := VerdictRecord{ConsequenceClass: consequence, DistinctnessReason: distinctnessUnobservable}
	if !consequence.RequiresDistinctFamily() {
		return record, nil
	}
	families, err := eligibleFamilies(ctx, reg)
	if err != nil {
		return record, err
	}
	if len(families) >= 2 {
		return record, nil
	}
	if consequence.HoldsWithoutDistinctFamily() {
		return record, ErrNoEligibleReviewerFamily
	}
	record.SameFamilyFallback = true
	publishFallback(ctx, events, FallbackEvent{
		Level: level, ConsequenceClass: consequence,
		Families: families, Reason: fallbackReasonSingleFamily,
	})
	return record, nil
}

// Review is internal/review's own dispatch orchestration entry point.
// Provider.Review (provider.go, the ABI path) always calls this; a
// same-binary caller needing VerdictRecord's fields (AH/S-69.T2, once built)
// calls it directly. ctx is passed to every Execute unchanged, so the
// thread privacy the caller attached reaches the router.
func Review(ctx context.Context, plan Plan, level provider.ReviewCRLevel, exec provider.ModelExecutor,
	reg provider.ProviderRegistryReader, events EventPublisher) (provider.ReviewResponse, VerdictRecord, error) {
	record, err := familyGate(ctx, reg, level, plan.Blind.ConsequenceClass, events)
	if err != nil {
		return provider.ReviewResponse{}, record, err
	}
	if level == provider.ReviewCRLevelC {
		report, crcRecord, err := CRC(ctx, plan, exec, reg, events)
		// OR, never assignment: the pre-dispatch gate and the OBSERVED
		// dispatch are two independent reasons to record a same-family
		// fallback, and overwriting discarded whichever the other found
		// (a registry offering two families erased an observed
		// single-family dispatch from the ledger row).
		crcRecord.SameFamilyFallback = crcRecord.SameFamilyFallback || record.SameFamilyFallback
		if err != nil {
			return provider.ReviewResponse{}, crcRecord, err
		}
		return provider.ReviewResponse{Findings: report.Findings, Approved: report.Approved}, crcRecord, nil
	}
	tmpl, err := TemplateFor(level)
	if err != nil {
		return provider.ReviewResponse{}, record, err
	}
	resp, _, err := dispatchOnce(ctx, exec, plan, tmpl)
	if err != nil {
		return provider.ReviewResponse{}, record, err
	}
	return resp, record, nil
}
