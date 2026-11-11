package review

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestJobReviewPolicyRule checks the job adapter's gates and consequence rules.
func TestJobReviewPolicyRule(t *testing.T) {
	for _, tc := range []struct {
		risk           jobs.RiskClass
		class          ConsequenceClass
		gates          int
		distinct, hold bool
	}{
		{jobs.RiskClassLow, ConsequenceLow, 0, false, false},
		{jobs.RiskClassNormal, ConsequenceNormal, 1, true, false},
		{jobs.RiskClassHigh, ConsequenceHigh, 2, true, true},
		{jobs.RiskClassCritical, ConsequenceCritical, 2, true, true},
	} {
		t.Run(string(tc.risk), func(t *testing.T) {
			p, err := NewJobReviewPolicy(tc.risk)
			if err != nil {
				t.Fatal(err)
			}
			if p.Class != tc.class || p.ReviewGates != tc.gates ||
				p.RequiresDistinctFamily() != tc.distinct || p.HoldsWithoutDistinctFamily() != tc.hold {
				t.Fatalf("policy = %+v, distinct=%v hold=%v", p, p.RequiresDistinctFamily(), p.HoldsWithoutDistinctFamily())
			}
			if p.Source != "internal/review.ConsequenceClass@1" {
				t.Fatalf("source = %q", p.Source)
			}
			if len(p.Checklist) != 10 || !reflect.DeepEqual(p.Checklist, ChecklistV1Dimensions()) {
				t.Fatalf("checklist = %v", p.Checklist)
			}
			p.Checklist[0] = "changed"
			next, err := NewJobReviewPolicy(tc.risk)
			if err != nil || !reflect.DeepEqual(next.Checklist, ChecklistV1Dimensions()) {
				t.Fatalf("checklist mutation leaked: %+v, %v", next, err)
			}
		})
	}
	t.Run("unknown", testUnknownJobRiskClass)
}

func testUnknownJobRiskClass(t *testing.T) {
	if ErrUnknownRiskClass.Error() != "invalid-input: internal/review: unknown job risk class" {
		t.Fatalf("sentinel = %v", ErrUnknownRiskClass)
	}
	for _, rc := range []jobs.RiskClass{"bogus", ""} {
		p, err := NewJobReviewPolicy(rc)
		if errors.Unwrap(err) != ErrUnknownRiskClass {
			t.Fatalf("risk %q: error = %v, want exact sentinel cause", rc, err)
		}
		want := fmt.Sprintf("invalid-input: %q: %s", rc, ErrUnknownRiskClass)
		if err.Error() != want || !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Fatalf("error = %v, want %q", err, want)
		}
		if !reflect.DeepEqual(p, JobReviewPolicy{}) {
			t.Fatalf("invalid policy = %+v", p)
		}
	}
}

func jobFamilyRegistry() fakeRegistry {
	return fakeRegistry{infos: []provider.ProviderInfo{
		{Name: "anthropic-acc1", Driver: "anthropic"},
		{Name: "anthropic-acc2", Driver: "anthropic"},
		{Name: "openai-1", Driver: "openai"},
		{Name: "empty-driver"},
	}}
}

type jobLookupErrorRegistry struct{ fakeRegistry }

func (jobLookupErrorRegistry) GetProvider(context.Context, string) (provider.ProviderInfo, error) {
	return provider.ProviderInfo{Driver: "must-not-resolve"}, errBoom
}

// TestProviderFamilyFailsClosed proves accounts are resolved to driver families.
func TestProviderFamilyFailsClosed(t *testing.T) {
	reg := jobFamilyRegistry()
	for _, tc := range []struct {
		name string
		reg  provider.ProviderRegistryReader
		want jobs.FamilyRef
	}{
		{"anthropic-acc1", reg, jobs.FamilyRef{Family: "anthropic", Resolved: true}},
		{"anthropic-acc2", reg, jobs.FamilyRef{Family: "anthropic", Resolved: true}},
		{"openai-1", reg, jobs.FamilyRef{Family: "openai", Resolved: true}},
		{"unknown", reg, jobs.FamilyRef{}},
		{"", reg, jobs.FamilyRef{}},
		{"empty-driver", reg, jobs.FamilyRef{}},
		{"anthropic-acc1", nil, jobs.FamilyRef{}},
		{"anthropic-acc1", jobLookupErrorRegistry{reg}, jobs.FamilyRef{}},
	} {
		if got := ProviderFamily(context.Background(), tc.reg, tc.name); got != tc.want {
			t.Errorf("ProviderFamily(%q, %T) = %+v, want %+v", tc.name, tc.reg, got, tc.want)
		}
	}
}

// TestAvailableFamilies checks ordered deduplication and registry failures.
func TestAvailableFamilies(t *testing.T) {
	ctx := context.Background()
	got, err := AvailableFamilies(ctx, jobFamilyRegistry())
	if err != nil || !reflect.DeepEqual(got, []string{"anthropic", "openai"}) {
		t.Fatalf("families = %v, %v", got, err)
	}
	_, err = AvailableFamilies(ctx, fakeRegistry{err: errBoom})
	if errors.Unwrap(err) != errBoom || !cascade.HasKind(err, cascade.KindUnavailable) ||
		err.Error() != "unavailable: internal/review: list provider families: boom" {
		t.Fatalf("list error = %v", err)
	}
	_, err = AvailableFamilies(ctx, nil)
	if err != ErrNoProviderRegistry || err.Error() != "unavailable: internal/review: no provider registry" {
		t.Fatalf("nil registry error = %v", err)
	}
	p, err := NewJobReviewPolicy(jobs.RiskClassHigh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.CheckAvailable(ctx, nil, jobs.FamilyRef{}); err != ErrNoProviderRegistry {
		t.Fatalf("CheckAvailable nil registry error = %v", err)
	}
	if _, err = p.CheckAvailable(ctx, fakeRegistry{err: errBoom}, jobs.FamilyRef{}); errors.Unwrap(err) != errBoom {
		t.Fatalf("CheckAvailable list error = %v", err)
	}
}

// TestJobReviewPolicySatisfiesJobsInterface exercises the jobs interface directly.
func TestJobReviewPolicySatisfiesJobsInterface(t *testing.T) {
	for _, rc := range []jobs.RiskClass{jobs.RiskClassHigh, jobs.RiskClassLow} {
		p, err := NewJobReviewPolicy(rc)
		if err != nil {
			t.Fatal(err)
		}
		var source jobs.ReviewPolicySource = p
		degraded, err := jobs.RequireDistinctFamily(source, jobs.FamilyRef{Family: "anthropic", Resolved: true}, nil)
		if rc == jobs.RiskClassHigh {
			if err != jobs.ErrNoDistinctFamily || err.Error() != "elevation-required: jobs: no distinct reviewer family" {
				t.Fatalf("high error = %v", err)
			}
		} else if err != nil {
			t.Fatalf("low error = %v", err)
		}
		if degraded {
			t.Fatal("unexpected degradation")
		}
	}
}
