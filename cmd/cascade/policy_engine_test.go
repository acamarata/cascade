// Purpose: the proof that every production policy engine in this binary is
//
//	built through newPolicyEngine, so a deny row stored in the policy domain
//	and the operator's [policy] profile reach the MCP tool filter and the
//	github-ci verbs as well as the daemon. Each
//	assertion runs a CONTROL first (the same input before the row or the
//	profile exists) so a filter that denies everything, or an engine that
//	never reads the store, cannot pass by being wrong in the safe direction.
//
// SPORT: cmd/cascade/policy (ADD) -- constructor verification.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// policyEngineTestEpoch is the instant the frozen clock in this file starts at.
var policyEngineTestEpoch = time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)

// policyEngineFixture is a store, a clock and a temp data directory with no
// config.toml yet.
type policyEngineFixture struct {
	store provider.Store
	clock runtime.Clock
	paths runtime.PathProvider
}

func newPolicyEngineFixture(t *testing.T) policyEngineFixture {
	t.Helper()
	return policyEngineFixture{
		store: storetest.NewMemStore(),
		clock: testkit.NewFrozenClock(policyEngineTestEpoch),
		paths: auditTestPaths(t),
	}
}

// writeConfig writes body as the fixture's config.toml.
func (f policyEngineFixture) writeConfig(t *testing.T, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.paths.ConfigPath()), 0o700); err != nil {
		t.Fatalf("config dir: %v", err)
	}
	if err := os.WriteFile(f.paths.ConfigPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("write config.toml: %v", err)
	}
}

// denyRow stores a deny row the way the deny-list engine does: through a
// StoreDenyList over the runtime store, for every action class.
func (f policyEngineFixture) denyRow(t *testing.T, pattern string) {
	t.Helper()
	list, err := policy.NewStoreDenyList(f.store)
	if err != nil {
		t.Fatalf("deny list: %v", err)
	}
	if err := list.Add(context.Background(), pattern, policy.ActionClass(0)); err != nil {
		t.Fatalf("store deny row %q: %v", pattern, err)
	}
}

// TestMCPPolicyFilterAppliesStoredDenyRow: a deny row stored for the MCP
// listing action hides a read tool the default profile lists. The control
// proves the tool is listed before the row, so DenyAllFilter cannot satisfy
// the second half by accident; the row is then the only thing that changed.
func TestMCPPolicyFilterAppliesStoredDenyRow(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)

	before := mcpPolicyFilter(ctx, f.store, f.clock, f.paths)
	if !before.Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatalf("control: the read capability is not listed (filter %T); the deny assertion below would prove nothing", before)
	}
	f.denyRow(t, "mcp.tools/*")

	after := mcpPolicyFilter(ctx, f.store, f.clock, f.paths)
	if _, denyAll := after.(mcp.DenyAllFilter); denyAll {
		t.Fatal("a readable deny store produced DenyAllFilter; the row must be applied by a real policy filter")
	}
	if after.Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatal("the MCP filter lists a tool a stored deny row names; the engine is not reading the deny store")
	}
}

// TestGithubCIEngineAppliesStoredDenyRow: the engine the github-ci verbs
// decide with refuses at the deny-list layer for an action a stored row
// names, and does not refuse there for the same action before the row.
func TestGithubCIEngineAppliesStoredDenyRow(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	subject := policy.Subject{Kind: policy.SubjectUser, ID: "github-ci-test"}
	req := policy.EvalRequest{
		Subject: subject, Capability: ci.MergeOnGreenCapability,
		Action: "gh pr merge 12 --squash", Summary: "deny-row probe",
	}

	engine, _, err := githubCIEngine(ctx, f.store, f.clock, f.paths)
	if err != nil {
		t.Fatalf("githubCIEngine: %v", err)
	}
	control, err := engine.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("control evaluate: %v", err)
	}
	if control.Layer == policy.LayerDenyList {
		t.Fatalf("control: the action is refused by the deny-list layer with no row stored (%s)", control.Reason)
	}

	f.denyRow(t, "gh pr merge *")
	engine, _, err = githubCIEngine(ctx, f.store, f.clock, f.paths)
	if err != nil {
		t.Fatalf("githubCIEngine (after row): %v", err)
	}
	out, err := engine.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Verdict != policy.VerdictDeny || out.Layer != policy.LayerDenyList ||
		!strings.Contains(out.Reason, "configured deny-list") {
		t.Fatalf("outcome = %v at %v (%s), want a deny from the configured deny-list row", out.Verdict, out.Layer, out.Reason)
	}
}

