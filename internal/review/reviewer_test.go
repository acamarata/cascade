package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// recordingEvents is a EventPublisher double: it keeps every published
// event so a test can assert the fallback WAS logged, and that a non-fallback
// path published nothing.
type recordingEvents struct{ events []Event }

func (r *recordingEvents) Publish(_ context.Context, ev Event) {
	r.events = append(r.events, ev)
}

func (r *recordingEvents) fallbacks() []*FallbackEvent {
	var out []*FallbackEvent
	for _, e := range r.events {
		if e.Fallback != nil {
			out = append(out, e.Fallback)
		}
	}
	return out
}

// TestCRCFanOut_ChallengeReceivesPropose is the ticket's named root test:
// the challenge pass's dispatched prompt contains the propose pass's
// Output VERBATIM -- byte for byte, no silent truncation or drop. A
// distinctive, otherwise-improbable marker string proves this can fail: a
// truncating implementation would cut it off, and a summarizing one would
// paraphrase it away.
func TestCRCFanOut_ChallengeReceivesPropose(t *testing.T) {
	const proposeMarker = "PROPOSE_PASS_VERBATIM_MARKER_7f3a9c2e_do_not_summarize_this_exact_string"
	proposeOutput := `{"approved":true,"findings":[]}` + "\n<!-- " + proposeMarker + " -->"

	exec := &fakeExecutor{responses: []provider.ModelResponse{
		{Output: proposeOutput, Selection: provider.Selection{Provider: "anthropic-acc1"}},
		{Output: `{"approved":false,"verdict":"reject","dissent":"d","findings":[]}`, Selection: provider.Selection{Provider: "openai-acc1"}},
	}}
	plan := mustPlan(t, provider.ReviewCRLevelC, ConsequenceHigh, "diff --git a/x.go b/x.go\n+x", "")

	if _, _, err := CRC(context.Background(), plan, exec, twoFamilyRegistry(), nil); err != nil {
		t.Fatalf("CRC: %v", err)
	}
	if len(exec.calls) != 2 {
		t.Fatalf("CRC dispatched %d times, want exactly 2 (propose, challenge)", len(exec.calls))
	}
	challengePrompt := exec.calls[1].Inputs[0].Content
	if !strings.Contains(challengePrompt, proposeOutput) {
		t.Errorf("challenge prompt does not contain propose's Output verbatim.\npropose output: %s\nchallenge prompt: %s", proposeOutput, challengePrompt)
	}
	// The marker alone is not enough -- prove the FULL propose output,
	// including its JSON wrapper, survived unmodified (catches a
	// truncation that keeps only the trailing comment).
	if idx := strings.Index(challengePrompt, proposeOutput); idx < 0 {
		t.Error("propose output does not appear as one contiguous, unmodified substring of the challenge prompt")
	}
}

// TestReviewHighConsequenceHolds is the ticket's named root test: a High
// consequence_class with no eligible distinct family HOLDS -- returns
// ErrNoEligibleReviewerFamily, dispatches nothing -- rather than falling
// back to the same family.
func TestReviewHighConsequenceHolds(t *testing.T) {
	oneFamily := fakeRegistry{infos: []provider.ProviderInfo{{Name: "anthropic-acc1", Driver: "anthropic"}}}
	exec := &fakeExecutor{responses: []provider.ModelResponse{
		{Output: `{"approved":true,"findings":[]}`},
	}}
	plan := mustPlan(t, provider.ReviewCRLevelC, ConsequenceHigh, "diff --git a/x.go b/x.go\n+x", "")

	_, record, err := Review(context.Background(), plan, provider.ReviewCRLevelC, exec, oneFamily, nil)
	if err != ErrNoEligibleReviewerFamily {
		t.Fatalf("Review error = %v, want the exact ErrNoEligibleReviewerFamily sentinel", err)
	}
	if len(exec.calls) != 0 {
		t.Errorf("Review dispatched %d times on a High-consequence HOLD, want 0 -- a HOLD must never fall back same-family", len(exec.calls))
	}
	if record.ConsequenceClass != ConsequenceHigh {
		t.Errorf("record.ConsequenceClass = %q, want %q", record.ConsequenceClass, ConsequenceHigh)
	}

	// Critical behaves identically.
	planCrit := mustPlan(t, provider.ReviewCRLevelC, ConsequenceCritical, "diff --git a/x.go b/x.go\n+x", "")
	if _, _, err := Review(context.Background(), planCrit, provider.ReviewCRLevelC, exec, oneFamily, nil); err != ErrNoEligibleReviewerFamily {
		t.Fatalf("Critical: Review error = %v, want ErrNoEligibleReviewerFamily", err)
	}
}

