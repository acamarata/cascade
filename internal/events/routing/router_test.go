package routing

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the routing boundary's tests. Every allow/ask/deny case is
//   driven through a REAL policy engine (real capability registry, real
//   grant store, real balanced autonomy profile, real command classifier)
//   and a REAL append-only audit log, so the expected behaviour comes from
//   the specified ladder rather than from a second copy of the router's
//   own logic. The stub engines below exist only for the failure modes a
//   real engine cannot be made to produce on demand: an infrastructure
//   error and an out-of-range verdict.
// Constraints: no bare time.Now (FixedClock everywhere); no sleeps.

const testSubjectID = "hooks-agent"

func testSubject() policy.Subject {
	return policy.Subject{Kind: policy.SubjectAgent, ID: testSubjectID}
}

// newRealEngine builds the production engine over in-memory collaborators
// and registers capName with class. The balanced profile is the one
// 06-FORGE-SPEC §5.15 fixes: L0/L1 allow, L2/L3 ask, L4 deny.
func newRealEngine(t *testing.T, capName string, class policy.ActionClass) *policy.Engine {
	t.Helper()
	ctx := context.Background()
	clock := runtime.NewFixedClock(time.Unix(1_700_000_000, 0).UTC())
	reg := policy.NewMemoryRegistry()
	if err := reg.Add(ctx, policy.Capability{
		Name: capName, Desc: "routing test capability", DefaultPolicy: class,
	}); err != nil {
		t.Fatalf("register capability: %v", err)
	}
	grants, err := policy.NewStoreGrants(storetest.NewMemStore(), reg, clock)
	if err != nil {
		t.Fatalf("grant store: %v", err)
	}
	ctrl := policy.NewController(nil)
	if err := ctrl.Apply(ctx, map[string]interface{}{
		"policy": map[string]interface{}{"autonomy_profile": "balanced"},
	}); err != nil {
		t.Fatalf("apply profile: %v", err)
	}
	engine, err := policy.NewEngine(reg, grants, ctrl)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return engine
}

// newAuditLog returns a real append-only log over an in-memory store.
func newAuditLog() *audit.Log {
	return audit.New(storetest.NewMemStore(), runtime.NewFixedClock(time.Unix(1_700_000_000, 0).UTC()), nil)
}

func routedAction(command string, capName string) Action {
	return Action{
		Subject: testSubject(), Capability: capName, Verb: "hooks.fire",
		Command: command, Params: []byte(`{"k":"v"}`),
		Origin: OriginHook, Ref: "hook-1", Summary: "routing test",
	}
}

// countRoutedRows returns how many policy.route rows the log holds.
func countRoutedRows(t *testing.T, log *audit.Log) int {
	t.Helper()
	recs, err := log.Query(context.Background(), audit.Filter{Kinds: []audit.Kind{audit.KindPolicyRoute}})
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	return len(recs.Records)
}

// TestRouteActionAllowsReadCommand proves the allow leg end to end: a
// read-class capability with a read command sits on L0, which the
// balanced profile allows, and the decision is recorded.
func TestRouteActionAllowsReadCommand(t *testing.T) {
	log := newAuditLog()
	r, err := NewActionRouter(newRealEngine(t, "hooks.read", policy.ClassRead), log)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	verdict, _, err := r.RouteAction(context.Background(), routedAction("cat notes.txt", "hooks.read"))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if verdict != policy.VerdictAllow {
		t.Fatalf("verdict = %v, want allow", verdict)
	}
	if n := countRoutedRows(t, log); n != 1 {
		t.Fatalf("policy.route rows = %d, want 1", n)
	}
}

// TestRouteActionDeniesPrivilegedCapability proves the deny leg: the
// capability's own class raises the evaluated level to L4, which the
// balanced profile denies. A deny is a verdict AND a typed error.
func TestRouteActionDeniesPrivilegedCapability(t *testing.T) {
	log := newAuditLog()
	r, err := NewActionRouter(newRealEngine(t, "hooks.admin", policy.ClassDestructivePrivileged), log)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	verdict, _, err := r.RouteAction(context.Background(), routedAction("cat notes.txt", "hooks.admin"))
	if verdict != policy.VerdictDeny {
		t.Fatalf("verdict = %v, want deny", verdict)
	}
	if err == nil {
		t.Fatal("deny returned a nil error; a caller could read that as permission")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Fatalf("kind = %v (ok=%v), want policy_denied", kind, ok)
	}
	if n := countRoutedRows(t, log); n != 1 {
		t.Fatalf("policy.route rows = %d, want 1", n)
	}
}

// TestRouteActionAsksForWorkspaceMutation proves the ask leg: the
// balanced profile's L2 slot is ask, and an ask is returned with no error
// so the caller can suspend the action rather than treat it as a failure.
func TestRouteActionAsksForWorkspaceMutation(t *testing.T) {
	log := newAuditLog()
	r, err := NewActionRouter(newRealEngine(t, "hooks.write", policy.ClassWorkspaceMutation), log)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	verdict, _, err := r.RouteAction(context.Background(), routedAction("cat notes.txt", "hooks.write"))
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if verdict != policy.VerdictAsk {
		t.Fatalf("verdict = %v, want ask", verdict)
	}
}

// TestRouteActionDeniesUnregisteredCapability proves an unregistered
// capability is refused rather than defaulted (R-21.207).
func TestRouteActionDeniesUnregisteredCapability(t *testing.T) {
	r, err := NewActionRouter(newRealEngine(t, "hooks.read", policy.ClassRead), newAuditLog())
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	verdict, _, err := r.RouteAction(context.Background(), routedAction("cat notes.txt", "hooks.nonesuch"))
	if verdict != policy.VerdictDeny || err == nil {
		t.Fatalf("unregistered capability: verdict=%v err=%v, want deny with an error", verdict, err)
	}
}