// TestMCPPolicyFilterHonoursOperatorProfile: a config.toml [policy] overlay
// stricter than the default changes the MCP verdict. NewController(nil) --
// the engine every MCP process used to get -- ignores the file, so the
// control (no config.toml) lists the tool and the strict file must not.
func TestMCPPolicyFilterHonoursOperatorProfile(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)

	if !mcpPolicyFilter(ctx, f.store, f.clock, f.paths).Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatal("control: with no config.toml the read capability is not listed")
	}
	f.writeConfig(t, "schema_version = 1\n\n[policy]\nask = [\"L0\", \"L1\"]\n")

	filter := mcpPolicyFilter(ctx, f.store, f.clock, f.paths)
	if _, denyAll := filter.(mcp.DenyAllFilter); denyAll {
		t.Fatal("a well-formed [policy] overlay produced DenyAllFilter; the profile must be applied, not refused")
	}
	if filter.Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatal("the MCP filter ignored the operator's [policy] profile: the tool is still listed under ask = [L0, L1]")
	}
}

// TestPolicyEngineMalformedPolicyRefuses: an unparseable [policy] section is
// an error from the constructor, DenyAllFilter from the MCP path and the
// returned error from github-ci. A nil config is not malformed: it is the
// default profile.
func TestPolicyEngineMalformedPolicyRefuses(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	bad := &runtime.Config{Extra: map[string]any{"policy": map[string]any{"autonomy_profile": "unlimited"}}}

	if _, err := newPolicyEngine(ctx, policyEngineInputs{Store: f.store, Clock: f.clock, Config: bad, Paths: f.paths}); err == nil {
		t.Fatal("newPolicyEngine accepted an unknown autonomy profile; it must refuse, not fall back")
	}
	if _, err := newPolicyEngine(ctx, policyEngineInputs{Store: f.store, Clock: f.clock, Config: nil, Paths: f.paths}); err != nil {
		t.Fatalf("a nil config (no config file) must build the default profile: %v", err)
	}

	if !mcpPolicyFilter(ctx, f.store, f.clock, f.paths).Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatal("control: the read capability is not listed before the config is broken")
	}
	f.writeConfig(t, "schema_version = 1\n\n[policy]\nautonomy_profile = \"unlimited\"\n")
	if _, ok := mcpPolicyFilter(ctx, f.store, f.clock, f.paths).(mcp.DenyAllFilter); !ok {
		t.Fatal("a malformed [policy] section did not produce mcp.DenyAllFilter")
	}
	if _, _, err := githubCIEngine(ctx, f.store, f.clock, f.paths); err == nil {
		t.Fatal("github-ci built an engine over a malformed [policy] section")
	}
	f.writeConfig(t, "schema_version = 1\n[policy\n")
	if _, ok := mcpPolicyFilter(ctx, f.store, f.clock, f.paths).(mcp.DenyAllFilter); !ok {
		t.Fatal("a config.toml that is not TOML did not produce mcp.DenyAllFilter")
	}
	if _, _, err := githubCIEngine(ctx, f.store, f.clock, f.paths); err == nil {
		t.Fatal("github-ci built an engine over a config.toml that is not TOML")
	}
}