// TestReviewNormalConsequenceFallsBackAndLogs proves the R-21.156(b)
// narrowed fallback AND the AC's "used and LOGGED, never silent" half (CR fix
// D5): below High/Critical one eligible family is enough, the dispatch
// proceeds, and the fallback is PUBLISHED through the injected event seam. The
// rejected draft had this test name asserting no log at all.
func TestReviewNormalConsequenceFallsBackAndLogs(t *testing.T) {
	oneFamily := fakeRegistry{infos: []provider.ProviderInfo{{Name: "anthropic-acc1", Driver: "anthropic"}}}
	exec := &fakeExecutor{responses: []provider.ModelResponse{
		{Output: `{"approved":true,"findings":[{"severity":"nit","file":"x.go","line":1,"message":"m"}]}`, Selection: provider.Selection{Provider: "anthropic-acc1"}},
	}}
	events := &recordingEvents{}
	plan := mustPlan(t, provider.ReviewCRLevelB, ConsequenceNormal, "diff --git a/x.go b/x.go\n+x", "")

	resp, record, err := Review(context.Background(), plan, provider.ReviewCRLevelB, exec, oneFamily, events)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(exec.calls) != 1 {
		t.Errorf("Review dispatched %d times, want 1 (fallback proceeds, it does not hold)", len(exec.calls))
	}
	if !record.SameFamilyFallback {
		t.Error("record.SameFamilyFallback = false, want true: only one family was eligible")
	}
	if len(resp.Findings) == 0 {
		t.Error("expected a non-empty finding list")
	}
	fbs := events.fallbacks()
	if len(fbs) != 1 {
		t.Fatalf("published %d fallback events, want exactly 1 -- a silent fallback is the defect", len(fbs))
	}
	if fbs[0].Level != provider.ReviewCRLevelB || fbs[0].ConsequenceClass != ConsequenceNormal {
		t.Errorf("fallback event = %+v, want it to name CR-B at consequence normal", *fbs[0])
	}
	if len(fbs[0].Families) != 1 || fbs[0].Families[0] != "anthropic" {
		t.Errorf("fallback event families = %v, want the one registered family", fbs[0].Families)
	}
	if fbs[0].Reason == "" {
		t.Error("the fallback event carries no reason")
	}
}

// TestReviewTwoFamiliesPublishesNoFallback is the "absent otherwise" half of
// the same assertion: with two families registered nothing is published, so
// the event above cannot be a constant.
func TestReviewTwoFamiliesPublishesNoFallback(t *testing.T) {
	exec := &fakeExecutor{responses: []provider.ModelResponse{
		{Output: `{"approved":true,"findings":[{"severity":"nit","file":"x.go","line":1,"message":"m"}]}`, Selection: provider.Selection{Provider: "anthropic-acc1"}},
	}}
	events := &recordingEvents{}
	plan := mustPlan(t, provider.ReviewCRLevelB, ConsequenceNormal, "diff --git a/x.go b/x.go\n+x", "")
	if _, _, err := Review(context.Background(), plan, provider.ReviewCRLevelB, exec, twoFamilyRegistry(), events); err != nil {
		t.Fatalf("Review: %v", err)
	}
	if fbs := events.fallbacks(); len(fbs) != 0 {
		t.Errorf("published %d fallback events on a two-family registry, want 0: %+v", len(fbs), fbs)
	}
}

// TestReviewLowConsequenceSkipsFamilyGate proves CR-A/Low never even
// queries the registry -- a registry failure must not block the
// lightweight, per-hunk tier.
func TestReviewLowConsequenceSkipsFamilyGate(t *testing.T) {
	failingRegistry := fakeRegistry{err: errBoom}
	exec := &fakeExecutor{responses: []provider.ModelResponse{
		{Output: `{"approved":true,"findings":[{"severity":"nit","file":"x.go","line":1,"message":"m"}]}`},
	}}
	plan := mustPlan(t, provider.ReviewCRLevelA, ConsequenceLow, "diff --git a/x.go b/x.go\n+x", "")
	if _, _, err := Review(context.Background(), plan, provider.ReviewCRLevelA, exec, failingRegistry, nil); err != nil {
		t.Fatalf("Review (Low consequence) should never consult a failing registry: %v", err)
	}
}

