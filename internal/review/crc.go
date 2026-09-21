// Purpose: CR-C, the two-pass adversarial sequence (P1-E25-W5-S52-T4): the
//   propose dispatch, the challenge dispatch whose prompt carries the propose
//   output VERBATIM, the OBSERVED cross-family check, and CRCReport -- the
//   verdict/dissent shape pkg/provider.ReviewResponse cannot carry.
// Inputs: a Plan, a provider.ModelExecutor, a provider.ProviderRegistryReader
//   (the name->driver resolver the family comparison needs), a (possibly nil)
//   EventPublisher.
// Outputs: a CRCReport plus its VerdictRecord, or a typed error.
// Constraints: two things the CR found fail-open are closed here.
//   (1) ReviewerFamilyDistinct is computed from the two Selections the router
//   actually returned, never from registry capability (D2(a)) -- and it
//   compares the FAMILY (provider.ProviderInfo.Driver, resolved through the
//   registry) rather than Selection.Provider, which is the ACCOUNT name the
//   dispatch filter records. Two accounts of one vendor are one family; an
//   account name the registry cannot resolve is NOT distinct (fail closed).
//   (2) At a consequence class that HOLDS (High/Critical), two passes on the
//   SAME family REFUSE the review post-dispatch: no findings are returned and
//   ErrNoEligibleReviewerFamily is raised with both family names in the
//   message (D2(b)). The refusal is driven by record.ReviewerFamilyDistinct
//   itself, so forcing that flag true -- the CR's own mutation -- turns
//   TestCRCSameFamilyAtHighConsequenceRefuses red instead of silently
//   producing a self-ratifying ledger row.
//
//   FAN-OUT (D9, honest): this is two ORDERED Execute calls, not
//   internal/conductor.FanOut (fanout.go, K/S-23.T2). FanOut needs
//   WithPermitFn/JournalAppender collaborators only the DAEMON composition
//   root constructs, and this package dispatches over the client-side
//   provider.ModelExecutor seam `cascade run` itself uses, which cannot reach
//   them. So there is no per-leg governor permit and no fan-out journal leg
//   here. Recorded as contradiction E for a daemon-side tail ticket, never
//   silently substituted for the real primitive.
// SPORT: internal/review.crc/ADD (P1-E25-W5-S52-T4).

package review

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// CRCReport is CR-C's own richer result: the arbitration verdict and dissent
// fields the ticket's acceptance criterion names, plus the same Findings/
// Approved shape ReviewResponse carries. provider.ReviewResponse cannot hold
// Verdict/Dissent (O/S-33.T1's frozen ABI has exactly Findings and Approved
// and no summary, notes or metadata field -- verified, not assumed), so CRC
// is the documented caller-facing entry point for them and the ABI gap is
// listed in this ticket's PCI (D6).
type CRCReport struct {
	Findings []provider.ReviewFinding
	Approved bool
	Verdict  string
	Dissent  string
}

// arbitrationWire is the structured JSON payload the CHALLENGE pass's
// Requirements.Structured=true instructs the model to emit: a final verdict,
// a dissent, the single-pass finding shape, and the same checklist/executed-
// check records every level must answer (AMD-20260916/6).
type arbitrationWire struct {
	Approved  bool                  `json:"approved"`
	Verdict   string                `json:"verdict"`
	Dissent   string                `json:"dissent"`
	Findings  []wireFinding         `json:"findings"`
	Checklist []wireChecklistAnswer `json:"checklist"`
	Executed  []wireExecutedCheck   `json:"executed_checks"`
}

// parseArbitration decodes the challenge pass's structured Output into a
// CRCReport -- consumed, never discarded, the same rule parseFindings
// enforces for the single-pass levels -- and applies the checklist rule.
func parseArbitration(output string, checks Checklist) (CRCReport, error) {
	var w arbitrationWire
	if err := json.Unmarshal([]byte(output), &w); err != nil {
		return CRCReport{}, cascade.Wrap(cascade.KindInternal, err, "internal/review: parse CR-C arbitration output")
	}
	findings, err := decodeFindings(w.Findings)
	if err != nil {
		return CRCReport{}, err
	}
	violations := checks.Violations(w.Checklist, w.Executed)
	findings = append(findings, violations...)
	return CRCReport{
		Findings: findings,
		Approved: w.Approved && len(violations) == 0,
		Verdict:  w.Verdict,
		Dissent:  w.Dissent,
	}, nil
}

// buildChallengeRequest embeds propose's Output VERBATIM -- byte for byte, no
// truncation, no summarization (TestCRCFanOut_ChallengeReceivesPropose). The
// challenge rubric is followed by the blind request's own Rubric tail (the
// caller's verbatim Context and the checklist), so the challenge pass is
// held to the same checklist the propose pass was.
func buildChallengeRequest(blind BlindRequest, tmpl TaskTemplate, proposeOutput string) provider.ModelRequest {
	prompt := crCChallengeRubric + "\n\n" + blind.Rubric +
		"\n\nARTIFACT:\n" + blind.Artifact +
		"\n\nPROPOSAL (verbatim, challenge this):\n" + proposeOutput
	return provider.ModelRequest{
		TaskID:       blind.CheckpointID + "-challenge",
		TaskClass:    string(tmpl.TaskClass()),
		Inputs:       []provider.ChatMessage{{Role: "user", Content: prompt}},
		Requirements: tmpl.Requirements(),
		Sensitivity:  tmpl.Sensitivity(),
	}
}

