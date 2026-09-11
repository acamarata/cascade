package conductor

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/pkg/provider"
)

// TestExecute_ConstructionRequiresAuditBroker asserts NewExecutor refuses
// when the audit broker is nil - there is no default, no no-op writer, and
// no build-tag bypass (R-21.206 A).
func TestExecute_ConstructionRequiresAuditBroker(t *testing.T) {
	cfg, _ := newReadyConfig(t)
	cfg.Audit = nil
	if _, err := NewExecutor(cfg); err != ErrConstructionFailed {
		t.Fatalf("got %v, want ErrConstructionFailed", err)
	}
}

func TestExecute_ConstructionRequiresRouterAndResolver(t *testing.T) {
	cfg, _ := newReadyConfig(t)
	cfg.Router = nil
	if _, err := NewExecutor(cfg); err != ErrConstructionFailed {
		t.Fatalf("nil Router: got %v, want ErrConstructionFailed", err)
	}
	cfg2, _ := newReadyConfig(t)
	cfg2.Resolver = nil
	if _, err := NewExecutor(cfg2); err != ErrConstructionFailed {
		t.Fatalf("nil Resolver: got %v, want ErrConstructionFailed", err)
	}
}

// TestExecute_SecurityPipelineNotReady removes each of the six R-21.206
// collaborators individually and asserts Execute returns
// ErrSecurityPipelineNotReady with ZERO provider calls in every case.
func TestExecute_SecurityPipelineNotReady(t *testing.T) {
	mutators := map[string]func(*ExecutorConfig){
		"Classifier":  func(c *ExecutorConfig) { c.Classifier = nil },
		"Taxonomy":    func(c *ExecutorConfig) { c.Taxonomy = nil },
		"Policy":      func(c *ExecutorConfig) { c.Policy = nil },
		"Sensitivity": func(c *ExecutorConfig) { c.Sensitivity = nil },
		"Firewall":    func(c *ExecutorConfig) { c.Firewall = nil },
	}
	for name, mutate := range mutators {
		t.Run(name, func(t *testing.T) {
			cfg, deps := newReadyConfig(t)
			mutate(&cfg)
			exec, err := NewExecutor(cfg)
			if err != nil {
				t.Fatalf("NewExecutor with nil %s: %v", name, err)
			}
			deps.prov.chatFn = func(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
				t.Fatal("provider called before the pipeline was ready")
				return provider.ChatResponse{}, nil
			}
			if _, err := exec.Execute(context.Background(), validReq()); err != ErrSecurityPipelineNotReady {
				t.Fatalf("Execute with nil %s: got %v, want ErrSecurityPipelineNotReady", name, err)
			}
			if deps.router.calls != 0 {
				t.Fatalf("nil %s: router.Select called %d times, want 0", name, deps.router.calls)
			}
			if _, _, err := exec.ExecuteStream(context.Background(), validReq()); err != ErrSecurityPipelineNotReady {
				t.Fatalf("ExecuteStream with nil %s: got %v, want ErrSecurityPipelineNotReady", name, err)
			}
		})
	}
}
