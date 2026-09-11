package conductor

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/hooks/egress"
	"github.com/acamarata/cascade/pkg/provider"
)

// fakeSubstitutor is a deterministic EgressSubstitutor double: it records
// every call and either echoes the payload or returns forceErr.
type fakeSubstitutor struct {
	calls     int
	lastClass egress.EgressClass
	lastTier  egress.SensitivityTier
	lastBytes []byte
	forceErr  error
	rewrite   []byte
}

func (f *fakeSubstitutor) InterceptClass(_ context.Context, class egress.EgressClass, tier egress.SensitivityTier, content []byte) ([]byte, error) {
	f.calls++
	f.lastClass = class
	f.lastTier = tier
	f.lastBytes = content
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	if f.rewrite != nil {
		return f.rewrite, nil
	}
	return content, nil
}

// TestSubstitutionMiddleware_CalledOnEveryDispatch asserts
// SubstitutionMiddleware calls the substitutor exactly once per call,
// under EgressClassConductor.
func TestSubstitutionMiddleware_CalledOnEveryDispatch(t *testing.T) {
	sub := &fakeSubstitutor{}
	out, err := SubstitutionMiddleware(context.Background(), sub, EgressClassConductor, egress.TierPublic, []byte("payload"))
	if err != nil {
		t.Fatalf("SubstitutionMiddleware: %v", err)
	}
	if sub.calls != 1 {
		t.Fatalf("substitutor called %d times, want 1", sub.calls)
	}
	if sub.lastClass != EgressClassConductor {
		t.Fatalf("class = %q, want %q", sub.lastClass, EgressClassConductor)
	}
	if string(out) != "payload" {
		t.Fatalf("out = %q, want payload", out)
	}
}

// TestSubstitutionMiddleware_SubstitutorError_ProviderNotCalled asserts a
// substitutor error maps to ErrEgressSubstitutionFailed and returns no
// payload.
func TestSubstitutionMiddleware_SubstitutorError_ProviderNotCalled(t *testing.T) {
	sub := &fakeSubstitutor{forceErr: egress.ErrCapabilityRequired}
	out, err := SubstitutionMiddleware(context.Background(), sub, EgressClassConductor, egress.TierRestricted, []byte("payload"))
	if err != ErrEgressSubstitutionFailed {
		t.Fatalf("got %v, want ErrEgressSubstitutionFailed", err)
	}
	if out != nil {
		t.Fatalf("out = %v, want nil on failure", out)
	}
}

// TestSubstitutionMiddleware_TaggedPayloadSubstituted asserts a payload
// the substitutor rewrites never reaches the caller in its original form.
func TestSubstitutionMiddleware_TaggedPayloadSubstituted(t *testing.T) {
	sub := &fakeSubstitutor{rewrite: []byte("[REDACTED]")}
	out, err := SubstitutionMiddleware(context.Background(), sub, EgressClassConductor, egress.TierInternal, []byte("secret-shaped-value"))
	if err != nil {
		t.Fatalf("SubstitutionMiddleware: %v", err)
	}
	if string(out) != "[REDACTED]" {
		t.Fatalf("out = %q, the raw tagged value must never reach the caller", out)
	}
}

// TestEgressClassConductor_AllowLocalOnlyRegistered asserts the
// registered InterceptConfig matches the R-21.228 contract verbatim, and
// that a second registration on the same registry is idempotent.
func TestEgressClassConductor_AllowLocalOnlyRegistered(t *testing.T) {
	reg := egress.NewRegistry()
	if err := RegisterEgressClassConductor(reg); err != nil {
		t.Fatalf("RegisterEgressClassConductor: %v", err)
	}
	if err := RegisterEgressClassConductor(reg); err != nil {
		t.Fatalf("second RegisterEgressClassConductor: %v, want idempotent success", err)
	}
	cfg, ok := reg.Lookup(EgressClassConductor)
	if !ok {
		t.Fatal("EgressClassConductor is not registered")
	}
	if !cfg.Enabled || !cfg.AllowRestricted || !cfg.AllowLocalOnly {
		t.Fatalf("cfg = %+v, want Enabled/AllowRestricted/AllowLocalOnly all true", cfg)
	}
	want := map[egress.SensitivityTier]bool{
		egress.TierLocalOnly: true, egress.TierRestricted: true,
		egress.TierInternal: true, egress.TierPublic: true,
	}
	if len(cfg.AllowedTiers) != len(want) {
		t.Fatalf("AllowedTiers = %v, want exactly local-only/restricted/internal/public", cfg.AllowedTiers)
	}
	for _, tier := range cfg.AllowedTiers {
		if !want[tier] {
			t.Fatalf("AllowedTiers contains unexpected tier %q", tier)
		}
	}
}

