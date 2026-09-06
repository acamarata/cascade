package policy

// Purpose: covers the standing-grant and policy halves of the Epic I
//   handler set over the real engine, the real B-layer grant store and a
//   real deny-list engine, including the deny-listed-class refusal, the
//   empty-action refusal and the audit read.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

// recordingQuerier answers audit reads from a fixed page.
type recordingQuerier struct {
	page audit.Page
	err  error
}

func (q recordingQuerier) Query(context.Context, audit.Filter) (audit.Page, error) {
	return q.page, q.err
}

// newPolicyRPCFixture builds the handler set with a real engine over the
// real grant store, plus a deny-list holding one denied action.
func newPolicyRPCFixture(t *testing.T, denied string) *rpcFixture {
	t.Helper()
	f := newApprovalFixture(t)
	engine, err := NewEngine(f.reg, f.grants, NewController(nil))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	deps := RPCDeps{
		Queue:    f.queue,
		Engine:   engine,
		Registry: f.reg,
		Grants:   f.grants,
		DenyList: &classDenyList{denied: map[string]bool{
			ClassWorkspaceMutation.String() + "|" + denied: true,
		}},
		Audit:    recordingQuerier{},
		Clock:    f.clock,
		Attestor: okAttestor{},
	}
	return &rpcFixture{approvalFixture: f, deps: deps, handlers: MethodHandlers(deps)}
}

// standingParams is the canonical create/change request the tests write.
func standingParams(action string, exp time.Time) StandingWriteParams {
	id, _ := cascade.NewID()
	return StandingWriteParams{
		GrantID:     id.String(),
		ActionClass: ClassWorkspaceMutation,
		Action:      action,
		Capability:  approvalCap().Name,
		Grantee:     testSubject(),
		Exp:         exp,
	}
}

// TestStandingCreateWritesThroughTheOneStore drives create, list and
// revoke against the real grant store.
func TestStandingCreateWritesThroughTheOneStore(t *testing.T) {
	f := newPolicyRPCFixture(t, "rm -rf /")
	params := standingParams("workspace.write", f.clock.Now().Add(time.Hour))
	res, err := f.call(t, "standing_grant.create", params)
	if err != nil {
		t.Fatalf("standing_grant.create: %v", err)
	}
	if !res.(StandingWriteResult).Changed {
		t.Error("standing_grant.create reported no change")
	}

	listed, err := f.call(t, "standing_grant.list", StandingListParams{Subject: testSubject()})
	if err != nil {
		t.Fatalf("standing_grant.list: %v", err)
	}
	if len(listed.(StandingListResult).Grants) != 1 {
		t.Fatalf("standing_grant.list = %+v, want the one row create wrote", listed)
	}

	if _, err := f.call(t, "standing_grant.revoke", StandingRevokeParams{
		Grantee: testSubject(), Capability: approvalCap().Name,
	}); err != nil {
		t.Fatalf("standing_grant.revoke: %v", err)
	}
	listed, err = f.call(t, "standing_grant.list", StandingListParams{Subject: testSubject()})
	if err != nil {
		t.Fatalf("standing_grant.list after revoke: %v", err)
	}
	if len(listed.(StandingListResult).Grants) != 0 {
		t.Errorf("the revoked grant is still listed: %+v", listed)
	}
}

// TestStandingCreateDeniedClassRefused is the contract's named assertion:
// a deny-listed action class is refused as the permission-denied kind, and
// no row exists afterwards.
func TestStandingCreateDeniedClassRefused(t *testing.T) {
	f := newPolicyRPCFixture(t, "rm -rf /")
	params := standingParams("rm -rf /", f.clock.Now().Add(time.Hour))
	_, err := f.call(t, "standing_grant.create", params)
	if err == nil {
		t.Fatal("standing_grant.create wrote a grant for a deny-listed action")
	}
	if !errors.Is(err, ErrDeniedClass) {
		t.Errorf("refusal = %v, want ErrDeniedClass", err)
	}
	if !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("refusal kind = %v, want permission-denied", err)
	}
	grants, err := f.grants.List(context.Background(), testSubject())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(grants) != 0 {
		t.Errorf("the refused create left %d grant row(s) behind", len(grants))
	}
}

// TestStandingCreateRefusesAnElevationClassVerb proves the second guard is
// reached through this surface too, and that a malformed grant id is
// refused before any of it runs.
func TestStandingCreateRefusesAnElevationClassVerb(t *testing.T) {
	f := newPolicyRPCFixture(t, "rm -rf /")
	params := standingParams("vault.get", f.clock.Now().Add(time.Hour))
	if _, err := f.call(t, "standing_grant.create", params); !errors.Is(err, ErrDeniedClass) {
		t.Errorf("an elevation-class verb was granted standing: %v", err)
	}
	bad := standingParams("workspace.write", f.clock.Now().Add(time.Hour))
	bad.GrantID = "not-an-id"
	if _, err := f.call(t, "standing_grant.create", bad); !cascade.HasKind(err, cascade.KindPermissionDenied) {
		t.Errorf("a malformed grant id = %v, want permission-denied", err)
	}
}

