package policy

// Purpose: every refusal Simulate itself can make — no engine, no context,
//   and an approval sink whose admission cannot be asked without acting on
//   it. Split from dryrun_test.go per Art.10.3 (the 300-line file cap).
// SPORT: internal/policy Engine/CHANGED (P1-E09-W2-S18-T4).

import (
	"context"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/pkg/cascade"
)

// --- error paths ----------------------------------------------------------

// TestDryRunErrorPaths covers every refusal Simulate itself can make: no
// engine, no context, and an approval sink whose admission cannot be asked
// without acting on it.
func TestDryRunErrorPaths(t *testing.T) {
	var nilEngine *Engine
	res, err := nilEngine.Simulate(context.Background(), simInput(readCap().Name, L0))
	if err == nil || res.Verdict != VerdictDeny || res.RiskLevel != L4 {
		t.Errorf("the nil engine answered %+v, %v; want a deny with an error", res, err)
	}

	f := newDryRunFixture(t)
	//nolint:staticcheck // SA1012: passing a nil context is exactly the misuse under test.
	res, err = f.engine.Simulate(nil, simInput(readCap().Name, L0))
	if err == nil || res.Verdict != VerdictDeny {
		t.Errorf("a nil context answered %+v, %v; want a deny with an error", res, err)
	}
	if res.EffectiveScope != corpus.VisibilityPrivate {
		t.Errorf("a refused simulation reported reach %q", res.EffectiveScope)
	}

	// A queue this package did not build cannot be previewed, so the
	// report denies rather than calling a sink that might write.
	opaque, err := NewEngine(f.reg, f.grants, f.ctrl)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	res = mustSimulate(t, opaque.WithApprovalQueue(unsimulatableQueue{}), simInput(approvalCap().Name, L2))
	if res.Verdict != VerdictDeny {
		t.Errorf("an unsimulatable queue produced %+v, want a deny", res)
	}
	if res.WouldEmitAudit {
		t.Error("a refused preview claimed the live path would write an audit row")
	}
}

// mustSimulate runs a simulation that must not error.
func mustSimulate(t *testing.T, e *Engine, in DryRunInput) DryRunResult {
	t.Helper()
	res, err := e.Simulate(context.Background(), in)
	if err != nil {
		t.Fatalf("Simulate: %v", err)
	}
	return res
}

// unsimulatableQueue is a queue built outside this package: it satisfies
// ApprovalQueue and nothing else, so there is no way to ask it what it
// would do without doing it. Every method fails the test if it is reached,
// because reaching one is the defect.
type unsimulatableQueue struct{}

func (unsimulatableQueue) Enqueue(context.Context, EnqueueRequest) (EnqueueResult, error) {
	return EnqueueResult{}, cascade.New(cascade.KindInternal, "policy: a simulation called a live queue")
}
func (unsimulatableQueue) GetPending(context.Context) ([]PendingEntry, error) { return nil, nil }
func (unsimulatableQueue) Decide(context.Context, []DecisionRequest) ([]DecisionOutcome, error) {
	return nil, nil
}
func (unsimulatableQueue) Cancel(context.Context, string) error { return nil }
func (unsimulatableQueue) ConsumeToken(context.Context, ConsumeRequest) (ConsumeResult, error) {
	return ConsumeResult{}, nil
}
func (unsimulatableQueue) Expire(context.Context) (int, error) { return 0, nil }
