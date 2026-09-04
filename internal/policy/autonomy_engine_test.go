// Purpose: hard requirement 1 — the autonomy profile may RESTRICT and may
// never WIDEN. Every case here takes a refusal produced by a layer BENEATH
// the profile (the classifier's top rung, the capability registry, the L4
// ceiling) and proves that the most permissive profile the type can express
// still refuses. The complementary direction — a stricter profile
// tightening what a lower layer permitted — is asserted too, so the
// property is "restrict-only", not merely "ignored".
//
// The grant store is the REAL StoreGrants over a real SQLite file (Art.2),
// reusing grant_test.go's fixture rather than a double.
//
// SPORT: internal/policy Engine/ADDED (P1-E09-W2-S18-T1).
package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// destructiveCap is a registered capability whose own class sits on the
// deny rung. It exercises layer 4's one-directional fold: the capability
// can raise the evaluated level, never lower it.
func destructiveCap() Capability {
	return Capability{
		Name:          "vault.rotate",
		Desc:          "rotate a stored secret",
		DefaultPolicy: ClassDestructivePrivileged,
	}
}

// engineFixture is a grant fixture plus an engine over it, with the
// most permissive profile the type can express already installed.
type engineFixture struct {
	*grantFixture
	ctrl   *Controller
	engine *Engine
}

// newEngineFixture builds the engine over the real store, registers the
// destructive capability alongside the fixture's read capability, and
// installs the allow-everything profile DIRECTLY (no config path can
// produce it — that is the point: even if one could, the layers below hold).
func newEngineFixture(t *testing.T) *engineFixture {
	t.Helper()
	f := newGrantFixture(t)
	if err := f.reg.Add(context.Background(), destructiveCap()); err != nil {
		t.Fatalf("registering the destructive capability: %v", err)
	}
	ctrl := NewController(nil)
	ctrl.profile.Store(allowAllProfile())
	eng, err := NewEngine(f.reg, f.store, ctrl)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return &engineFixture{grantFixture: f, ctrl: ctrl, engine: eng}
}

// readRequest is a request for the fixture's read capability at level.
func readRequest(level RiskLevel) EvalRequest {
	return EvalRequest{Subject: testSubject(), Capability: readCap().Name, Level: level}
}

// TestPermissiveProfileCannotReleaseALowerLayerDenial is the property the
// whole design rests on. Each row is a refusal owned by a layer beneath the
// autonomy profile; the profile in force is allow-everything.
func TestPermissiveProfileCannotReleaseALowerLayerDenial(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t)

	cases := []struct {
		name    string
		req     EvalRequest
		wantErr bool
	}{
		{"classifier top rung", readRequest(L4), false},
		{"classifier could not parse (unset level reads as L4)", readRequest(0), false},
		{"classifier out-of-range level", readRequest(RiskLevel(200)), false},
		{"capability is not registered", EvalRequest{
			Subject: testSubject(), Capability: "memory.forge", Level: L0,
		}, true},
		{"capability name is malformed", EvalRequest{
			Subject: testSubject(), Capability: "Memory Read;rm -rf /", Level: L0,
		}, true},
		{"capability class raises an L0 request to L4", EvalRequest{
			Subject: testSubject(), Capability: destructiveCap().Name, Level: L0,
		}, false},
		{"subject names nobody", EvalRequest{Capability: readCap().Name, Level: L0}, true},
	}
	for _, tc := range cases {
		out, err := f.engine.Evaluate(ctx, tc.req)
		if out.Verdict != VerdictDeny {
			t.Errorf("%s: the allow-everything profile released it: %+v", tc.name, out)
		}
		if out.AutoAdvance {
			t.Errorf("%s: auto-advanced a denied action", tc.name)
		}
		if tc.wantErr && err == nil {
			t.Errorf("%s: denied with no error to explain it", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: a verdict was reported as an error: %v", tc.name, err)
		}
	}
}

// TestUnknownCapabilityDeniesByIdentity pins the grant model's own refusal:
// it arrives as ErrCapabilityNotFound, so a caller classifies it by
// identity rather than by reading the message.
func TestUnknownCapabilityDeniesByIdentity(t *testing.T) {
	f := newEngineFixture(t)
	out, err := f.engine.Evaluate(context.Background(), EvalRequest{
		Subject: testSubject(), Capability: "memory.forge", Level: L0,
	})
	if !errors.Is(err, ErrCapabilityNotFound) {
		t.Fatalf("error = %v, want ErrCapabilityNotFound", err)
	}
	if out.Verdict != VerdictDeny || out.Layer != LayerFailClosed {
		t.Errorf("outcome = %+v, want a fail-closed deny", out)
	}
	if out.Level != L4 {
		t.Errorf("an unresolvable capability evaluated at %s, want L4", out.Level)
	}
}