// failingEngine is the infrastructure-failure case: an evaluator that
// cannot answer at all.
type failingEngine struct{}

func (failingEngine) Evaluate(context.Context, policy.EvalRequest) (policy.EvalOutcome, error) {
	return policy.EvalOutcome{}, errors.New("evaluator unavailable")
}

// TestRouteActionDeniesWhenEngineUnavailable proves the unavailable case
// fails closed: an evaluation that errors denies, and says so.
func TestRouteActionDeniesWhenEngineUnavailable(t *testing.T) {
	log := newAuditLog()
	r, err := NewActionRouter(failingEngine{}, log)
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	verdict, _, err := r.RouteAction(context.Background(), routedAction("cat notes.txt", "hooks.read"))
	if verdict != policy.VerdictDeny {
		t.Fatalf("verdict = %v, want deny", verdict)
	}
	if err == nil || !strings.Contains(err.Error(), RouteDeniedCode) {
		t.Fatalf("err = %v, want a %s refusal", err, RouteDeniedCode)
	}
	if n := countRoutedRows(t, log); n != 1 {
		t.Fatalf("policy.route rows = %d, want the refusal recorded", n)
	}
}

// unreadableEngine returns a verdict outside the three defined values,
// which is what an evaluator decoding a row it does not understand would
// produce.
type unreadableEngine struct{}

func (unreadableEngine) Evaluate(context.Context, policy.EvalRequest) (policy.EvalOutcome, error) {
	return policy.EvalOutcome{Verdict: policy.Verdict(200)}, nil
}

// TestRouteActionDeniesUnreadableVerdict proves the unparseable case
// fails closed: a verdict the router cannot read is a deny, never a pass.
func TestRouteActionDeniesUnreadableVerdict(t *testing.T) {
	r, err := NewActionRouter(unreadableEngine{}, newAuditLog())
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	verdict, _, err := r.RouteAction(context.Background(), routedAction("cat notes.txt", "hooks.read"))
	if verdict != policy.VerdictDeny || err == nil {
		t.Fatalf("unreadable verdict: verdict=%v err=%v, want deny with an error", verdict, err)
	}
}

// allowEngine always allows, so a test can prove a NON-policy failure
// still denies.
type allowEngine struct{}

func (allowEngine) Evaluate(context.Context, policy.EvalRequest) (policy.EvalOutcome, error) {
	return policy.EvalOutcome{Verdict: policy.VerdictAllow, Level: policy.L0, Layer: policy.LayerAutonomyProfile}, nil
}

// failingAuditor cannot record.
type failingAuditor struct{}

func (failingAuditor) Append(context.Context, audit.Event) (audit.Record, error) {
	return audit.Record{}, errors.New("audit store unavailable")
}

// TestRouteActionDeniesWhenDecisionCannotBeRecorded proves an allow whose
// audit row could not be written is downgraded to deny: an authorization
// nobody can review does not run.
func TestRouteActionDeniesWhenDecisionCannotBeRecorded(t *testing.T) {
	r, err := NewActionRouter(allowEngine{}, failingAuditor{})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	verdict, _, err := r.RouteAction(context.Background(), routedAction("cat notes.txt", "hooks.read"))
	if verdict != policy.VerdictDeny || err == nil {
		t.Fatalf("unrecordable allow: verdict=%v err=%v, want deny with an error", verdict, err)
	}
}

// TestRouteActionRefusesMalformedInput proves the unparseable-input leg
// at every required field, including a nil context and a nil router.
func TestRouteActionRefusesMalformedInput(t *testing.T) {
	r, err := NewActionRouter(allowEngine{}, newAuditLog())
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	good := routedAction("cat notes.txt", "hooks.read")
	cases := map[string]Action{
		"no origin":      func() Action { a := good; a.Origin = ""; return a }(),
		"unknown origin": func() Action { a := good; a.Origin = Origin("bridge"); return a }(),
		"no ref":         func() Action { a := good; a.Ref = ""; return a }(),
		"no capability":  func() Action { a := good; a.Capability = ""; return a }(),
		"no command":     func() Action { a := good; a.Command = ""; return a }(),
		"no subject":     func() Action { a := good; a.Subject = policy.Subject{}; return a }(),
	}
	for name, action := range cases {
		verdict, _, err := r.RouteAction(context.Background(), action)
		if verdict != policy.VerdictDeny || err == nil {
			t.Fatalf("%s: verdict=%v err=%v, want deny with an error", name, verdict, err)
		}
	}
	//nolint:staticcheck // the nil-context refusal is the behaviour under test.
	if verdict, _, err := r.RouteAction(nil, good); verdict != policy.VerdictDeny || err == nil {
		t.Fatalf("nil ctx: verdict=%v err=%v, want deny with an error", verdict, err)
	}
	var nilRouter *ActionRouter
	if verdict, _, err := nilRouter.RouteAction(context.Background(), good); verdict != policy.VerdictDeny || err == nil {
		t.Fatalf("nil router: verdict=%v err=%v, want deny with an error", verdict, err)
	}
}

// TestNewActionRouterRequiresCollaborators proves neither collaborator is
// optional.
func TestNewActionRouterRequiresCollaborators(t *testing.T) {
	if _, err := NewActionRouter(nil, newAuditLog()); err == nil {
		t.Fatal("router built with no policy engine")
	}
	if _, err := NewActionRouter(allowEngine{}, nil); err == nil {
		t.Fatal("router built with no audit writer")
	}
}
