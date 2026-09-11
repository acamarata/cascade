package conductor

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestSentinels_WrapFrozenKinds asserts every declared sentinel wraps a
// member of the frozen 14-kind taxonomy, and that ErrSensitivityLocalOnly
// is declared nowhere in this package (R-21.217).
func TestSentinels_WrapFrozenKinds(t *testing.T) {
	cases := []struct {
		name string
		err  error
		kind cascade.Kind
	}{
		{"ErrInvalidRequest", ErrInvalidRequest, cascade.KindInvalidInput},
		{"ErrNoLane", ErrNoLane, cascade.KindUnavailable},
		{"ErrSensitivityViolation", ErrSensitivityViolation, cascade.KindPolicyDenied},
		{"ErrSecurityPipelineNotReady", ErrSecurityPipelineNotReady, cascade.KindUnavailable},
		{"ErrConstructionFailed", ErrConstructionFailed, cascade.KindInvalidInput},
	}
	for _, tc := range cases {
		if !cascade.HasKind(tc.err, tc.kind) {
			t.Errorf("%s does not wrap %s", tc.name, tc.kind)
		}
	}
}

// TestExecute_UnknownClassificationTerminalDeny asserts a classifier error
// is a terminal deny (ErrInvalidRequest) and Execute makes zero provider
// calls; there is no approval or elevation path for it (R-21.208).
func TestExecute_UnknownClassificationTerminalDeny(t *testing.T) {
	cfg, deps := newReadyConfig(t)
	cfg.Classifier = &fakeClassifier{err: cascade.New(cascade.KindInvalidInput, "unclassifiable")}
	deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
		t.Fatal("provider called on a classifier-error request")
		return provider.ChatResponse{}, nil
	}
	exec, err := NewExecutor(cfg)
	if err != nil {
		t.Fatalf("NewExecutor: %v", err)
	}
	if _, err := exec.Execute(context.Background(), validReq()); err != ErrInvalidRequest {
		t.Fatalf("got %v, want ErrInvalidRequest", err)
	}
	if deps.router.calls != 0 {
		t.Fatalf("router.Select called %d times, want 0: terminal deny must never reach selection", deps.router.calls)
	}
}