// observedFamily is one pass's resolved provider FAMILY: the Selection the
// executor reported, the registry driver that provider name maps to, and
// whether that resolution succeeded at all.
type observedFamily struct {
	sel      provider.Selection
	family   string
	resolved bool
}

// resolveFamily maps one observed Selection to its provider family.
// provider.Selection.Provider is the registry provider NAME the dispatch
// filter recorded (internal/conductor/filters_dispatch.go sets it from
// cand.provider.Name) -- an ACCOUNT such as "anthropic-acc1" -- while a
// "family" everywhere else in this package is provider.ProviderInfo.Driver
// (reviewer.go's eligibleFamilies counts drivers). Comparing the NAMES read
// the default two-Anthropic-account fleet as two families, which is the
// fail-open the confirming review found, so the name is resolved through the
// registry reader Review already holds. A name no registry can resolve is
// reported unresolved and therefore NOT distinct: this fails closed.
func resolveFamily(ctx context.Context, reg provider.ProviderRegistryReader, sel provider.Selection) observedFamily {
	out := observedFamily{sel: sel}
	if sel.Provider == "" || reg == nil {
		return out
	}
	info, err := reg.GetProvider(ctx, sel.Provider)
	if err != nil || info.Driver == "" {
		return out
	}
	out.family, out.resolved = info.Driver, true
	return out
}

// label renders one pass's family for the refusal message: the resolved
// family, or the account name it could not be resolved from, or the absence
// itself -- never an empty string standing in for any of the three.
func (f observedFamily) label() string {
	switch {
	case f.resolved:
		return f.family
	case f.sel.Provider != "":
		return f.sel.Provider + " (family unresolved)"
	default:
		return "(none reported)"
	}
}

// observedDistinct reports whether the two passes landed on different
// provider FAMILIES, and the fixed reason string for that verdict. An empty
// Provider, or a provider name the registry cannot map to a driver, means the
// dispatch proved nothing -- "unproven", never "distinct".
func observedDistinct(propose, challenge observedFamily) (bool, string) {
	if propose.sel.Provider == "" || challenge.sel.Provider == "" {
		return false, distinctnessNoSelection
	}
	if !propose.resolved || !challenge.resolved {
		return false, distinctnessUnresolvedFamily
	}
	if propose.family == challenge.family {
		return false, distinctnessObservedSame
	}
	return true, distinctnessObservedDifferent
}

// sameFamilyRefusal is the D2(b) post-dispatch HOLD: both passes landed on
// one family at a consequence class that may not fall back. It WRAPS
// ErrNoEligibleReviewerFamily (rather than declaring a sibling sentinel, the
// R-21.217 rule privacy.go follows) so errors.Is still holds, and names both
// observed families in the message.
func sameFamilyRefusal(consequence ConsequenceClass, propose, challenge observedFamily) error {
	return cascade.Wrapf(cascade.KindElevationRequired, ErrNoEligibleReviewerFamily,
		"internal/review: CR-C at consequence %s requires two DISTINCT reviewer families, but the propose pass "+
			"landed on family %q and the challenge pass on family %q; the review HOLDS with no findings returned",
		consequence, propose.label(), challenge.label())
}

// CRC runs the CR-C two-pass adversarial sequence: a propose dispatch, then
// a challenge dispatch whose prompt carries the propose pass's Output
// verbatim. The pre-dispatch family gate is Review's job; CRC owns the
// POST-dispatch observed-distinctness check.
func CRC(ctx context.Context, plan Plan, exec provider.ModelExecutor,
	reg provider.ProviderRegistryReader, events EventPublisher) (CRCReport, VerdictRecord, error) {
	blind := plan.Blind
	record := VerdictRecord{ConsequenceClass: blind.ConsequenceClass, DistinctnessReason: distinctnessNoSelection}
	tmpl, err := TemplateFor(provider.ReviewCRLevelC)
	if err != nil {
		return CRCReport{}, record, err
	}
	proposeResp, err := exec.Execute(ctx, buildModelRequest(blind, tmpl, blind.CheckpointID+"-propose"))
	if err != nil {
		return CRCReport{}, record, wrapDispatch(err, "internal/review: CR-C propose dispatch failed")
	}
	challengeResp, err := exec.Execute(ctx, buildChallengeRequest(blind, tmpl, proposeResp.Output))
	if err != nil {
		return CRCReport{}, record, wrapDispatch(err, "internal/review: CR-C challenge dispatch failed")
	}
	propose := resolveFamily(ctx, reg, proposeResp.Selection)
	challenge := resolveFamily(ctx, reg, challengeResp.Selection)
	record.ReviewerFamilyDistinct, record.DistinctnessReason = observedDistinct(propose, challenge)
	if !record.ReviewerFamilyDistinct && blind.ConsequenceClass.HoldsWithoutDistinctFamily() {
		return CRCReport{}, record, sameFamilyRefusal(blind.ConsequenceClass, propose, challenge)
	}
	if !record.ReviewerFamilyDistinct {
		record.SameFamilyFallback = true
		publishFallback(ctx, events, FallbackEvent{
			Level: provider.ReviewCRLevelC, ConsequenceClass: blind.ConsequenceClass,
			Families: []string{propose.label()}, Reason: fallbackReasonSingleFamily,
		})
	}
	report, err := parseArbitration(challengeResp.Output, plan.Checks)
	if err != nil {
		return CRCReport{}, record, err
	}
	if note, ok := exclusionNote(plan.Excluded); ok {
		report.Findings = append(report.Findings, note)
	}
	return report, record, nil
}
