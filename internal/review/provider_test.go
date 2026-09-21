package review

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// errBoom is a shared test-only sentinel simulating a collaborator failure.
var errBoom = errors.New("boom")

// fakeExecutor is a test-only provider.ModelExecutor double: it replays one
// queued (response, error) pair per call, in order, and records every
// dispatched request so a test can assert on it. Never used in production
// (internal/plugins/review_wiring.go wires the real seam).
type fakeExecutor struct {
	responses []provider.ModelResponse
	errs      []error
	calls     []provider.ModelRequest
}

func (f *fakeExecutor) Execute(_ context.Context, req provider.ModelRequest) (provider.ModelResponse, error) {
	i := len(f.calls)
	f.calls = append(f.calls, req)
	if i < len(f.errs) && f.errs[i] != nil {
		return provider.ModelResponse{}, f.errs[i]
	}
	if i < len(f.responses) {
		return f.responses[i], nil
	}
	return provider.ModelResponse{}, errors.New("fakeExecutor: no more responses queued")
}

// ctxCheckExecutor is a test-only provider.ModelExecutor double proving the
// context-cancellation error path: it returns ctx.Err() unchanged whenever
// ctx is already done, and never fabricates a different error.
type ctxCheckExecutor struct{}

func (ctxCheckExecutor) Execute(ctx context.Context, _ provider.ModelRequest) (provider.ModelResponse, error) {
	if err := ctx.Err(); err != nil {
		return provider.ModelResponse{}, err
	}
	return provider.ModelResponse{}, errors.New("ctxCheckExecutor: ctx was not cancelled")
}

// fakeRegistry is a test-only provider.ProviderRegistryReader double.
type fakeRegistry struct {
	infos []provider.ProviderInfo
	err   error
}

func (f fakeRegistry) GetProvider(_ context.Context, name string) (provider.ProviderInfo, error) {
	for _, i := range f.infos {
		if i.Name == name {
			return i, nil
		}
	}
	return provider.ProviderInfo{}, errors.New("fakeRegistry: not found")
}
func (f fakeRegistry) ListProviders(_ context.Context) ([]provider.ProviderInfo, error) {
	return f.infos, f.err
}
func (f fakeRegistry) ListLanes(context.Context) ([]provider.LaneInfo, error)        { return nil, nil }
func (f fakeRegistry) ListPool(context.Context, string) ([]provider.LaneInfo, error) { return nil, nil }
func (f fakeRegistry) GetByModel(context.Context, string) ([]provider.ProviderInfo, error) {
	return nil, nil
}

// twoFamilyRegistry is the fixture every non-HOLD test uses: two distinct
// provider families, satisfying R-21.156's cross-family requirement.
func twoFamilyRegistry() fakeRegistry {
	return fakeRegistry{infos: []provider.ProviderInfo{
		{Name: "anthropic-acc1", Driver: "anthropic"},
		{Name: "openai-acc1", Driver: "openai-compat"},
	}}
}

