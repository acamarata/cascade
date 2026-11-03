package review

import (
	"context"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// v1Diff is the fixture artifact every checklist v1 case reviews.
const v1Diff = "diff --git a/internal/review/provider.go b/internal/review/provider.go\n+bug here"

// v1Review runs one Review through a recording executor that answers every
// dispatch with output, and returns the response and the recorded prompts.
func v1Review(t *testing.T, level provider.ReviewCRLevel, reqContext, output string) (provider.ReviewResponse, []string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	rec := &fakeExecutor{}
	for i := 0; i < 2; i++ {
		rec.responses = append(rec.responses, provider.ModelResponse{
			Output: output, Selection: provider.Selection{Provider: []string{"anthropic-acc1", "openai-acc1"}[i]},
		})
	}
	p, err := NewProvider(rec, twoFamilyRegistry(), nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	resp, err := p.Review(context.Background(), provider.ReviewRequest{Level: level, Diff: v1Diff, Context: reqContext})
	if err != nil {
		t.Fatalf("Review(%s): %v", level, err)
	}
	if len(rec.calls) == 0 {
		t.Fatalf("Review(%s) dispatched nothing", level)
	}
	prompts := make([]string, 0, len(rec.calls))
	for _, c := range rec.calls {
		prompts = append(prompts, c.Inputs[0].Content)
	}
	return resp, prompts
}

// v1Answers renders a structured response answering every v1 dimension
// satisfied except violated, which is answered "violated".
func v1Answers(violated string) string {
	parts := make([]string, 0, len(checklistV1Dimensions))
	for _, d := range checklistV1Dimensions {
		status := "satisfied"
		if d == violated {
			status = "violated"
		}
		parts = append(parts, `{"name":"`+d+`","status":"`+status+`","reason":"checked `+d+`"}`)
	}
	return `{"approved":true,"findings":[],"checklist":[` + strings.Join(parts, ",") + `]}`
}

// TestProviderReviewAddsChecklistV1 proves CR-B and CR-C requests without a
// DIMENSION: line reach the model with the ten v1 dimensions, CR-A and
// self-declared requests are unchanged, and a violated dimension n yields a
// finding whose Message starts "[D<n>] ".
func TestProviderReviewAddsChecklistV1(t *testing.T) {
	for _, level := range []provider.ReviewCRLevel{provider.ReviewCRLevelB, provider.ReviewCRLevelC} {
		t.Run(string(level)+"/injected", func(t *testing.T) {
			_, prompts := v1Review(t, level, "scope: internal/review", v1Answers(""))
			for i, pr := range prompts {
				for _, d := range checklistV1Dimensions {
					if !strings.Contains(pr, "dimension "+d+":") {
						t.Errorf("dispatch %d prompt lacks v1 dimension %q", i, d)
					}
				}
			}
		})
		t.Run(string(level)+"/violated", func(t *testing.T) {
			resp, _ := v1Review(t, level, "", v1Answers("security"))
			if !hasMessagePrefix(resp.Findings, "[D3] ") {
				t.Fatalf("violated security (D3) produced no \"[D3] \" finding: %+v", resp.Findings)
			}
			if resp.Approved {
				t.Errorf("a violated v1 dimension left the review approved")
			}
		})
		t.Run(string(level)+"/self-declared", func(t *testing.T) {
			out := `{"approved":true,"findings":[],"checklist":[{"name":"own-dim","status":"violated","reason":"x"}]}`
			resp, prompts := v1Review(t, level, "DIMENSION: own-dim", out)
			for i, pr := range prompts {
				if !strings.Contains(pr, "dimension own-dim:") || strings.Contains(pr, "dimension correctness:") {
					t.Errorf("dispatch %d: self-declared checklist was altered", i)
				}
			}
			if hasMessagePrefix(resp.Findings, "[D") {
				t.Errorf("self-declared checklist produced a v1 finding: %+v", resp.Findings)
			}
		})
	}
	t.Run("CR-A/unchanged", func(t *testing.T) {
		resp, prompts := v1Review(t, provider.ReviewCRLevelA, "", v1Answers("security"))
		if strings.Contains(prompts[0], "CHECKLIST") || strings.Contains(prompts[0], "DIMENSION:") {
			t.Errorf("CR-A prompt gained a checklist: %q", prompts[0])
		}
		if hasMessagePrefix(resp.Findings, "[D") {
			t.Errorf("CR-A produced a v1 finding: %+v", resp.Findings)
		}
	})
}

// hasMessagePrefix reports whether any finding's Message starts with prefix.
func hasMessagePrefix(fs []provider.ReviewFinding, prefix string) bool {
	for _, f := range fs {
		if strings.HasPrefix(f.Message, prefix) {
			return true
		}
	}
	return false
}

// TestReviewContextCarriesChecklist proves ChecklistV1Context parses to
// exactly the ten v1 dimensions and that the public ABI file is untouched.
func TestReviewContextCarriesChecklist(t *testing.T) {
	got := ParseChecklist(ChecklistV1Context())
	want := []string{"correctness", "error-handling", "security", "concurrency", "tests",
		"scope", "contract", "simplicity", "performance", "docs"}
	if !reflect.DeepEqual(got.Dimensions, want) {
		t.Fatalf("ChecklistV1Context dimensions = %v, want %v", got.Dimensions, want)
	}
	if len(got.RequiredChecks) != 0 {
		t.Errorf("ChecklistV1Context declared checks %v, want none", got.RequiredChecks)
	}
	if !reflect.DeepEqual(ChecklistV1Dimensions(), want) {
		t.Errorf("ChecklistV1Dimensions() = %v, want %v", ChecklistV1Dimensions(), want)
	}
	if n := v1Number("Docs "); n != 10 {
		t.Errorf("v1Number(docs) = %d, want 10", n)
	}
	if n := v1Number("nope"); n != 0 {
		t.Errorf("v1Number(nope) = %d, want 0", n)
	}
	assertABIUnchanged(t, "../../pkg/provider/review_provider.go")
}

// assertABIUnchanged fails when git reports a working-tree or index change
// to path. Without git or a repository (an exported tree) it skips the
// check rather than guessing.
func assertABIUnchanged(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("public ABI file %s is missing: %v", path, err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Logf("git not available; ABI diff check skipped")
		return
	}
	out, err := exec.Command("git", "status", "--porcelain", "--", path).Output()
	if err != nil {
		t.Logf("git status unavailable (%v); ABI diff check skipped", err)
		return
	}
	if s := strings.TrimSpace(string(out)); s != "" {
		t.Errorf("pkg/provider/review_provider.go changed: %s", s)
	}
}
