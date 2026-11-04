//go:build !windows

// Purpose: the daemon verb mcp.capability.check. It answers whether the
// CALLER holds an mcp.tool.* or mcp.profile.* capability, where the caller is
// the principal the daemon resolved from the request's token (P1-SEC-18).
// Inputs: params {"capability": "<mcp.tool.* | mcp.profile.*>"} and the
// caller principal in ctx (principal.CallerFrom), never a params field.
// Outputs: policy.CheckResult; any verdict short of allow reads as deny.
// Constraints: the subject is never read from params, MCP clientInfo or any
// other caller-declared field (R22b N-2); params are decoded with unknown
// fields refused, so a "subject" field is KindInvalidInput. A caller with no
// principal (the owner CLI, a tokenless call) is denied without an engine
// call. policy.check and EvalParams.Subject are not reused.
// SPORT: cmd/cascade mcp.capability.check verb (P1-HRN-05).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mcpCapabilityCheckMethod is the verb's wire name.
const mcpCapabilityCheckMethod = "mcp.capability.check"

// The two capability families the verb answers for.
const (
	mcpProfileCapabilityPrefix = "mcp.profile."
	mcpToolCapabilityPrefix    = "mcp.tool."
)

// The owner view may ask; a principal-less owner view is still denied by
// capabilityVerdict, so the declaration admits nobody to a grant.
var _ = rpc.DeclareAccess(mcpCapabilityCheckMethod, rpc.AccessOwnerView)

// mcpCapabilityCheckParams is the verb's whole request shape.
type mcpCapabilityCheckParams struct {
	Capability string `json:"capability"`
}

// mountMCPCapabilityCheck registers mcp.capability.check over e.
func mountMCPCapabilityCheck(reg *rpc.Registry, e *policy.Engine) {
	reg.Register(mcpCapabilityCheckMethod, func(ctx context.Context, raw json.RawMessage) (any, error) {
		capability, err := decodeMCPCapabilityParams(raw)
		if err != nil {
			return nil, err
		}
		out, err := capabilityVerdict(ctx, e, capability)
		if err != nil {
			return nil, err
		}
		return capabilityCheckResult(out), nil
	})
}

// capabilityCheckResult reports out as the verb's answer. The verb asks
// whether the caller HOLDS the capability; there is no human turn on this
// path, so anything short of allow (ask included) is reported as deny.
func capabilityCheckResult(out policy.EvalOutcome) policy.CheckResult {
	verdict := policy.VerdictDeny
	if out.Verdict == policy.VerdictAllow {
		verdict = policy.VerdictAllow
	}
	return policy.CheckResult{
		Verdict:     verdict.String(),
		Level:       out.Level.String(),
		AutoAdvance: verdict == policy.VerdictAllow && out.AutoAdvance,
	}
}

// decodeMCPCapabilityParams reads params strictly and refuses a capability
// outside the two MCP families.
func decodeMCPCapabilityParams(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", cascade.New(cascade.KindInvalidInput, "mcp.capability.check: params are required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p mcpCapabilityCheckParams
	if err := dec.Decode(&p); err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "mcp.capability.check: params are not readable")
	}
	if dec.More() {
		return "", cascade.New(cascade.KindInvalidInput, "mcp.capability.check: trailing data after params")
	}
	c := p.Capability
	if !strings.HasPrefix(c, mcpProfileCapabilityPrefix) && !strings.HasPrefix(c, mcpToolCapabilityPrefix) {
		return "", cascade.New(cascade.KindInvalidInput,
			"mcp.capability.check: capability must be an mcp.profile.* or mcp.tool.* name")
	}
	return c, nil
}

// capabilityVerdict evaluates capability for the caller principal in ctx. No
// principal means deny, with no engine call: the owner view and a tokenless
// call hold no MCP grant of their own.
func capabilityVerdict(ctx context.Context, e *policy.Engine, capability string) (policy.EvalOutcome, error) {
	caller, ok := principal.CallerFrom(ctx)
	if !ok || caller.Principal == nil || caller.Principal.ID == "" {
		return policy.EvalOutcome{
			Verdict: policy.VerdictDeny,
			Level:   policy.L4,
			Layer:   policy.LayerFailClosed,
			Reason:  "no caller principal",
		}, nil
	}
	if e == nil {
		return policy.EvalOutcome{}, cascade.New(cascade.KindUnavailable,
			"mcp.capability.check: this daemon has no policy engine")
	}
	return e.Evaluate(ctx, policy.EvalRequest{
		Subject:     policy.Subject{Kind: policy.SubjectAgent, ID: caller.Principal.ID},
		Capability:  capability,
		Verb:        mcpCapabilityCheckMethod,
		CommandLess: true,
	})
}
