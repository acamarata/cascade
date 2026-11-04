//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// capabilityCheckFixture is the daemon registry with the mcp-profile wiring
// run over an engine built by newPolicyEngine, plus the minter whose
// principals the registry's access check resolves.
type capabilityCheckFixture struct {
	reg    *rpc.Registry
	parts  policyEngineParts
	minter *principal.Minter
	now    time.Time
}

func newCapabilityCheckFixture(t *testing.T) capabilityCheckFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", home)
	f := newPolicyEngineFixture(t)
	parts, err := newPolicyEngine(context.Background(), policyEngineInputs{
		Store: f.store, Clock: f.clock, Capabilities: mcpToolCapabilities(), Paths: f.paths,
	})
	if err != nil {
		t.Fatalf("newPolicyEngine: %v", err)
	}
	minter := principal.NewMinter(f.clock)
	now := f.clock.Now()
	reg := rpc.NewRegistry()
	reg.SetAccessPolicy(func(tok string) (principal.Principal, error) { return minter.Resolve(tok, now) })
	w := &daemonWiring{Ctx: context.Background(), Registry: reg,
		Deps: daemonRuntime{Policy: &policyWiring{policyEngineParts: parts}}}
	wired := false
	for _, r := range daemonRegistrations {
		if r.Name == "mcp-profile-capabilities" {
			if err := r.Wire(w); err != nil {
				t.Fatalf("wire: %v", err)
			}
			wired = true
		}
	}
	if !wired || !reg.Registered(mcpCapabilityCheckMethod) {
		t.Fatalf("mcp-profile-capabilities did not mount %s", mcpCapabilityCheckMethod)
	}
	return capabilityCheckFixture{reg: reg, parts: parts, minter: minter, now: now}
}

// principalCtx mints a token for id and returns a ctx carrying the principal
// the daemon resolves from it.
func (f capabilityCheckFixture) principalCtx(t *testing.T, id string) context.Context {
	t.Helper()
	tok, err := f.minter.Mint(principal.Principal{ID: id, Kind: principal.KindMCP, Scope: "project:test"}, time.Hour)
	if err != nil {
		t.Fatalf("mint %s: %v", id, err)
	}
	p, err := f.minter.Resolve(tok, f.now)
	if err != nil {
		t.Fatalf("resolve %s: %v", id, err)
	}
	return principal.WithCaller(context.Background(), principal.Caller{Principal: &p})
}

func (f capabilityCheckFixture) grant(t *testing.T, s policy.Subject, capability string) {
	t.Helper()
	if err := f.parts.Grants.Grant(context.Background(), policy.Grant{
		Subject: s, Capability: capability, ScopeClass: corpus.VisibilityTeam,
	}); err != nil {
		t.Fatalf("grant %v: %v", s, err)
	}
}

// check dispatches the verb through the registry and returns the verdict, or
// the wire error.
func (f capabilityCheckFixture) check(ctx context.Context, params string) (string, *rpc.ErrorObject) {
	res, eo := f.reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: mcpCapabilityCheckMethod,
		Params: json.RawMessage(params)})
	if eo != nil {
		return "", eo
	}
	return res.(policy.CheckResult).Verdict, nil
}

func (f capabilityCheckFixture) wantVerdict(ctx context.Context, t *testing.T, who, want string) {
	t.Helper()
	got, eo := f.check(ctx, `{"capability":"mcp.profile.full"}`)
	if eo != nil {
		t.Fatalf("%s: unexpected error %v", who, eo)
	}
	if got != want {
		t.Fatalf("%s: verdict %q, want %q", who, got, want)
	}
}

func TestMCPCapabilityCheckBindsCallerPrincipal(t *testing.T) {
	f := newCapabilityCheckFixture(t)
	const capFull = "mcp.profile.full"
	aSubject := policy.Subject{Kind: policy.SubjectAgent, ID: "principal-a"}
	f.grant(t, aSubject, capFull)
	f.grant(t, mcpToolSubject, capFull)
	f.grant(t, policy.Subject{Kind: policy.SubjectUser, ID: "principal-b"}, capFull)
	f.grant(t, policy.Subject{Kind: policy.SubjectUser, ID: "owner"}, capFull)

	aCtx := f.principalCtx(t, "principal-a")
	bCtx := f.principalCtx(t, "principal-b")
	f.wantVerdict(aCtx, t, "A with grant", "allow")
	f.wantVerdict(bCtx, t, "B without grant", "deny")

	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	f.wantVerdict(owner, t, "owner CLI", "deny")
	if got, eo := f.check(context.Background(), `{"capability":"mcp.profile.full"}`); eo == nil && got == "allow" {
		t.Fatalf("tokenless caller: verdict %q, want a refusal", got)
	}

	// A caller-declared subject is refused, never evaluated.
	_, eo := f.check(bCtx, `{"capability":"mcp.profile.full","subject":{"kind":"agent","id":"principal-a"}}`)
	if eo == nil {
		t.Fatalf("params with subject: no error")
	}
	if eo.Code != cascade.KindInvalidInput.JSONRPCCode() || !strings.Contains(eo.Message, "mcp.capability.check: params are not readable") {
		t.Fatalf("params with subject: got %d %q, want KindInvalidInput params-not-readable", eo.Code, eo.Message)
	}
	_, eo = f.check(aCtx, `{"capability":"policy.check"}`)
	if eo == nil || eo.Code != cascade.KindInvalidInput.JSONRPCCode() ||
		!strings.Contains(eo.Message, "must be an mcp.profile.* or mcp.tool.* name") {
		t.Fatalf("non-MCP capability: got %v, want KindInvalidInput", eo)
	}

	if err := f.parts.Grants.Revoke(context.Background(), aSubject, capFull); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	f.wantVerdict(aCtx, t, "A after revoke", "deny")
}
