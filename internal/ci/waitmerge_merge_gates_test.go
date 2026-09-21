// Purpose: the four merge-on-green gates the adversarial review found
// unproven or unprovable -- the DISTINCT grant (D4), the L3 rung asserted
// through MergeOnGreen itself rather than through the fixture (D5), the
// audit record written BEFORE the irreversible call plus the data-class
// declaration (D6), and the moved-head TOCTOU refusal (D8). Split from
// waitmerge_merge_test.go under Art.10.3's 300-line cap.
//
// SPORT: internal.ci.MergeOnGreen/TESTED (P1-E25-W5-S51-T3).
package ci

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestMergeOnGreen_HumanMergeGrantDoesNotAuthorizeUnattendedMerge is the
// REVIEW'S OWN INPUT (finding 4): a subject holding a plain
// cascade-github.prs.merge grant -- issued so a HUMAN could merge one pull
// request -- must NOT silently authorize an unattended merge-on-green loop.
//
// MUTATION TARGET (c): pointing mergeEvalRequest's Capability back at
// MergeCapability makes this test merge on the human grant and turn red.
func TestMergeOnGreen_HumanMergeGrantDoesNotAuthorizeUnattendedMerge(t *testing.T) {
	f := newMergeFixture(t)
	subject := testSubjectForMerge()
	f.grant(t, subject, MergeCapability)
	f.caller.resp = []byte(`{"sha":"cafef00d","merged":true,"message":""}`)
	opts := baseMergeOptions()
	opts.Subject = subject

	_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if !cascade.HasKind(err, cascade.KindPolicyDenied) {
		t.Fatalf("err = %v, want KindPolicyDenied: a human-merge grant is not a merge-on-green grant", err)
	}
	if !strings.Contains(err.Error(), MergeOnGreenCapability) {
		t.Fatalf("err = %q, want it to name the capability actually required (%s)", err.Error(), MergeOnGreenCapability)
	}
	if len(f.caller.calls) != 0 {
		t.Fatalf("caller.calls = %d, want 0", len(f.caller.calls))
	}
	assertPolicyDenyAudited(t, f)

	// The distinct grant, on the same subject, does let it through.
	f.grant(t, subject, MergeOnGreenCapability)
	if _, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref)); err != nil {
		t.Fatalf("with the merge-on-green grant: err = %v, want nil", err)
	}
	if len(f.caller.calls) != 1 {
		t.Fatalf("caller.calls = %d, want exactly 1 once the distinct grant exists", len(f.caller.calls))
	}
}

// TestMergeOnGreen_ClassifiesL3 proves acceptance criterion 5 THROUGH
// MergeOnGreen (the review's finding 5: the old test asserted the fixture's
// own registration and never called the production function). The
// capability is registered one rung lower here, and the production path
// must refuse rather than merge under the wrong rung.
//
// MUTATION TARGET: deleting authorize's `outcome.Level != policy.L3` guard
// turns this green -- the merge would proceed at L1.
func TestMergeOnGreen_ClassifiesL3(t *testing.T) {
	// First: the shipped registration really is L3.
	f := newMergeFixture(t)
	outcome, err := f.engine.Evaluate(context.Background(), mergeEvalRequest(baseMergeOptions()))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if outcome.Level != policy.L3 {
		t.Fatalf("Level = %s, want L3 for the production capability class", outcome.Level)
	}

	// Then: MergeOnGreen refuses when the rung it gets back is not L3.
	low := newMergeFixtureAtClass(t, policy.ClassLocalDev)
	subject := testSubjectForMerge()
	low.grant(t, subject, MergeOnGreenCapability)
	low.caller.resp = []byte(`{"sha":"cafef00d","merged":true,"message":""}`)
	opts := baseMergeOptions()
	opts.Subject = subject

	_, err = MergeOnGreen(context.Background(), low.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if !cascade.HasKind(err, cascade.KindInternal) {
		t.Fatalf("err = %v, want the wrong-rung refusal", err)
	}
	if !strings.Contains(err.Error(), "want L3") {
		t.Fatalf("err = %q, want it to name the rung it required", err.Error())
	}
	if len(low.caller.calls) != 0 {
		t.Fatalf("caller.calls = %d, want 0 -- nothing merges under the wrong rung", len(low.caller.calls))
	}
}

// TestMergeOnGreen_AuditsTheDecisionBeforeTheCall is the REVIEW'S OWN INPUT
// (finding 6): a kill during Caller.Call must not leave an executed L3
// external side effect with no record. The call FAILS here, and the
// pre-dispatch policy.decide record must already exist.
//
// MUTATION TARGET (d): moving the pre-call record to after callMerge turns
// this red, because the only policy.decide row would then be absent.
func TestMergeOnGreen_AuditsTheDecisionBeforeTheCall(t *testing.T) {
	f, opts := grantedFixture(t)
	f.caller.err = cascade.New(cascade.KindUnavailable, "killed mid-dispatch")
	// Observed INSIDE Call: a kill here must already have left a record.
	var decideAtDispatch []string
	f.caller.onCall = func() { decideAtDispatch = auditOutcomes(t, f, audit.KindPolicyDecide) }

	if _, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref)); err == nil {
		t.Fatal("err = nil, want the transport failure")
	}
	if len(decideAtDispatch) != 1 || !strings.Contains(decideAtDispatch[0], "dispatching "+mergeToolMethod) {
		t.Fatalf("policy.decide rows AT THE MOMENT OF DISPATCH = %v, want the allow record already sealed", decideAtDispatch)
	}
	decide := auditOutcomes(t, f, audit.KindPolicyDecide)
	if len(decide) != 1 || !strings.Contains(decide[0], "dispatching "+mergeToolMethod) {
		t.Fatalf("policy.decide outcomes = %v, want one pre-dispatch allow record", decide)
	}
	route := auditOutcomes(t, f, audit.KindPolicyRoute)
	if len(route) != 1 || !strings.Contains(route[0], "merge call failed") {
		t.Fatalf("policy.route outcomes = %v, want one post-call failure record", route)
	}
}