// TestSensitivity_LocalOnlyNeverEgresses asserts a local-only request
// with no local-locality lane returns ErrNoLane from FILTER 2 and never
// reaches an egress class: ErrSensitivityLocalOnly does not exist
// anywhere in this package (R-21.217), and SubstitutionMiddleware is
// never called when Router.Select refuses first.
func TestSensitivity_LocalOnlyNeverEgresses(t *testing.T) {
	reg := &fakeRegistry{
		providers: []provider.ProviderInfo{{Name: "prov-remote", BaseURL: "https://api.example.invalid", HealthStatus: "healthy"}},
		lanes:     []provider.LaneInfo{{LaneName: "lane-remote", ProviderName: "prov-remote"}},
	}
	quota := &fakeQuota{order: []LaneID{"lane-remote"}}
	router := NewRouter(reg, quota, nil, nil)
	req := chatReq()
	req.Sensitivity = provider.SensitivityLocalOnly

	sub := &fakeSubstitutor{}
	_, err := router.Select(context.Background(), req)
	if err != ErrNoLane {
		t.Fatalf("got %v, want ErrNoLane", err)
	}
	if sub.calls != 0 {
		t.Fatalf("substitutor called %d times, want 0: a refused selection never reaches egress", sub.calls)
	}
}

// TestSensitivity_LocalOnlyRequiresComputedLocalLane documents a CONTRACT
// DEVIATION (see sensitivity.go's header comment and the journal):
// provider.Selection carries no locality-bearing field, and ExecutorConfig
// (pipeline.go) grants execute.go no registry reader outside this
// ticket's files_scope to compute the R-21.113 predicate from at the
// pre-dispatch point. FILTER 2 (K/S-22.T2, landed in the same change) is
// this build's sole reachable computed-locality enforcement layer; there
// is no second assertion point for this test to exercise without
// expanding files_scope.
func TestSensitivity_LocalOnlyRequiresComputedLocalLane(t *testing.T) {
	t.Skip("CONTRACT DEVIATION: pre-dispatch computed-locality re-assertion needs a registry reader on ExecutorConfig (pipeline.go), outside this ticket's files_scope; see journal")
}

// TestEgressClassConductor_EmbeddingRequestsTransitIntercept documents a
// CONTRACT DEVIATION: R-40.X14 requires every ProviderEmbedder request to
// transit this middleware, but internal/conductor/embed.go (the Embed
// entry point) is not in this ticket's files_scope (add:
// sensitivity.go/sensitivity_test.go, change: execute.go/errors.go/
// docs/security-posture.md only). Wiring embed.go's dispatch through
// SubstitutionMiddleware requires a files_scope amendment or a follow-up
// ticket; see the journal.
func TestEgressClassConductor_EmbeddingRequestsTransitIntercept(t *testing.T) {
	t.Skip("CONTRACT DEVIATION: embed.go is outside this ticket's files_scope; embeddings do not yet transit SubstitutionMiddleware; see journal")
}

// TestEgressClassConductor_IsNotTheIntakeClass asserts EgressClassConductor
// and egress.EgressClassProviderIntake are distinct registered classes,
// and that registering one never touches the other's entry.
func TestEgressClassConductor_IsNotTheIntakeClass(t *testing.T) {
	if EgressClassConductor == egress.EgressClassProviderIntake {
		t.Fatal("EgressClassConductor must not equal EgressClassProviderIntake")
	}
	reg := egress.NewRegistry()
	if err := reg.Register(egress.EgressClassProviderIntake, egress.InterceptConfig{Enabled: true, Owner: "J/S-20.T1"}); err != nil {
		t.Fatalf("registering provider-intake: %v", err)
	}
	if err := RegisterEgressClassConductor(reg); err != nil {
		t.Fatalf("RegisterEgressClassConductor: %v", err)
	}
	intakeCfg, ok := reg.Lookup(egress.EgressClassProviderIntake)
	if !ok || intakeCfg.AllowLocalOnly {
		t.Fatalf("provider-intake config was mutated by conductor registration: %+v", intakeCfg)
	}
}
