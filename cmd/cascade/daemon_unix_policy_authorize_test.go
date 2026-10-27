//go:build !windows

// Purpose: proves policy.authorize records a same-turn authorization only
// for an attested call to itself, that the authorization answers exactly
// one evaluation by the subject it was recorded for, that an unattested
// call leaves the ledger untouched, and that every refusal is audited. An
// attested call that is then refused has already ended the previous turn.
// Inputs: the daemon registry with the real elevation middleware over an
// injected in-memory custody (newElevatedDaemon), and an engine built by
// newPolicyEngine over a memory store with a configured deny row.
// Outputs: none (tests).
// Constraints: the witness comes only from the middleware; no keychain.
// SPORT: cmd/cascade policy.authorize tests (P1-SEC-10).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// deniedOverridable is an L0 action a configured deny row names: the row
// is overridable, and nothing past layer 1 refuses it outright.
const deniedOverridable = "ls /srv"

// recordingAudit is an audit.Writer that keeps every event and fails every
// append with fail when it is set.
type recordingAudit struct {
	mu   sync.Mutex
	rows []audit.Event
	fail error
}

func (r *recordingAudit) Append(_ context.Context, e audit.Event) (audit.Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, e)
	return audit.Record{}, r.fail
}

// outcomes lists every audited outcome, in order.
func (r *recordingAudit) outcomes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.rows {
		out = append(out, e.Outcome)
	}
	return out
}

type authorizeFixture struct {
	d     elevatedDaemon
	parts policyEngineParts
	audit *recordingAudit
}

// newAuthorizeFixture builds the engine through newPolicyEngine with deny
// rows for patterns, and mounts policy.authorize on the elevated registry
// exactly as the daemon wiring does.
func newAuthorizeFixture(t *testing.T, patterns ...string) authorizeFixture {
	t.Helper()
	f := newPolicyEngineFixture(t)
	for _, p := range patterns {
		f.denyRow(t, p)
	}
	parts, err := newPolicyEngine(context.Background(), policyEngineInputs{
		Store: f.store, Clock: f.clock, Capabilities: mcpToolCapabilities(), Paths: f.paths,
	})
	if err != nil {
		t.Fatalf("newPolicyEngine: %v", err)
	}
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
	rec := &recordingAudit{}
	mountPolicyAuthorize(d.Registry, parts, rec)
	return authorizeFixture{d: d, parts: parts, audit: rec}
}

// authorize sends policy.authorize for action through the attested caller.
func (f authorizeFixture) authorize(ctx context.Context, action string) (policyAuthorizeResult, error) {
	var res policyAuthorizeResult
	err := f.d.Call(ctx, policyAuthorizeMethod, policyAuthorizeParams{Action: action}, &res)
	return res, err
}

// layerOneDenies evaluates action as the MCP subject ctx carries and reports
// whether the deny list decided it.
func (f authorizeFixture) layerOneDenies(ctx context.Context, t *testing.T, action string) bool {
	t.Helper()
	out, err := f.parts.Engine.Evaluate(ctx, policy.EvalRequest{
		Subject: mcpCallSubject(ctx), Capability: coretools.CapabilityContextRead, Action: action,
	})
	if err != nil {
		t.Fatalf("Evaluate %q: %v", action, err)
	}
	return out.Verdict == policy.VerdictDeny && out.Layer == policy.LayerDenyList
}

// assertNothingLive fails when the ledger holds an authorization.
func (f authorizeFixture) assertNothingLive(t *testing.T) {
	t.Helper()
	if n := f.parts.SameTurn.Live(); n != 0 {
		t.Fatalf("ledger holds %d authorizations, want 0", n)
	}
}

func TestPolicyAuthorizeRPCRequiresAttestation(t *testing.T) {
	f := newAuthorizeFixture(t, deniedOverridable)
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	raw, _ := json.Marshal(policyAuthorizeParams{Action: deniedOverridable})
	_, eo := f.d.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: policyAuthorizeMethod, Params: raw})
	if eo == nil || eo.Code != cascade.RPCCodeElevationRequired || nonceOf(eo) == "" {
		t.Fatalf("unattested policy.authorize = %+v, want ELEVATION_REQUIRED with a nonce", eo)
	}
	f.assertNothingLive(t)
	if slices.Contains(f.audit.outcomes(), sameTurnGranted) {
		t.Fatal("an unattested call audited a grant")
	}
	// The handler the registry mounts, reached without the middleware's
	// witness, refuses and audits the refusal.
	if _, err := newPolicyAuthorizer(f.parts.SameTurn, f.audit).handle(ctx, raw); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("handler without a witness = %v", err)
	}
	f.assertNothingLive(t)
	if got := f.audit.outcomes(); !slices.Equal(got, []string{sameTurnUnattested}) {
		t.Fatalf("audited %v, want [%s]", got, sameTurnUnattested)
	}
	if !f.layerOneDenies(context.Background(), t, deniedOverridable) {
		t.Fatal("the deny row was overridden without an attested authorization")
	}
}