// TestMergeOnGreen_AuditsTheCompletedMerge proves the success half of the
// same pair: the decision, then what actually happened.
func TestMergeOnGreen_AuditsTheCompletedMerge(t *testing.T) {
	f, opts := grantedFixture(t)
	f.caller.resp = []byte(`{"sha":"cafef00d","merged":true,"message":""}`)
	if _, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref)); err != nil {
		t.Fatalf("MergeOnGreen: %v", err)
	}
	route := auditOutcomes(t, f, audit.KindPolicyRoute)
	if len(route) != 1 || !strings.Contains(route[0], "merged cafef00d") {
		t.Fatalf("policy.route outcomes = %v, want the merged SHA", route)
	}
	records := auditRecords(t, f, audit.KindPolicyRoute)
	if records[0].RiskLevel != policy.L3.String() {
		t.Fatalf("RiskLevel = %q, want %q", records[0].RiskLevel, policy.L3.String())
	}
	if records[0].ParamsHash == "" {
		t.Fatal("ParamsHash is empty: the record does not bind to what was dispatched")
	}
}

// TestMergeOnGreen_DataClassAboveTheDestinationCeilingIsDenied proves the
// 06 §5.16 inheritance is REAL rather than nominal (finding 6's second
// half): with LaneMaxDataClass declared, policy layer 0 actually runs, so a
// calling thread carrying confidential material cannot push it to GitHub.
func TestMergeOnGreen_DataClassAboveTheDestinationCeilingIsDenied(t *testing.T) {
	f, opts := grantedFixture(t)
	f.caller.resp = []byte(`{"sha":"cafef00d","merged":true,"message":""}`)
	opts.DataClass = policy.DataClassConfidential

	_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if err == nil {
		t.Fatal("err = nil, want a layer-0 data-class refusal")
	}
	if len(f.caller.calls) != 0 {
		t.Fatalf("caller.calls = %d, want 0", len(f.caller.calls))
	}
}

// TestMergeOnGreen_RefusesWhenTheHeadMoved is D8: the ref that went green is
// re-read immediately before the merge, and a ref that moved in between is
// refused rather than merged.
//
// MUTATION TARGET (f): deleting the recheckHead call turns this green.
func TestMergeOnGreen_RefusesWhenTheHeadMoved(t *testing.T) {
	f, opts := grantedFixture(t)
	f.caller.resp = []byte(`{"sha":"cafef00d","merged":true,"message":""}`)
	f.headSHA = "0ddba11" // the branch moved after the wait resolved

	_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if !cascade.HasKind(err, cascade.KindConflict) {
		t.Fatalf("err = %v, want KindConflict for a moved head", err)
	}
	if !strings.Contains(err.Error(), "0ddba11") || !strings.Contains(err.Error(), "deadbeef") {
		t.Fatalf("err = %q, want both the current and the green SHA named", err.Error())
	}
	if len(f.caller.calls) != 0 {
		t.Fatalf("caller.calls = %d, want 0 -- a moved ref must never be merged", len(f.caller.calls))
	}
	assertPolicyDenyAudited(t, f)
}

// TestMergeOnGreen_RefusesWhenTheHeadCannotBeReRead proves the fetcher's
// own failure is a refusal, not an assumption that the ref stood still.
func TestMergeOnGreen_RefusesWhenTheHeadCannotBeReRead(t *testing.T) {
	f, opts := grantedFixture(t)
	f.headSHAErr = cascade.New(cascade.KindUnavailable, "304 Not Modified")

	_, err := MergeOnGreen(context.Background(), f.deps(), opts, passedWait(opts.Owner, opts.Repo, opts.Ref))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if len(f.caller.calls) != 0 {
		t.Fatalf("caller.calls = %d, want 0", len(f.caller.calls))
	}
}

// auditRecords reads back every record of kind from the real audit log.
func auditRecords(t *testing.T, f *mergeFixture, kind audit.Kind) []audit.Record {
	t.Helper()
	page, err := f.audit.Query(context.Background(), audit.Filter{Kinds: []audit.Kind{kind}})
	if err != nil {
		t.Fatalf("Query(%s): %v", kind, err)
	}
	return page.Records
}

// auditOutcomes is auditRecords' Outcome projection.
func auditOutcomes(t *testing.T, f *mergeFixture, kind audit.Kind) []string {
	t.Helper()
	records := auditRecords(t, f, kind)
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.Outcome)
	}
	return out
}