// TestStandingGrantCannotPreAuthoriseL4 covers the other lower-layer
// refusal that a permissive profile must not release, from the opposite
// direction: an explicit, valid, unexpired grant on a destructive
// capability still denies, because §5.15 reserves that rung for same-turn
// authorization.
func TestStandingGrantCannotPreAuthoriseL4(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t)
	g := Grant{
		Subject:    testSubject(),
		Capability: destructiveCap().Name,
		ScopeClass: corpus.VisibilityTeam,
	}
	if err := f.store.Grant(ctx, g); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	out, err := f.engine.Evaluate(ctx, EvalRequest{
		Subject: testSubject(), Capability: destructiveCap().Name, Level: L0,
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Verdict != VerdictDeny || out.AutoAdvance {
		t.Errorf("a standing grant pre-authorised an L4 action: %+v", out)
	}
	if out.Level != L4 {
		t.Errorf("evaluated at %s, want the capability's own L4 rung", out.Level)
	}
}

// TestProfileRestrictsWhatALowerLayerPermitted is the other half of
// "restrict-only": a grant that does match is still subject to the running
// profile's auto-advance ceiling, and a profile change takes effect on the
// next call.
func TestProfileRestrictsWhatALowerLayerPermitted(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t)
	if err := f.store.Grant(ctx, validGrant()); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	out, err := f.engine.Evaluate(ctx, readRequest(L0))
	if err != nil || out.Verdict != VerdictAllow || out.Layer != LayerStandingGrant {
		t.Fatalf("granted L0 read = %+v, %v; want a standing-grant allow", out, err)
	}
	if !out.AutoAdvance {
		t.Error("an allow-tier L0 read under an allow-everything profile did not auto-advance")
	}

	// Tighten the profile: L0 now asks. The grant still matches at layer
	// 3, but the profile's ceiling removes auto-advance.
	if err := f.ctrl.Apply(ctx, policyTree(map[string]interface{}{
		"autonomy_profile": "balanced",
		"ask":              levelList("L0"),
	})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err = f.engine.Evaluate(ctx, readRequest(L0))
	if err != nil {
		t.Fatalf("Evaluate after the swap: %v", err)
	}
	if out.AutoAdvance {
		t.Error("auto-advance survived a profile that no longer permits it at L0")
	}
}

// TestEngineHasNoCache asserts the no-cache decision the grant model
// already made, now across both moving parts: a revoked grant and a
// swapped profile are both in force on the very next Evaluate.
func TestEngineHasNoCache(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t)
	if err := f.ctrl.Apply(ctx, policyTree(map[string]interface{}{"autonomy_profile": "balanced"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := f.store.Grant(ctx, validGrant()); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	if out, _ := f.engine.Evaluate(ctx, readRequest(L2)); out.Verdict != VerdictAllow {
		t.Fatalf("granted L2 = %+v, want allow", out)
	}
	if err := f.store.Revoke(ctx, testSubject(), readCap().Name); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	out, err := f.engine.Evaluate(ctx, readRequest(L2))
	if err != nil {
		t.Fatalf("Evaluate after revoke: %v", err)
	}
	if out.Verdict != VerdictAsk || out.Layer != LayerAutonomyProfile {
		t.Errorf("after revocation the L2 read = %+v, want the profile's ask", out)
	}
	// And a profile swap is equally immediate.
	if err := f.ctrl.Apply(ctx, policyTree(map[string]interface{}{
		"autonomy_profile": "balanced", "deny": levelList("L2"),
	})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if out, _ := f.engine.Evaluate(ctx, readRequest(L2)); out.Verdict != VerdictDeny {
		t.Errorf("after the tightening swap the L2 read = %+v, want deny", out)
	}
}

// TestEngineWithoutAProfileFailsClosed covers layer 6: an engine whose
// controller has loaded nothing denies and says so.
func TestEngineWithoutAProfileFailsClosed(t *testing.T) {
	ctx := context.Background()
	f := newGrantFixture(t)
	ctrl := NewController(nil)
	eng, err := NewEngine(f.reg, f.store, ctrl)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	if eng.HasProfile() {
		t.Error("HasProfile is true before any config was applied")
	}
	out, err := eng.Evaluate(ctx, readRequest(L0))
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if out.Verdict != VerdictDeny || out.Layer != LayerFailClosed {
		t.Errorf("an unloaded engine answered %+v, want a fail-closed deny", out)
	}
	if err := ctrl.Apply(ctx, policyTree(map[string]interface{}{"autonomy_profile": "balanced"})); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !eng.HasProfile() {
		t.Error("HasProfile is false after a successful load")
	}
	if out, _ := eng.Evaluate(ctx, readRequest(L0)); out.Verdict != VerdictAllow {
		t.Errorf("after loading, the L0 read = %+v, want allow", out)
	}
}