func TestPolicyAuthorizeRefusesWithoutWitness(t *testing.T) {
	f := newAuthorizeFixture(t, deniedOverridable)
	authz := newPolicyAuthorizer(f.parts.SameTurn, f.audit)
	raw, _ := json.Marshal(policyAuthorizeParams{Action: deniedOverridable})
	if _, err := authz.handle(context.Background(), raw); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("no witness: %v", err)
	}
	f.assertNothingLive(t)
	// A real witness, minted by the middleware for another verb over the
	// same params, must not be accepted.
	f.d.Registry.Register("vault.get", func(ctx context.Context, p json.RawMessage) (any, error) {
		w, ok := rpc.VerifiedElevationFrom(ctx)
		if !ok || w.Method() != "vault.get" {
			t.Errorf("the proxy ran without a vault.get witness (%t, %q)", ok, w.Method())
		}
		return authz.handle(ctx, p)
	})
	err := f.d.Call(context.Background(), "vault.get", policyAuthorizeParams{Action: deniedOverridable}, nil)
	if rpcCode(err) != cascade.RPCCodeElevationRequired {
		t.Fatalf("vault.get witness: %v", err)
	}
	f.assertNothingLive(t)
	if got := f.audit.outcomes(); !slices.Equal(got, []string{sameTurnUnattested, sameTurnUnattested}) {
		t.Fatalf("audited %v, want two %s rows", got, sameTurnUnattested)
	}
	if !f.layerOneDenies(context.Background(), t, deniedOverridable) {
		t.Fatal("a witness for another verb overrode the deny row")
	}
}

func TestAuthorizeOverridesOverridableDenyOnce(t *testing.T) {
	f := newAuthorizeFixture(t, deniedOverridable)
	ctx := context.Background()
	if !f.layerOneDenies(ctx, t, deniedOverridable) {
		t.Fatal("control: the deny row does not deny")
	}
	res, err := f.authorize(ctx, deniedOverridable)
	if err != nil || res.Nonce == "" || res.ExpiresAt.IsZero() {
		t.Fatalf("policy.authorize = %+v, %v", res, err)
	}
	if f.layerOneDenies(ctx, t, deniedOverridable) {
		t.Fatal("the first evaluation after the authorization was denied by the deny row")
	}
	if !f.layerOneDenies(ctx, t, deniedOverridable) {
		t.Fatal("the authorization answered a second evaluation")
	}
	if got := f.audit.outcomes(); !slices.Equal(got, []string{sameTurnGranted}) {
		t.Fatalf("audited %v, want [%s]", got, sameTurnGranted)
	}
}

func TestAuthorizeEndsPreviousWindow(t *testing.T) {
	f := newAuthorizeFixture(t, deniedOverridable, "ls /opt")
	ctx := context.Background()
	if _, err := f.authorize(ctx, deniedOverridable); err != nil {
		t.Fatalf("first policy.authorize: %v", err)
	}
	var wire map[string]any
	if err := f.d.Call(ctx, policyAuthorizeMethod, policyAuthorizeParams{Action: "ls /opt"}, &wire); err != nil {
		t.Fatalf("second policy.authorize: %v", err)
	}
	if _, n := wire["nonce"].(string); !n || len(wire) != 2 || wire["expires_at"] == nil {
		t.Fatalf("policy.authorize result = %v, want exactly nonce and expires_at", wire)
	}
	if n := f.parts.SameTurn.Live(); n != 1 {
		t.Fatalf("ledger holds %d authorizations after the second, want 1", n)
	}
	if !f.layerOneDenies(ctx, t, deniedOverridable) {
		t.Fatal("the first window survived the second authorization")
	}
	if f.layerOneDenies(ctx, t, "ls /opt") {
		t.Fatal("the second authorization does not answer")
	}
}

func TestPolicyAuthorizeAuditFailureRefuses(t *testing.T) {
	f := newAuthorizeFixture(t, deniedOverridable)
	f.audit.fail = errors.New("audit store unavailable")
	if _, err := f.authorize(context.Background(), deniedOverridable); err == nil {
		t.Fatal("policy.authorize succeeded with an audit log that cannot append")
	}
	f.assertNothingLive(t)
	if !f.layerOneDenies(context.Background(), t, deniedOverridable) {
		t.Fatal("an unaudited grant overrode the deny row")
	}
}

func TestUnconditionalDenyNotOverridable(t *testing.T) {
	f := newAuthorizeFixture(t)
	for _, action := range []string{"rm -rf /", "frobnicate --everything"} {
		if _, err := f.authorize(context.Background(), action); rpcCode(err) == 0 || err == nil {
			t.Fatalf("authorize %q = %v, want a refusal", action, err)
		}
		f.assertNothingLive(t)
	}
	if got := f.audit.outcomes(); !slices.Equal(got, []string{sameTurnUnconditional, sameTurnUnconditional}) {
		t.Fatalf("audited %v, want two %s rows", got, sameTurnUnconditional)
	}
	if !f.layerOneDenies(context.Background(), t, "rm -rf /") {
		t.Fatal("an L4 action passed the deny list")
	}
}

func TestElevationActionAuthorizeRefusedAndAudited(t *testing.T) {
	for _, action := range []string{"vault.get", policyAuthorizeMethod} {
		t.Run(action, func(t *testing.T) {
			f := newAuthorizeFixture(t)
			_, err := f.authorize(context.Background(), action)
			if rpcCode(err) != cascade.RPCCodeElevationRequired {
				t.Fatalf("authorize %q = %v, want ELEVATION_REQUIRED", action, err)
			}
			f.assertNothingLive(t)
			if got := f.audit.outcomes(); !slices.Equal(got, []string{policy.CodeSameTurnElevation}) {
				t.Fatalf("audited %v, want [%s]", got, policy.CodeSameTurnElevation)
			}
		})
	}
}