// TestCRCReportHasVerdictAndDissent proves CRCReport -- the richer
// entry point, since pkg/provider.ReviewResponse cannot carry these two
// fields -- actually populates both Verdict and Dissent from the challenge
// pass's structured output.
func TestCRCReportHasVerdictAndDissent(t *testing.T) {
	plan := mustPlan(t, provider.ReviewCRLevelC, ConsequenceHigh, "diff --git a/x.go b/x.go\n+x", "")
	exec := &fakeExecutor{responses: []provider.ModelResponse{
		{Output: `{"approved":false,"findings":[]}`, Selection: provider.Selection{Provider: "anthropic-acc1"}},
		{Output: `{"approved":false,"verdict":"reject","dissent":"disagree on severity","findings":[{"severity":"blocker","file":"x.go","line":1,"message":"race"}]}`, Selection: provider.Selection{Provider: "openai-acc1"}},
	}}
	report, record, err := CRC(context.Background(), plan, exec, twoFamilyRegistry(), nil)
	if err != nil {
		t.Fatalf("CRC: %v", err)
	}
	if report.Verdict == "" || report.Dissent == "" {
		t.Errorf("CRCReport missing verdict/dissent: %+v", report)
	}
	if !record.ReviewerFamilyDistinct {
		t.Error("record.ReviewerFamilyDistinct = false, want true: propose and challenge resolved to different families")
	}
}

// TestCRCDispatchFailures covers the propose-failure,
// challenge-failure and unparseable-challenge-output branches CRC's
// happy-path tests never exercise.
func TestCRCDispatchFailures(t *testing.T) {
	plan := mustPlan(t, provider.ReviewCRLevelC, ConsequenceHigh, "diff --git a/x.go b/x.go\n+x", "")

	t.Run("propose fails", func(t *testing.T) {
		exec := &fakeExecutor{errs: []error{errBoom}}
		if _, _, err := CRC(context.Background(), plan, exec, twoFamilyRegistry(), nil); err == nil {
			t.Error("CRC returned nil error on a propose dispatch failure")
		}
	})
	t.Run("challenge fails", func(t *testing.T) {
		exec := &fakeExecutor{
			responses: []provider.ModelResponse{{Output: `{"approved":true,"findings":[]}`}},
			errs:      []error{nil, errBoom},
		}
		if _, _, err := CRC(context.Background(), plan, exec, twoFamilyRegistry(), nil); err == nil {
			t.Error("CRC returned nil error on a challenge dispatch failure")
		}
	})
	t.Run("challenge output unparseable", func(t *testing.T) {
		// Distinct families on purpose: otherwise the D2(b) same-family
		// HOLD fires first and this subtest would pass without ever
		// reaching the parser it exists to exercise.
		exec := &fakeExecutor{responses: []provider.ModelResponse{
			{Output: `{"approved":true,"findings":[]}`, Selection: provider.Selection{Provider: "anthropic-acc1"}},
			{Output: "not json", Selection: provider.Selection{Provider: "openai-acc1"}},
		}}
		_, _, err := CRC(context.Background(), plan, exec, twoFamilyRegistry(), nil)
		if err == nil {
			t.Fatal("CRC returned nil error on an unparseable challenge output")
		}
		if errors.Is(err, ErrNoEligibleReviewerFamily) {
			t.Fatalf("CRC failed on the family gate, not the parser: %v", err)
		}
	})
}

// TestParseArbitrationInvalid covers parseArbitration's own malformed/
// invalid-severity branches (parseFindings' equivalent coverage lives in
// provider_test.go's TestParseFindingsInvalid).
func TestParseArbitrationInvalid(t *testing.T) {
	if _, err := parseArbitration("not json", Checklist{}); err == nil {
		t.Error("parseArbitration(malformed JSON) returned nil error")
	}
	if _, err := parseArbitration(`{"approved":true,"findings":[{"severity":"catastrophic"}]}`, Checklist{}); err == nil {
		t.Error("parseArbitration(invalid severity) returned nil error")
	}
}

// The Art.2 real-counterpart test moved to codec_test.go
// (TestReviewProviderRealCounterpart_WireCodec). The version that lived here
// decoded the fixture through this file's OWN mirror struct and asserted
// nothing about task_class, requirements or sensitivity, so a fixture that had
// drifted off protocol would still have passed -- the CR's #11 finding. The
// replacement encodes the reviewer's request with the REAL
// pkg/provider.Client.ModelExecute codec and grounds every wire field against
// conductor.TaskClasses().

// TestEligibleFamiliesDeduplicates proves eligibleFamilies collapses
// repeated driver names rather than over-counting pool members of the same
// family as distinct families.
func TestEligibleFamiliesDeduplicates(t *testing.T) {
	reg := fakeRegistry{infos: []provider.ProviderInfo{
		{Name: "anthropic-acc1", Driver: "anthropic"},
		{Name: "anthropic-acc2", Driver: "anthropic"},
		{Name: "openai-acc1", Driver: "openai-compat"},
	}}
	families, err := eligibleFamilies(context.Background(), reg)
	if err != nil {
		t.Fatalf("eligibleFamilies: %v", err)
	}
	if len(families) != 2 {
		t.Fatalf("eligibleFamilies = %v, want exactly 2 distinct families", families)
	}
}
