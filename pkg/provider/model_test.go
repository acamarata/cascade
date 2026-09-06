package provider_test

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestSensitivityTierZeroValueIsRestricted asserts R-21.264's explicit
// requirement: an unset SensitivityTier field reads as
// provider.SensitivityRestricted, never a permissive default.
func TestSensitivityTierZeroValueIsRestricted(t *testing.T) {
	var tier provider.SensitivityTier
	if tier != provider.SensitivityRestricted {
		t.Fatalf("zero value = %v, want SensitivityRestricted", tier)
	}
	if got, want := tier.String(), "restricted"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestSensitivityTierString(t *testing.T) {
	cases := []struct {
		tier provider.SensitivityTier
		want string
	}{
		{provider.SensitivityRestricted, "restricted"},
		{provider.SensitivityLocalOnly, "local-only"},
		{provider.SensitivityInternal, "internal"},
		{provider.SensitivityPublic, "public"},
		{provider.SensitivityTier(200), "invalid-sensitivity-tier"},
	}
	for _, tc := range cases {
		if got := tc.tier.String(); got != tc.want {
			t.Errorf("SensitivityTier(%d).String() = %q, want %q", tc.tier, got, tc.want)
		}
	}
}

func TestSensitivityTierValid(t *testing.T) {
	if !provider.SensitivityPublic.Valid() {
		t.Fatal("SensitivityPublic should be valid")
	}
	if provider.SensitivityTier(200).Valid() {
		t.Fatal("SensitivityTier(200) should be invalid")
	}
}

// TestSensitivityTierNormativeOrdering asserts the 06-FORGE-SPEC.md §5.16
// ordering local-only > restricted > internal > public, which is
// independent of each member's zero-based declaration value.
func TestSensitivityTierNormativeOrdering(t *testing.T) {
	if !provider.SensitivityLocalOnly.MoreRestrictiveThan(provider.SensitivityRestricted) {
		t.Fatal("local-only must rank more restrictive than restricted")
	}
	if !provider.SensitivityRestricted.MoreRestrictiveThan(provider.SensitivityInternal) {
		t.Fatal("restricted must rank more restrictive than internal")
	}
	if !provider.SensitivityInternal.MoreRestrictiveThan(provider.SensitivityPublic) {
		t.Fatal("internal must rank more restrictive than public")
	}
	if provider.SensitivityPublic.MoreRestrictiveThan(provider.SensitivityLocalOnly) {
		t.Fatal("public must never rank more restrictive than local-only")
	}
}

// fakeExecutor is a minimal ModelExecutor used only to assert
// ModelRequest/ModelResponse round-trip through an interface boundary.
type fakeExecutor struct {
	got provider.ModelRequest
}

func (f *fakeExecutor) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	f.got = req
	return provider.ModelResponse{
		JobID:     "job-1",
		Selection: provider.Selection{LaneID: "lane-a", Provider: "anthropic", Model: "test-model"},
		Output:    "hello",
		Usage:     provider.Usage{InputTokens: 3, OutputTokens: 1},
	}, nil
}

func TestModelExecutorRoundTrip(t *testing.T) {
	fake := &fakeExecutor{}
	var exec provider.ModelExecutor = fake
	req := provider.ModelRequest{
		TaskID:    "t-1",
		TaskClass: "chat",
		Inputs:    []provider.ChatMessage{{Role: "user", Content: "hi"}},
		Requirements: provider.Requirements{
			Reasoning: "low", Context: 8000, Structured: false,
		},
		Sensitivity: provider.SensitivityInternal,
		Policy:      provider.Policy{ExternalAllowed: true},
		RequiredCapabilities: provider.RequiredCapabilities{
			ToolUse: true,
		},
	}

	resp, err := exec.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if resp.JobID != "job-1" {
		t.Fatalf("JobID = %q, want job-1", resp.JobID)
	}
	if resp.Selection.LaneID != "lane-a" {
		t.Fatalf("Selection.LaneID = %q, want lane-a", resp.Selection.LaneID)
	}
	if resp.Output != "hello" {
		t.Fatalf("Output = %q, want hello", resp.Output)
	}
	if fake.got.TaskID != "t-1" {
		t.Fatalf("recorded TaskID = %q, want t-1", fake.got.TaskID)
	}
	if !fake.got.RequiredCapabilities.ToolUse {
		t.Fatal("recorded RequiredCapabilities.ToolUse should be true")
	}
}