// TestStandingChangeRunsTheSameGuards proves change is not a way around
// the deny-list.
func TestStandingChangeRunsTheSameGuards(t *testing.T) {
	f := newPolicyRPCFixture(t, "rm -rf /")
	params := standingParams("rm -rf /", f.clock.Now().Add(time.Hour))
	if _, err := f.call(t, "standing_grant.change", params); !errors.Is(err, ErrDeniedClass) {
		t.Errorf("standing_grant.change reached a denied class: %v", err)
	}
	ok := standingParams("workspace.write", f.clock.Now().Add(time.Hour))
	if _, err := f.call(t, "standing_grant.change", ok); err != nil {
		t.Errorf("standing_grant.change on a permitted action = %v, want nil", err)
	}
}

// TestPolicyExplainClassifiesAndTraces drives explain and check through
// the real engine and asserts the destructive command lands at L4 with a
// deny verdict, which is the acceptance story's own step.
func TestPolicyExplainClassifiesAndTraces(t *testing.T) {
	f := newPolicyRPCFixture(t, "never")
	res, err := f.call(t, "policy.explain", EvalParams{
		Subject:    testSubject(),
		Capability: approvalCap().Name,
		Action:     "rm -rf /tmp/x",
	})
	if err != nil {
		t.Fatalf("policy.explain: %v", err)
	}
	explained := res.(ExplainResult)
	if explained.Level != "L4" {
		t.Errorf("rm -rf classified at %s, want L4", explained.Level)
	}
	if explained.Verdict != "deny" {
		t.Errorf("rm -rf verdict = %s, want deny", explained.Verdict)
	}
	if explained.Explanation == "" || explained.MatchedRule == "" {
		t.Errorf("policy.explain returned no decision path: %+v", explained)
	}

	checked, err := f.call(t, "policy.check", EvalParams{
		Subject: testSubject(), Capability: approvalCap().Name, Action: "rm -rf /tmp/x",
	})
	if err != nil {
		t.Fatalf("policy.check: %v", err)
	}
	if got := checked.(CheckResult); got.Verdict != "deny" || got.AutoAdvance {
		t.Errorf("policy.check = %+v, want a deny with no auto-advance", got)
	}
}

// TestPolicyEvalRefusesAnEmptyAction covers the error path the contract
// names, on both evaluating verbs.
func TestPolicyEvalRefusesAnEmptyAction(t *testing.T) {
	f := newPolicyRPCFixture(t, "never")
	for _, method := range []string{"policy.explain", "policy.check"} {
		if _, err := f.call(t, method, EvalParams{}); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s with an empty action = %v, want invalid-input", method, err)
		}
	}
	noEngine := MethodHandlers(RPCDeps{Attestor: okAttestor{}})
	_, err := noEngine["policy.explain"](context.Background(), []byte(`{"action":"ls"}`))
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Errorf("policy.explain with no engine = %v, want unavailable", err)
	}
}

// TestPolicyListReportsCapabilitiesAndVerbs proves the surface reports the
// registry it was actually built over.
func TestPolicyListReportsCapabilitiesAndVerbs(t *testing.T) {
	f := newPolicyRPCFixture(t, "never")
	res, err := f.call(t, "policy.list", struct{}{})
	if err != nil {
		t.Fatalf("policy.list: %v", err)
	}
	listed := res.(ListResult)
	if len(listed.Capabilities) != 1 || listed.Capabilities[0].Name != approvalCap().Name {
		t.Errorf("policy.list capabilities = %+v, want the registered one", listed.Capabilities)
	}
	if len(listed.Verbs) != len(RegisteredVerbs()) {
		t.Errorf("policy.list reported %d verbs, want %d", len(listed.Verbs), len(RegisteredVerbs()))
	}
}

// TestPolicyAuditQueryReadsTheLog covers the read, the closed filter
// grammar and the unwired-log refusal.
func TestPolicyAuditQueryReadsTheLog(t *testing.T) {
	f := newPolicyRPCFixture(t, "never")
	f.deps.Audit = recordingQuerier{page: audit.Page{
		Records:    []audit.Record{{Seq: 1, ID: "01J8ZC5W2K4F6H8M0P2R4T6V8X"}},
		NextCursor: "more",
	}}
	f.handlers = MethodHandlers(f.deps)
	res, err := f.call(t, "policy.audit_query", AuditQueryParams{})
	if err != nil {
		t.Fatalf("policy.audit_query: %v", err)
	}
	page := res.(AuditQueryResult)
	if len(page.Records) != 1 || page.NextCursor != "more" {
		t.Errorf("policy.audit_query = %+v, want the one record and its cursor", page)
	}
	if _, err := f.call(t, "policy.audit_query", AuditQueryParams{
		Filter: []string{"this-is-not-a-key=value-pair-key"},
	}); err == nil {
		t.Error("policy.audit_query accepted a filter key that is not in the grammar")
	}
}
