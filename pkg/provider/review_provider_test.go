// Purpose: the runnable godoc Example for provider.ReviewProvider
//
//	(Art.10.6) and error-path/shape tests over ReviewCRLevel and
//	ReviewSeverity.
//
// Constraints: every double here exists ONLY in this _test.go file
//
//	(Art.1.1); no implementation ships from this ticket — Y/S-52.T4's
//	native adversarial reviewer builds the real one.
//
// SPORT: pkg.provider.ReviewProvider tests (ADD) — P1-E15-W4-S33-T1.
package provider_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// blockingReviewProvider is a contract-holding ReviewProvider double: it
// refuses an unsupported ReviewCRLevel and otherwise raises exactly one
// blocker finding for a diff containing the literal string "TODO".
type blockingReviewProvider struct{}

var _ provider.ReviewProvider = blockingReviewProvider{}

func (blockingReviewProvider) Review(_ context.Context, req provider.ReviewRequest) (provider.ReviewResponse, error) {
	if !req.Level.Valid() {
		return provider.ReviewResponse{}, cascade.Newf(cascade.KindInvalidInput, "unsupported review level %q", req.Level)
	}
	if req.Diff == "" {
		return provider.ReviewResponse{}, cascade.New(cascade.KindInvalidInput, "empty diff")
	}
	if containsTODO(req.Diff) {
		return provider.ReviewResponse{
			Findings: []provider.ReviewFinding{{
				Severity: provider.ReviewSeverityBlocker,
				File:     "example.go",
				Line:     1,
				Message:  "unresolved TODO in shipped diff",
			}},
			Approved: false,
		}, nil
	}
	return provider.ReviewResponse{Approved: true}, nil
}

func (blockingReviewProvider) Capabilities(_ context.Context, _ string) (provider.Capabilities, error) {
	return provider.Capabilities{
		CompliancePosture: provider.NewCompliancePosture(
			[]string{"api-key"}, false, true, []string{"agent"}, "steady", false,
		),
	}, nil
}

func containsTODO(diff string) bool {
	for i := 0; i+4 <= len(diff); i++ {
		if diff[i:i+4] == "TODO" {
			return true
		}
	}
	return false
}

func TestReviewCRLevel_Valid(t *testing.T) {
	for _, l := range []provider.ReviewCRLevel{provider.ReviewCRLevelA, provider.ReviewCRLevelB, provider.ReviewCRLevelC} {
		if !l.Valid() {
			t.Errorf("%v.Valid() = false, want true", l)
		}
	}
	if provider.ReviewCRLevel("CR-D").Valid() {
		t.Error(`ReviewCRLevel("CR-D").Valid() = true, want false`)
	}
}

func TestReviewSeverity_Valid(t *testing.T) {
	for _, s := range []provider.ReviewSeverity{
		provider.ReviewSeverityNit, provider.ReviewSeverityMinor,
		provider.ReviewSeverityMajor, provider.ReviewSeverityBlocker,
	} {
		if !s.Valid() {
			t.Errorf("%v.Valid() = false, want true", s)
		}
	}
	if provider.ReviewSeverity("catastrophic").Valid() {
		t.Error(`ReviewSeverity("catastrophic").Valid() = true, want false`)
	}
}

func TestReviewProvider_UnsupportedLevel(t *testing.T) {
	var rp provider.ReviewProvider = blockingReviewProvider{}
	_, err := rp.Review(context.Background(), provider.ReviewRequest{Level: "CR-D", Diff: "x"})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Review(unsupported level) err = %v, want KindInvalidInput", err)
	}
}

func TestReviewProvider_EmptyDiff(t *testing.T) {
	var rp provider.ReviewProvider = blockingReviewProvider{}
	_, err := rp.Review(context.Background(), provider.ReviewRequest{Level: provider.ReviewCRLevelA})
	if !cascade.HasKind(err, cascade.KindInvalidInput) {
		t.Fatalf("Review(empty diff) err = %v, want KindInvalidInput", err)
	}
}

func TestReviewProvider_BlockerFinding(t *testing.T) {
	var rp provider.ReviewProvider = blockingReviewProvider{}
	res, err := rp.Review(context.Background(), provider.ReviewRequest{
		Level: provider.ReviewCRLevelB, Diff: "+ // TODO: fix this",
	})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Approved {
		t.Fatal("Approved = true, want false for a blocker finding")
	}
	if len(res.Findings) != 1 || res.Findings[0].Severity != provider.ReviewSeverityBlocker {
		t.Fatalf("Findings = %+v, want exactly one blocker", res.Findings)
	}
}

// ExampleReviewProvider drives a CR-B review over a diff and prints the
// verdict.
func ExampleReviewProvider() {
	ctx := context.Background()
	var rp provider.ReviewProvider = blockingReviewProvider{}

	res, err := rp.Review(ctx, provider.ReviewRequest{
		Level:   provider.ReviewCRLevelB,
		Diff:    "+func f() {}\n",
		Context: "adds a no-op helper",
	})
	if err != nil {
		fmt.Println("review error:", err)
		return
	}

	fmt.Println(res.Approved, len(res.Findings))
	// Output: true 0
}
