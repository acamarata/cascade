// Package policy (rpc_policy.go): Purpose: the policy.* half of the Epic I
//
//	handler set: explain, check, list and audit_query. Split from rpc.go
//	under Art.10.3's 300-line cap, and split again when this file itself
//	reached 343 lines: the standing_grant.* verbs moved to rpc_standing.go.
//	The guard, the params decoder and RPCDeps live in rpc.go and are shared.
//
// Inputs: raw JSON params and the RPCDeps collaborators.
// Outputs: the policy explain/check/list/audit_query handlers, plus their
//
//	params and result types.
//
// Constraints: these verbs are READ-ONLY. They evaluate through the one
//
//	Engine and never write a grant, a queue entry or an audit row of their
//	own; the writing half is rpc_standing.go's.
//
// SPORT: internal/policy rpc-policy-handlers/ADDED (P1-E09-W2-S18-T6).
package policy

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// EvalParams is what policy.explain and policy.check are called
// with. The action text is the classifier's only input.
type EvalParams struct {
	// Subject is who is acting.
	Subject Subject `json:"subject"`
	// Capability is the registered capability the action needs.
	Capability string `json:"capability"`
	// Action is the canonical text of what would run.
	Action string `json:"action"`
}

// ExplainResult is one evaluation plus its rendered trace.
type ExplainResult struct {
	// Verdict is the decision.
	Verdict string `json:"verdict"`
	// Level is the rung the action was evaluated at.
	Level string `json:"level"`
	// Layer names which layer decided.
	Layer string `json:"layer"`
	// Reason is the short human-readable explanation.
	Reason string `json:"reason"`
	// MatchedRule names the deciding layer's rule.
	MatchedRule string `json:"matched_rule"`
	// Explanation is the layer-by-layer rendering.
	Explanation string `json:"explanation"`
}

// evaluate runs one read-only evaluation through the one engine.
func (d RPCDeps) evaluate(ctx context.Context, p EvalParams) (EvalOutcome, error) {
	if p.Action == "" {
		return EvalOutcome{}, cascade.New(cascade.KindInvalidInput,
			"policy: an evaluation needs an action to classify")
	}
	engine, err := d.requireEngine()
	if err != nil {
		return EvalOutcome{}, err
	}
	return engine.Evaluate(ctx, EvalRequest{
		Subject:    p.Subject,
		Capability: p.Capability,
		Action:     p.Action,
	})
}

// policyExplain returns the verdict and the whole decision path.
func (d RPCDeps) policyExplain(ctx context.Context, params json.RawMessage) (any, error) {
	var p EvalParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	out, err := d.evaluate(ctx, p)
	if err != nil {
		return nil, err
	}
	return ExplainResult{
		Verdict:     out.Verdict.String(),
		Level:       out.Level.String(),
		Layer:       out.Layer.String(),
		Reason:      out.Reason,
		MatchedRule: out.Trace.MatchedRule,
		Explanation: out.Trace.Explanation,
	}, nil
}

// CheckResult is the short answer: what would happen, without the
// trace.
type CheckResult struct {
	// Verdict is the decision.
	Verdict string `json:"verdict"`
	// Level is the rung the action was evaluated at.
	Level string `json:"level"`
	// AutoAdvance reports whether an autonomous loop may proceed.
	AutoAdvance bool `json:"auto_advance"`
}

// policyCheck returns the verdict alone.
func (d RPCDeps) policyCheck(ctx context.Context, params json.RawMessage) (any, error) {
	var p EvalParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	out, err := d.evaluate(ctx, p)
	if err != nil {
		return nil, err
	}
	return CheckResult{
		Verdict:     out.Verdict.String(),
		Level:       out.Level.String(),
		AutoAdvance: out.AutoAdvance,
	}, nil
}

// ListResult is the surface's own shape: the capabilities this
// process registered and the verbs it will dispatch.
type ListResult struct {
	// Capabilities are the registered capabilities, ordered by name.
	Capabilities []Capability `json:"capabilities"`
	// Verbs are the registered surface verbs, ordered by method name.
	Verbs []VerbEntry `json:"verbs"`
}

// VerbEntry is one registered verb as this surface reports it.
type VerbEntry struct {
	// Method is the JSON-RPC method name.
	Method string `json:"method"`
	// Risk is the rung the verb is dispatched at.
	Risk string `json:"risk"`
	// Elevated reports whether the verb needs the elevation flow.
	Elevated bool `json:"elevated"`
	// MCPTool is the mirrored tool name, or empty when not mirrored.
	MCPTool string `json:"mcp_tool,omitempty"`
}

// policyList returns the registered capabilities and verbs.
func (d RPCDeps) policyList(ctx context.Context, _ json.RawMessage) (any, error) {
	caps := []Capability{}
	if d.Registry != nil {
		listed, err := d.Registry.List(ctx)
		if err != nil {
			return nil, err
		}
		caps = listed
	}
	verbs := make([]VerbEntry, 0, len(verbRegistry))
	for _, spec := range RegisteredVerbs() {
		verbs = append(verbs, VerbEntry{
			Method:   spec.Method,
			Risk:     spec.Risk.String(),
			Elevated: spec.Elevated(),
			MCPTool:  spec.MCPTool(),
		})
	}
	return ListResult{Capabilities: caps, Verbs: verbs}, nil
}

// AuditQueryParams is what policy.audit_query is called with. The
// filter arrives as the same key=value tokens the command line takes, so
// the CLI and the socket parse one grammar and not two.
type AuditQueryParams struct {
	// Filter is the key=value token list.
	Filter []string `json:"filter"`
}

// AuditQueryResult is one page of audit records.
type AuditQueryResult struct {
	// Records are the matching records, oldest first.
	Records []audit.Record `json:"records"`
	// NextCursor resumes the walk when more records remain.
	NextCursor string `json:"next_cursor,omitempty"`
}

// policyAuditQuery reads the append-only log. It never writes to it.
func (d RPCDeps) policyAuditQuery(ctx context.Context, params json.RawMessage) (any, error) {
	var p AuditQueryParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if d.Audit == nil {
		return nil, cascade.New(cascade.KindUnavailable, "policy: no audit log is wired in this process")
	}
	filter, err := audit.ParseFilter(p.Filter)
	if err != nil {
		return nil, err
	}
	page, err := d.Audit.Query(ctx, filter)
	if err != nil {
		return nil, err
	}
	records := page.Records
	if records == nil {
		records = []audit.Record{}
	}
	return AuditQueryResult{Records: records, NextCursor: page.NextCursor}, nil
}
