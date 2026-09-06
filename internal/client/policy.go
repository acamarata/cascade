package client

// Purpose: the typed method wrappers over the approval.* /
//   standing_grant.* / policy.* namespace (I/S-18.T6), following
//   status.go's and context_scope.go's established pattern: a command
//   calls one of these instead of assembling a request and decoding a
//   response itself.
//
// Inputs: the params types internal/policy declares for each verb.
// Outputs: the result types it declares, or a taxonomy error.
// Constraints: the method-name literals below are the registry's own keys.
//   policy_test.go asserts every one of them is a registered verb, so a
//   wrapper that dials a method the daemon does not serve is a test
//   failure rather than a runtime "method not found".

import (
	"context"

	"github.com/acamarata/cascade/internal/policy"
)

// The method names this file dials. They are the same strings the verb
// registry is keyed by.
const (
	ApprovalListMethod        = "approval.list"
	ApprovalShowMethod        = "approval.show"
	ApprovalGrantMethod       = "approval.grant"
	ApprovalDenyMethod        = "approval.deny"
	ApprovalExpireMethod      = "approval.expire"
	StandingGrantListMethod   = "standing_grant.list"
	StandingGrantCreateMethod = "standing_grant.create"
	StandingGrantChangeMethod = "standing_grant.change"
	StandingGrantRevokeMethod = "standing_grant.revoke"
	PolicyExplainMethod       = "policy.explain"
	PolicyCheckMethod         = "policy.check"
	PolicyListMethod          = "policy.list"
	PolicyAuditQueryMethod    = "policy.audit_query"
)

// ApprovalList returns the pending approval queue.
func (c *Client) ApprovalList(ctx context.Context) (policy.ApprovalListResult, error) {
	var res policy.ApprovalListResult
	err := c.Do(ctx, ApprovalListMethod, struct{}{}, &res)
	return res, err
}

// ApprovalShow returns one pending entry.
func (c *Client) ApprovalShow(ctx context.Context, requestID string) (policy.PendingEntry, error) {
	var res policy.PendingEntry
	err := c.Do(ctx, ApprovalShowMethod, policy.ApprovalShowParams{RequestID: requestID}, &res)
	return res, err
}

// ApprovalGrant redeems a signed approval token.
func (c *Client) ApprovalGrant(ctx context.Context, params policy.ApprovalGrantParams) (policy.ApprovalGrantResult, error) {
	var res policy.ApprovalGrantResult
	err := c.Do(ctx, ApprovalGrantMethod, params, &res)
	return res, err
}

// ApprovalDeny records one refusal.
func (c *Client) ApprovalDeny(ctx context.Context, params policy.ApprovalDecisionParams) (policy.DecisionResult, error) {
	var res policy.DecisionResult
	err := c.Do(ctx, ApprovalDenyMethod, params, &res)
	return res, err
}

// ApprovalExpire runs the expiry sweep.
func (c *Client) ApprovalExpire(ctx context.Context) (policy.ApprovalExpireResult, error) {
	var res policy.ApprovalExpireResult
	err := c.Do(ctx, ApprovalExpireMethod, struct{}{}, &res)
	return res, err
}

// StandingGrantList returns the grants one subject holds.
func (c *Client) StandingGrantList(ctx context.Context, params policy.StandingListParams) (policy.StandingListResult, error) {
	var res policy.StandingListResult
	err := c.Do(ctx, StandingGrantListMethod, params, &res)
	return res, err
}

// StandingGrantCreate writes a new standing grant.
func (c *Client) StandingGrantCreate(ctx context.Context, params policy.StandingWriteParams) (policy.StandingWriteResult, error) {
	var res policy.StandingWriteResult
	err := c.Do(ctx, StandingGrantCreateMethod, params, &res)
	return res, err
}

// StandingGrantChange rewrites an existing standing grant's terms.
func (c *Client) StandingGrantChange(ctx context.Context, params policy.StandingWriteParams) (policy.StandingWriteResult, error) {
	var res policy.StandingWriteResult
	err := c.Do(ctx, StandingGrantChangeMethod, params, &res)
	return res, err
}

// StandingGrantRevoke removes one standing grant.
func (c *Client) StandingGrantRevoke(ctx context.Context, params policy.StandingRevokeParams) (policy.StandingWriteResult, error) {
	var res policy.StandingWriteResult
	err := c.Do(ctx, StandingGrantRevokeMethod, params, &res)
	return res, err
}

// PolicyExplain returns the verdict and the whole decision path.
func (c *Client) PolicyExplain(ctx context.Context, params policy.EvalParams) (policy.ExplainResult, error) {
	var res policy.ExplainResult
	err := c.Do(ctx, PolicyExplainMethod, params, &res)
	return res, err
}

// PolicyCheck returns the verdict alone.
func (c *Client) PolicyCheck(ctx context.Context, params policy.EvalParams) (policy.CheckResult, error) {
	var res policy.CheckResult
	err := c.Do(ctx, PolicyCheckMethod, params, &res)
	return res, err
}

// PolicyList returns the registered capabilities and verbs.
func (c *Client) PolicyList(ctx context.Context) (policy.ListResult, error) {
	var res policy.ListResult
	err := c.Do(ctx, PolicyListMethod, struct{}{}, &res)
	return res, err
}

// PolicyAuditQuery reads one page of the append-only audit log.
func (c *Client) PolicyAuditQuery(ctx context.Context, params policy.AuditQueryParams) (policy.AuditQueryResult, error) {
	var res policy.AuditQueryResult
	err := c.Do(ctx, PolicyAuditQueryMethod, params, &res)
	return res, err
}
