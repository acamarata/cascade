package review

import (
	"context"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// Purpose: AMD-20260916/6 (CR fix D3). The rejected draft implemented none of
//   this: req.Context was read only by the deleted sensitivity regex, and
//   buildModelRequest sent rubric + artifact, so a Context carrying required
//   checks was silently dropped. The two test names the amendment itself
//   fixes -- TestReviewerAnswersChecklist and TestReviewerRejectsUnexecutedCheck
//   -- are below, spelled exactly as the contract writes them.
// SPORT: internal/review.checklist-tests (ADD, P1-E25-W5-S52-T4).

// checklistContext is the documented Context form, in both spellings at once.
const checklistContext = "P1-E25-W5-S52-T4 review\n" +
	"DIMENSION: error paths are tested\n" +
	"DIMENSION: no Article-1 stubs\n" +
	"checks:\n" +
	"  - go test -race ./internal/review/...\n" +
	"  - golangci-lint run ./internal/review/...\n" +
	"CHECK: go vet ./internal/review/...\n"

// TestReviewerAnswersChecklist is the amendment's first named test: the
// checklist a caller declared reaches the DISPATCHED PROMPT (not merely this
// process's memory), and a response that answers every dimension and records
// every check produces no checklist violation of its own.
func TestReviewerAnswersChecklist(t *testing.T) {
	plan := mustPlan(t, provider.ReviewCRLevelB, ConsequenceNormal, "diff --git a/x.go b/x.go\n+x", checklistContext)
	if len(plan.Checks.Dimensions) != 2 {
		t.Fatalf("parsed dimensions = %v, want 2", plan.Checks.Dimensions)
	}
	if len(plan.Checks.RequiredChecks) != 3 {
		t.Fatalf("parsed required checks = %v, want 3 (two from the checks: block, one CHECK: line)",
			plan.Checks.RequiredChecks)
	}

	exec := &fakeExecutor{responses: []provider.ModelResponse{
		{Output: answeredOutput(), Selection: provider.Selection{Provider: "anthropic-acc1"}},
	}}
	p, err := NewProvider(exec, twoFamilyRegistry(), nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	resp, err := p.Review(context.Background(), provider.ReviewRequest{
		Level: provider.ReviewCRLevelB, Diff: "diff --git a/x.go b/x.go\n+x", Context: checklistContext,
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}

	prompt := exec.calls[0].Inputs[0].Content
	for _, want := range []string{
		"error paths are tested", "no Article-1 stubs",
		"go test -race ./internal/review/...", "golangci-lint run ./internal/review/...",
		"go vet ./internal/review/...", "satisfied | violated | not_applicable",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the dispatched prompt does not carry %q; a dropped checklist is the exact defect this test exists for", want)
		}
	}
	for _, f := range resp.Findings {
		if strings.Contains(f.Message, unexecutedCheckTitle) || strings.Contains(f.Message, "unanswered checklist dimension") {
			t.Errorf("a fully answered response still produced a checklist violation: %q", f.Message)
		}
	}
	if !resp.Approved {
		t.Error("a fully answered, approved response came back not approved")
	}
}

// answeredOutput is the structured response of a model that answered every
// dimension and recorded an execution for every required check.
func answeredOutput() string {
	return `{"approved":true,"findings":[],` +
		`"checklist":[` +
		`{"name":"error paths are tested","status":"satisfied","reason":"three error-path tests present"},` +
		`{"name":"no Article-1 stubs","status":"satisfied","reason":"no stub bodies in the diff"}],` +
		`"executed_checks":[` +
		`{"command":"go test -race ./internal/review/...","exit_code":0},` +
		`{"command":"golangci-lint run ./internal/review/...","exit_code":0},` +
		`{"command":"go vet ./internal/review/...","exit_code":0}]}`
}

// TestReviewerRejectsUnexecutedCheck is the amendment's second named test: a
// required check with NO execution record becomes a violated-D7 finding, and
// the response cannot come back approved. Three shapes of "trust me" are fed
// in, because each is a way a worker actually reports a check it never ran.
func TestReviewerRejectsUnexecutedCheck(t *testing.T) {
	for name, output := range unexecutedCheckOutputs() {
		t.Run(name, func(t *testing.T) {
			exec := &fakeExecutor{responses: []provider.ModelResponse{
				{Output: output, Selection: provider.Selection{Provider: "anthropic-acc1"}},
			}}
			p, err := NewProvider(exec, twoFamilyRegistry(), nil)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			resp, err := p.Review(context.Background(), provider.ReviewRequest{
				Level: provider.ReviewCRLevelB, Diff: "diff --git a/x.go b/x.go\n+x", Context: checklistContext,
			})
			if err != nil {
				t.Fatalf("Review: %v", err)
			}
			requireUnexecutedCheckFinding(t, resp)
		})
	}
}

// requireUnexecutedCheckFinding asserts the response carries at least one
// correctly-shaped unexecuted-check finding and is not approved.
func requireUnexecutedCheckFinding(t *testing.T, resp provider.ReviewResponse) {
	t.Helper()
	violations := 0
	for _, f := range resp.Findings {
		if !strings.HasPrefix(f.Message, unexecutedCheckTitle+":") {
			continue
		}
		violations++
		if f.Severity != provider.ReviewSeverityMajor {
			t.Errorf("unexecuted-check finding severity = %q, want %q", f.Severity, provider.ReviewSeverityMajor)
		}
		if !strings.Contains(f.Message, "violated D7") {
			t.Errorf("the finding %q does not cite D7 as AMD-20260916/6 requires", f.Message)
		}
	}
	if violations == 0 {
		t.Fatalf("no %q finding for a required check with no execution record; findings = %+v",
			unexecutedCheckTitle, resp.Findings)
	}
	if resp.Approved {
		t.Error("a response with an unexecuted required check came back approved")
	}
}

// unexecutedCheckOutputs is the three shapes of "trust me" a worker actually
// produces for a check it never ran. Split out of the test only because the two
// together exceed the 50-line function cap.
func unexecutedCheckOutputs() map[string]string {
	return map[string]string{
		"no executed_checks at all": `{"approved":true,"findings":[],` +
			`"checklist":[{"name":"error paths are tested","status":"satisfied","reason":"r"},` +
			`{"name":"no Article-1 stubs","status":"satisfied","reason":"r"}]}`,
		"command recorded with no exit code": `{"approved":true,"findings":[],` +
			`"checklist":[{"name":"error paths are tested","status":"satisfied","reason":"r"},` +
			`{"name":"no Article-1 stubs","status":"satisfied","reason":"r"}],` +
			`"executed_checks":[{"command":"go test -race ./internal/review/..."},` +
			`{"command":"golangci-lint run ./internal/review/..."},` +
			`{"command":"go vet ./internal/review/..."}]}`,
		"a different command than the one required": `{"approved":true,"findings":[],` +
			`"checklist":[{"name":"error paths are tested","status":"satisfied","reason":"r"},` +
			`{"name":"no Article-1 stubs","status":"satisfied","reason":"r"}],` +
			`"executed_checks":[{"command":"go build ./...","exit_code":0}]}`,
	}
}

// TestReviewerRejectsUnansweredDimension is the dimension half of the same
// rule: a dimension left unanswered, or answered outside the closed status
// vocabulary, or answered with no reason, is a violation.
func TestReviewerRejectsUnansweredDimension(t *testing.T) {
	answers := []wireChecklistAnswer{
		{Name: "error paths are tested", Status: "probably fine", Reason: "looks ok"},
		{Name: "no Article-1 stubs", Status: "satisfied", Reason: ""},
	}
	checks := ParseChecklist(checklistContext)
	missing := checks.unansweredDimensions(answers)
	if len(missing) != 2 {
		t.Fatalf("unansweredDimensions = %v, want both dimensions (one bad status, one empty reason)", missing)
	}
	findings := checks.Violations(answers, nil)
	if len(findings) != 5 {
		t.Fatalf("Violations = %d findings, want 5 (2 dimensions + 3 unexecuted checks); got %+v",
			len(findings), findings)
	}
}

// TestChecklistEmptyDemandsNothing proves the reviewer never invents an
// obligation: a caller who declared no checklist gets no checklist
// instructions in the prompt and no violation findings.
func TestChecklistEmptyDemandsNothing(t *testing.T) {
	checks := ParseChecklist("just an ordinary ticket description, no checklist here")
	if !checks.Empty() {
		t.Fatalf("ParseChecklist invented a checklist from ordinary prose: %+v", checks)
	}
	if got := checks.Instructions(); got != "" {
		t.Errorf("Instructions() = %q, want empty for an empty checklist", got)
	}
	if got := checks.Violations(nil, nil); got != nil {
		t.Errorf("Violations() = %+v, want none for an empty checklist", got)
	}
}