func findingsOutput(t *testing.T, approved bool, severity, message string) string {
	t.Helper()
	return `{"approved":` + boolStr(approved) + `,"findings":[{"severity":"` + severity + `","file":"internal/review/provider.go","line":42,"message":"` + message + `"}]}`
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// TestReviewProvider_AllLevels is the ticket's named root test: CR-A, CR-B
// and CR-C each produce a non-empty finding list against the same fixture
// diff, dispatched exclusively through the injected provider.ModelExecutor
// -- never a direct provider call. CR-C's own verdict/dissent proof lives
// in reviewer_test.go's TestReviewCRCReportHasVerdictAndDissent (same
// package): this test proves the ABI-frozen ReviewResponse path only.
func TestReviewProvider_AllLevels(t *testing.T) {
	diff := "diff --git a/internal/review/provider.go b/internal/review/provider.go\n+bug here"
	t.Run("CR-A", func(t *testing.T) { allLevelsCase(t, provider.ReviewCRLevelA, diff, 1) })
	t.Run("CR-B", func(t *testing.T) { allLevelsCase(t, provider.ReviewCRLevelB, diff, 1) })
	t.Run("CR-C", func(t *testing.T) { allLevelsCRCCase(t, diff) })
}

// allLevelsCase covers the single-dispatch levels (CR-A, CR-B): a
// non-empty finding list, dispatched exactly wantCalls times under the
// level's own task_class.
func allLevelsCase(t *testing.T, level provider.ReviewCRLevel, diff string, wantCalls int) {
	t.Helper()
	exec := &fakeExecutor{responses: []provider.ModelResponse{
		{Output: findingsOutput(t, false, "major", "unchecked error"), Selection: provider.Selection{Provider: "anthropic-acc1"}},
	}}
	p, err := NewProvider(exec, twoFamilyRegistry(), nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	resp, err := p.Review(context.Background(), provider.ReviewRequest{Level: level, Diff: diff})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(resp.Findings) == 0 {
		t.Fatalf("%s produced zero findings on a fixture that should surface one", level)
	}
	if len(exec.calls) != wantCalls {
		t.Fatalf("%s dispatched %d times, want exactly %d", level, len(exec.calls), wantCalls)
	}
	if exec.calls[0].TaskClass != "review" {
		t.Errorf("%s task_class = %q, want %q", level, exec.calls[0].TaskClass, "review")
	}
}

// allLevelsCRCCase covers CR-C: a non-empty finding list from the
// two-dispatch (propose, challenge) sequence under task_class "arbitrate".
func allLevelsCRCCase(t *testing.T, diff string) {
	t.Helper()
	exec := &fakeExecutor{responses: []provider.ModelResponse{
		{Output: `{"approved":false,"findings":[]}`, Selection: provider.Selection{Provider: "anthropic-acc1"}},
		{Output: `{"approved":false,"verdict":"reject: race condition","dissent":"understated blast radius","findings":[{"severity":"blocker","file":"x.go","line":1,"message":"race"}]}`, Selection: provider.Selection{Provider: "openai-acc1"}},
	}}
	p, err := NewProvider(exec, twoFamilyRegistry(), nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	resp, err := p.Review(context.Background(), provider.ReviewRequest{Level: provider.ReviewCRLevelC, Diff: diff})
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if len(resp.Findings) == 0 {
		t.Fatal("CR-C produced zero findings on a fixture that should surface one")
	}
	if exec.calls[0].TaskClass != "arbitrate" {
		t.Errorf("CR-C task_class = %q, want %q", exec.calls[0].TaskClass, "arbitrate")
	}
	if len(exec.calls) != 2 {
		t.Errorf("CR-C dispatched %d times, want exactly 2 (propose, challenge)", len(exec.calls))
	}
}

// TestReviewProvider_LocalOnlySensitivityRefused now lives in router_test.go:
// under the CR fix D1 the refusal is the REAL ROUTER's, driven by the thread
// privacy on the caller's ctx, not by a regex over the caller's free text, so
// the proof needs a real conductor.DefaultRouter rather than a fake executor.

// TestReviewProvider_ConductorFailure proves a conductor.execute failure
// propagates as a typed, non-nil error -- never a silent empty result.
func TestReviewProvider_ConductorFailure(t *testing.T) {
	exec := &fakeExecutor{errs: []error{errors.New("conductor: no candidate lane")}}
	p, err := NewProvider(exec, twoFamilyRegistry(), nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	_, err = p.Review(context.Background(), provider.ReviewRequest{Level: provider.ReviewCRLevelB, Diff: "diff --git a/x.go b/x.go\n+x"})
	if err == nil {
		t.Fatal("Review returned nil error on a conductor dispatch failure")
	}
	if !strings.Contains(err.Error(), "conductor.execute dispatch failed") {
		t.Errorf("error = %v, want it to name the dispatch failure", err)
	}
}

// TestReviewProvider_ContextCancellation proves a cancelled ctx's error
// reaches the caller unchanged, through the real Execute call, not a
// substituted or swallowed error.
func TestReviewProvider_ContextCancellation(t *testing.T) {
	p, err := NewProvider(ctxCheckExecutor{}, twoFamilyRegistry(), nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = p.Review(ctx, provider.ReviewRequest{Level: provider.ReviewCRLevelB, Diff: "diff --git a/x.go b/x.go\n+x"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Review error = %v, want it to wrap context.Canceled", err)
	}
}

// TestCapabilities covers the empty-lane structural report and the
// named-lane registry proxy, including the registry's own error path.
func TestCapabilities(t *testing.T) {
	p, err := NewProvider(&fakeExecutor{}, fakeRegistry{infos: []provider.ProviderInfo{
		{Name: "anthropic-acc1", Driver: "anthropic", Capabilities: provider.Capabilities{StructuredOutput: provider.CapabilitySupported}},
	}}, nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	caps, err := p.Capabilities(context.Background(), "")
	if err != nil {
		t.Fatalf("Capabilities(\"\"): %v", err)
	}
	if caps.StructuredOutput != provider.CapabilitySupported {
		t.Errorf("empty-lane Capabilities.StructuredOutput = %v, want CapabilitySupported", caps.StructuredOutput)
	}

	caps, err = p.Capabilities(context.Background(), "anthropic-acc1")
	if err != nil {
		t.Fatalf("Capabilities(named lane): %v", err)
	}
	if caps.StructuredOutput != provider.CapabilitySupported {
		t.Errorf("named-lane Capabilities did not proxy the registry's real value: %+v", caps)
	}

	if _, err := p.Capabilities(context.Background(), "does-not-exist"); err == nil {
		t.Error("Capabilities(unknown lane) returned nil error, want the registry's not-found error")
	}
}

// TestParseFindingsInvalid proves a malformed or semantically invalid
// structured output is a real, typed error -- never a silently-empty
// result standing in for a discarded model call. parseArbitration's own
// invalid-input coverage lives in reviewer_test.go.
func TestParseFindingsInvalid(t *testing.T) {
	if _, err := parseFindings("not json", Checklist{}); err == nil {
		t.Error("parseFindings(malformed JSON) returned nil error")
	}
	if _, err := parseFindings(`{"approved":true,"findings":[{"severity":"catastrophic","file":"x","line":1,"message":"m"}]}`, Checklist{}); err == nil {
		t.Error("parseFindings(invalid severity) returned nil error")
	}
}

// TestNewProviderRefusesNilCollaborators proves construction fails closed.
func TestNewProviderRefusesNilCollaborators(t *testing.T) {
	if _, err := NewProvider(nil, twoFamilyRegistry(), nil); err == nil {
		t.Error("NewProvider(nil executor, ...) returned nil error")
	}
	if _, err := NewProvider(&fakeExecutor{}, nil, nil); err == nil {
		t.Error("NewProvider(..., nil registry) returned nil error")
	}
}

// TestEmptyModelBodyIsATypedError closes a gap the fix lane's own mutation run
// exposed: nothing asserted that an EMPTY dispatch body is refused, so a parse
// path that treated "" as "no findings" passed the whole suite. An empty body
// is a dispatch whose result was never produced; reporting it as an approved,
// finding-free review is exactly the fires-and-forgets outcome parseFindings
// exists to prevent. Every level is covered, since CR-C decodes through
// parseArbitration instead.
func TestEmptyModelBodyIsATypedError(t *testing.T) {
	cases := map[provider.ReviewCRLevel][]provider.ModelResponse{
		provider.ReviewCRLevelA: {{Output: "", Selection: provider.Selection{Provider: "anthropic-acc1"}}},
		provider.ReviewCRLevelB: {{Output: "   \n", Selection: provider.Selection{Provider: "anthropic-acc1"}}},
		provider.ReviewCRLevelC: {
			{Output: `{"approved":true,"findings":[]}`, Selection: provider.Selection{Provider: "anthropic-acc1"}},
			{Output: "", Selection: provider.Selection{Provider: "openai-acc1"}},
		},
	}
	for level, responses := range cases {
		t.Run(string(level), func(t *testing.T) {
			exec := &fakeExecutor{responses: responses}
			p, err := NewProvider(exec, twoFamilyRegistry(), nil)
			if err != nil {
				t.Fatalf("NewProvider: %v", err)
			}
			resp, err := p.Review(context.Background(), provider.ReviewRequest{
				Level: level, Diff: "diff --git a/x.go b/x.go\n+x",
			})
			if err == nil {
				t.Fatalf("%s accepted an empty dispatch body and returned %+v -- an unproduced result must be a "+
					"typed error, never a silently-empty review", level, resp)
			}
			if resp.Approved {
				t.Errorf("%s returned Approved=true on an empty dispatch body", level)
			}
			if len(resp.Findings) != 0 {
				t.Errorf("%s returned findings on an empty dispatch body: %+v", level, resp.Findings)
			}
		})
	}

	// And the parsers themselves, directly: both refuse an empty body.
	if _, err := parseFindings("", Checklist{}); err == nil {
		t.Error("parseFindings(\"\") returned nil error")
	}
	if _, err := parseArbitration("", Checklist{}); err == nil {
		t.Error("parseArbitration(\"\") returned nil error")
	}
}