// TestPolicyEngineConstructorRefusals: the inputs the constructor cannot
// build an honest engine without, and a deny store it cannot read.
func TestPolicyEngineConstructorRefusals(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	for name, in := range map[string]policyEngineInputs{
		"nil store":       {Clock: f.clock, Paths: f.paths},
		"typed-nil store": {Store: (*storetest.MemStore)(nil), Clock: f.clock, Paths: f.paths},
		"nil clock":       {Store: f.store, Paths: f.paths},
	} {
		_, err := newPolicyEngine(ctx, in)
		if got, ok := cascade.KindOf(err); !ok || got != cascade.KindInvalidInput {
			t.Fatalf("%s: kind = %v (%v), want KindInvalidInput", name, got, err)
		}
	}
	if _, err := newPolicyEngine(ctx, policyEngineInputs{Store: f.store, Clock: f.clock}); err == nil {
		t.Fatal("newPolicyEngine built an engine with no path provider to bind the audit log's vault to")
	}
	if err := f.store.Put(ctx, "policy", "denylist/zz", []byte("{not json")); err != nil {
		t.Fatalf("seed corrupt deny row: %v", err)
	}
	if _, err := newPolicyEngine(ctx, policyEngineInputs{Store: f.store, Clock: f.clock, Paths: f.paths}); err == nil {
		t.Fatal("newPolicyEngine built an engine over a deny store holding an unreadable row")
	}
}

// TestPolicyEnginesShareRedactingLog: the engine parts the daemon composition
// root takes (newPolicyEngine) and the github-ci engine in one process audit
// through the very pointer openAuditLog returns for their store. This proves
// identity, so no second, unredacted Log can sit beside the shared one;
// that the shared Log redacts is proven by audit_log_test.go.
func TestPolicyEnginesShareRedactingLog(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	parts, err := newPolicyEngine(ctx, policyEngineInputs{
		Store: f.store, Clock: f.clock, Capabilities: mcpToolCapabilities(), Paths: f.paths,
	})
	if err != nil {
		t.Fatalf("newPolicyEngine: %v", err)
	}
	_, ciLog, err := githubCIEngine(ctx, f.store, f.clock, f.paths)
	if err != nil {
		t.Fatalf("githubCIEngine: %v", err)
	}
	want, err := openAuditLog(f.store, f.clock, nil, f.paths)
	if err != nil {
		t.Fatalf("openAuditLog: %v", err)
	}
	if parts.Audit == nil || parts.Audit != want {
		t.Fatalf("the constructor's audit log %p is not openAuditLog's %p for the store", parts.Audit, want)
	}
	if got, ok := ciLog.(*audit.Log); !ok || got != want {
		t.Fatalf("github-ci audits through %T %p, want the shared *audit.Log %p", ciLog, ciLog, want)
	}
}

// TestPolicyEngineConstructorAppliesStoredDenyRow: the engine the constructor
// itself returns (the daemon's, before any non-daemon caller swaps in its own
// deny list) refuses at the deny-list layer for an action a stored row names,
// and does not before the row. Without this a constructor that dropped its
// deny-list attachment would stay green behind the MCP and github-ci callers,
// which attach their own.
func TestPolicyEngineConstructorAppliesStoredDenyRow(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	in := policyEngineInputs{Store: f.store, Clock: f.clock, Capabilities: githubCICapabilities(), Paths: f.paths}
	req := policy.EvalRequest{
		Subject:    policy.Subject{Kind: policy.SubjectUser, ID: "constructor-test"},
		Capability: ci.MergeOnGreenCapability, Action: "gh pr merge 12 --squash", Summary: "constructor deny probe",
	}
	parts, err := newPolicyEngine(ctx, in)
	if err != nil {
		t.Fatalf("newPolicyEngine: %v", err)
	}
	if out, err := parts.Engine.Evaluate(ctx, req); err != nil || out.Layer == policy.LayerDenyList {
		t.Fatalf("control: the deny-list layer decided with no row stored: %v (%v)", out.Layer, err)
	}
	f.denyRow(t, "gh pr merge *")
	parts, err = newPolicyEngine(ctx, in)
	if err != nil {
		t.Fatalf("newPolicyEngine (after row): %v", err)
	}
	out, err := parts.Engine.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Verdict != policy.VerdictDeny || out.Layer != policy.LayerDenyList {
		t.Fatalf("constructor engine decided %v at %v, want a deny-list deny: it dropped the stored deny list", out.Verdict, out.Layer)
	}
}
