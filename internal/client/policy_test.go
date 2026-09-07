package client

// Purpose: unit tests for policy.go's typed method wrappers, in the
//   default no-network unit lane (Art.7.2): each wrapper is driven through
//   the REAL production dialer (UnixDialer, defined in client.go, which
//   already imports "net" so this file does not need to) against a socket
//   path nothing serves, exactly transport_test.go's pattern for Status.
//   That proves two real things per wrapper, not "no panic": (1) the
//   transport failure classification propagates unmangled through the
//   wrapper (KindUnavailable, not swallowed or re-tagged) and (2) the
//   wrapper returns the zero result rather than a half-populated one.
//   TestPolicyMethodLiterals separately proves every method-name literal
//   this file dials is a REAL registered verb in internal/policy's own
//   registry, so a wrapper that drifts from the registry is a test
//   failure here rather than a silent runtime "method not found".
// SPORT: internal/client (ADD).

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// TestPolicyMethodLiterals proves every constant policy.go dials is a verb
// internal/policy's own registry actually has, so a typo or a renamed verb
// fails here instead of surfacing as a runtime "method not found".
func TestPolicyMethodLiterals(t *testing.T) {
	methods := []string{
		ApprovalListMethod, ApprovalShowMethod, ApprovalGrantMethod,
		ApprovalDenyMethod, ApprovalExpireMethod,
		StandingGrantListMethod, StandingGrantCreateMethod,
		StandingGrantChangeMethod, StandingGrantRevokeMethod,
		PolicyExplainMethod, PolicyCheckMethod, PolicyListMethod,
		PolicyAuditQueryMethod,
	}
	for _, m := range methods {
		if _, err := policy.LookupVerb(m); err != nil {
			t.Errorf("LookupVerb(%q): %v, want a registered verb", m, err)
		}
	}
}

// policyTransportClient builds a Client dialing a socket nothing serves,
// so every wrapper below fails at the transport stage without ever
// needing a real daemon.
func policyTransportClient(t *testing.T) *Client {
	t.Helper()
	return New(missingSocket(t), UnixDialer, 2*time.Second)
}

func TestApprovalList_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.ApprovalList(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if len(res.Pending) != 0 {
		t.Errorf("ApprovalList returned %+v on failure, want the zero result", res)
	}
}

func TestApprovalShow_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.ApprovalShow(context.Background(), "req-1")
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.RequestID != "" {
		t.Errorf("ApprovalShow returned %+v on failure, want the zero result", res)
	}
}

func TestApprovalGrant_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.ApprovalGrant(context.Background(), policy.ApprovalGrantParams{
		RequestID: "req-1", SignedToken: "tok", Action: "do-a-thing",
	})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.RequestID != "" || res.ConsumedAt != "" {
		t.Errorf("ApprovalGrant returned %+v on failure, want the zero result", res)
	}
}

func TestApprovalDeny_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.ApprovalDeny(context.Background(), policy.ApprovalDecisionParams{RequestID: "req-1"})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.RequestID != "" || res.State != "" {
		t.Errorf("ApprovalDeny returned %+v on failure, want the zero result", res)
	}
}

func TestApprovalExpire_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.ApprovalExpire(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.Expired != 0 {
		t.Errorf("ApprovalExpire returned %+v on failure, want the zero result", res)
	}
}

func TestStandingGrantList_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.StandingGrantList(context.Background(), policy.StandingListParams{})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if len(res.Grants) != 0 {
		t.Errorf("StandingGrantList returned %+v on failure, want the zero result", res)
	}
}

func TestStandingGrantCreate_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.StandingGrantCreate(context.Background(), policy.StandingWriteParams{GrantID: "g1"})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.GrantID != "" || res.Changed {
		t.Errorf("StandingGrantCreate returned %+v on failure, want the zero result", res)
	}
}

func TestStandingGrantChange_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.StandingGrantChange(context.Background(), policy.StandingWriteParams{GrantID: "g1"})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.GrantID != "" || res.Changed {
		t.Errorf("StandingGrantChange returned %+v on failure, want the zero result", res)
	}
}

func TestStandingGrantRevoke_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.StandingGrantRevoke(context.Background(), policy.StandingRevokeParams{Capability: "cap"})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.GrantID != "" || res.Changed {
		t.Errorf("StandingGrantRevoke returned %+v on failure, want the zero result", res)
	}
}

func TestPolicyExplain_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.PolicyExplain(context.Background(), policy.EvalParams{Action: "approval.grant"})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.Verdict != "" {
		t.Errorf("PolicyExplain returned %+v on failure, want the zero result", res)
	}
}

func TestPolicyCheck_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.PolicyCheck(context.Background(), policy.EvalParams{Action: "approval.grant"})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if res.Verdict != "" {
		t.Errorf("PolicyCheck returned %+v on failure, want the zero result", res)
	}
}

func TestPolicyList_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.PolicyList(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if len(res.Verbs) != 0 || len(res.Capabilities) != 0 {
		t.Errorf("PolicyList returned %+v on failure, want the zero result", res)
	}
}

func TestPolicyAuditQuery_TransportFailurePropagates(t *testing.T) {
	c := policyTransportClient(t)
	res, err := c.PolicyAuditQuery(context.Background(), policy.AuditQueryParams{})
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("err = %v, want KindUnavailable", err)
	}
	if len(res.Records) != 0 || res.NextCursor != "" {
		t.Errorf("PolicyAuditQuery returned %+v on failure, want the zero result", res)
	}
}
