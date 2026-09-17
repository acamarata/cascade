package coretools

// Purpose: the production CapabilityFilter — the one that asks the one
//   policy entry point (P1-E16-W4-S34-T2, R-14.251 rules 2 and 3).
// Inputs: a *policy.Engine and the subject the MCP session acts as.
// Outputs: expose / do not expose, per tool capability.
// Constraints: fail-closed on every axis. No engine, an unregistered
//   capability, a verdict short of allow, or an evaluation error all mean
//   NOT exposed. An `ask` verdict is a denial HERE even though it is not
//   one at call time: a tool the user has not already granted must not
//   appear in a list the model reads as "things I may call", because the
//   model would offer it and the user would meet an approval prompt they
//   did not ask for.
// SPORT: internal/mcp/coretools (ADD) — P1-E16-W4-S34-T2.

import (
	"context"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/policy"
)

// Evaluator is the one policy entry point, narrowed to the single method
// this filter calls and transcribed from (*policy.Engine).Evaluate. The
// assertion below pins it. There is no policy.CapabilityGranted and none
// is added (R-21.236).
type Evaluator interface {
	Evaluate(ctx context.Context, req policy.EvalRequest) (policy.EvalOutcome, error)
}

var _ Evaluator = (*policy.Engine)(nil)

// listAction is the canonical action text every exposure check evaluates.
//
// It is a constant, and deliberately not the tool's own name: this
// question is "may this session see a tool needing capability X", not
// "may this session run tool Y". The per-call authorization is a separate
// evaluation the method's own gate performs, with the real action text.
const listAction = "mcp.tools/list"

// PolicyFilter answers exposure questions from the policy engine.
type PolicyFilter struct {
	engine  Evaluator
	subject policy.Subject
}

var _ mcp.CapabilityFilter = PolicyFilter{}

// NewPolicyFilter builds the filter for subject over engine.
//
// A nil engine yields a filter that denies everything, which is the same
// thing internal/mcp's DenyAllFilter does — returned as a PolicyFilter
// anyway so a composition root cannot accidentally hold a value whose
// type says "policy" while its behaviour says "allow".
func NewPolicyFilter(engine Evaluator, subject policy.Subject) PolicyFilter {
	return PolicyFilter{engine: engine, subject: subject}
}

// Allow reports whether capability is granted to this filter's subject.
func (f PolicyFilter) Allow(ctx context.Context, capability string) bool {
	if f.engine == nil || capability == "" {
		return false
	}
	outcome, err := f.engine.Evaluate(ctx, policy.EvalRequest{
		Subject:    f.subject,
		Capability: capability,
		Action:     listAction,
		// CommandLess: listing a tool HAS no command line, rather than
		// having an empty one. Without this the request is unclassifiable
		// and takes the highest rung, which would deny every tool for a
		// reason that has nothing to do with the grant (R-14.211).
		CommandLess: true,
	})
	return err == nil && outcome.Verdict == policy.VerdictAllow
}
