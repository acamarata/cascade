// Purpose: the guard-rail half of the policy engine constructor's proof: the
//
//	non-daemon engines read the deny rows again on every decision, a
//	config.toml that is present but cannot be followed or read refuses, and
//	the two production entry points (mcpToolFilter, buildGitHubCIPolicy) work
//	over the real runtime store rather than only the injected one the other
//	tests use. Split from policy_engine_test.go to stay under the file cap.
//
// SPORT: cmd/cascade/policy (ADD) -- constructor verification.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestPolicyEngineRefusesAnUnreadableConfig: a config.toml that exists but the
// process may not read is a refusal on both surfaces, never the default profile.
func TestPolicyEngineRefusesAnUnreadableConfig(t *testing.T) {
	if goruntime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("file modes do not deny a read on windows or to root; the guard under test is not OS-specific")
	}
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	f.writeConfig(t, "schema_version = 1\n")
	if err := os.Chmod(f.paths.ConfigPath(), 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.paths.ConfigPath(), 0o600) })
	if _, ok := mcpPolicyFilter(ctx, f.store, f.clock, f.paths).(mcp.DenyAllFilter); !ok {
		t.Fatal("an unreadable config.toml did not produce mcp.DenyAllFilter")
	}
	if _, _, err := githubCIEngine(ctx, f.store, f.clock, f.paths); err == nil {
		t.Fatal("github-ci built an engine over a config.toml it may not read")
	}
}

// TestNonDaemonEnginesRereadTheDenyList: a row written AFTER the MCP filter
// and the github-ci engine were built still refuses on their next decision.
// StoreDenyList caches what it read, so an engine holding one for the life of
// a stdio MCP server (or a github-ci wait that lasts minutes) would keep
// answering from the rows it started with.
func TestNonDaemonEnginesRereadTheDenyList(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	filter := mcpPolicyFilter(ctx, f.store, f.clock, f.paths)
	engine, _, err := githubCIEngine(ctx, f.store, f.clock, f.paths)
	if err != nil {
		t.Fatalf("githubCIEngine: %v", err)
	}
	req := policy.EvalRequest{
		Subject:    policy.Subject{Kind: policy.SubjectUser, ID: "github-ci-test"},
		Capability: ci.MergeOnGreenCapability, Action: "gh pr merge 12 --squash", Summary: "reread probe",
	}
	if !filter.Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatal("control: the read capability is not listed before the row exists")
	}
	if out, err := engine.Evaluate(ctx, req); err != nil || out.Layer == policy.LayerDenyList {
		t.Fatalf("control: the deny-list layer decided before any row exists: %v (%v)", out.Layer, err)
	}

	f.denyRow(t, "mcp.tools/*")
	f.denyRow(t, "gh pr merge *")

	if filter.Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatal("the MCP filter built before the row still lists the tool: its deny list is a stale snapshot")
	}
	out, err := engine.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Verdict != policy.VerdictDeny || out.Layer != policy.LayerDenyList {
		t.Fatalf("github-ci engine built before the row decided %v at %v, want a deny-list deny: stale snapshot", out.Verdict, out.Layer)
	}
}

// TestPolicyEngineRefusesAnUnfollowableConfig: a config.toml that exists as a
// symlink to nothing is an operator's file we cannot read, not an absent
// config. runtime.Load reads it as absent, so without the guard in
// loadPolicyConfig every surface would run the default profile.
func TestPolicyEngineRefusesAnUnfollowableConfig(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.paths.ConfigPath()), 0o700); err != nil {
		t.Fatalf("config dir: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone.toml"), f.paths.ConfigPath()); err != nil {
		if goruntime.GOOS == "windows" {
			t.Skipf("windows: creating a symlink needs a privilege this runner lacks (%v); loadPolicyConfig's Lstat/Stat guard is not OS-specific", err)
		}
		t.Fatalf("symlink: %v", err)
	}
	if _, ok := mcpPolicyFilter(ctx, f.store, f.clock, f.paths).(mcp.DenyAllFilter); !ok {
		t.Fatal("a dangling config.toml did not produce mcp.DenyAllFilter")
	}
	if _, _, err := githubCIEngine(ctx, f.store, f.clock, f.paths); err == nil {
		t.Fatal("github-ci built an engine over a config.toml it cannot read")
	}
	if err := os.Remove(f.paths.ConfigPath()); err != nil {
		t.Fatalf("remove link: %v", err)
	}
	if _, ok := mcpPolicyFilter(ctx, f.store, f.clock, f.paths).(mcp.DenyAllFilter); ok {
		t.Fatal("control: an absent config.toml produced DenyAllFilter; absent must be the default profile")
	}
}

// TestProductionEntryPointsReadTheRuntimeStore drives the two production
// entry points (mcpToolFilter and buildGitHubCIPolicy) over the real runtime
// store the daemon opens, so a regression in the store opening, the config
// load or the wrapper around the constructor cannot hide behind the
// store-injected helpers the other tests use. Windows has no runtime store at
// this tier (openMCPPolicyStore refuses), which is asserted rather than skipped.
func TestProductionEntryPointsReadTheRuntimeStore(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	if goruntime.GOOS == "windows" {
		assertNoRuntimeStoreOnWindows(t, f)
		return
	}
	filter, closeFilter := mcpToolFilter(f.paths, f.clock)
	if closeFilter == nil {
		t.Fatalf("mcpToolFilter returned no closer (filter %T): the runtime store did not open", filter)
	}
	if !filter.Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatalf("control: the production MCP filter (%T) does not list a read tool before any row", filter)
	}
	closeFilter()

	store, closeStore, err := openMCPPolicyStore(f.paths, f.clock)
	if err != nil {
		t.Fatalf("open runtime store: %v", err)
	}
	f.store = store
	f.denyRow(t, "mcp.tools/*")
	f.denyRow(t, "gh pr merge *")
	closeStore()

	filter, closeFilter = mcpToolFilter(f.paths, f.clock)
	_, denyAll := filter.(mcp.DenyAllFilter)
	listed := filter.Allow(ctx, coretools.CapabilityContextRead)
	if closeFilter != nil {
		closeFilter() // the runtime store takes one exclusive opener at a time
	}
	if denyAll || listed {
		t.Fatalf("the production MCP filter (%T) lists a tool a row in the runtime store names (deny-all %v)", filter, denyAll)
	}
	engine, _, closeCI, err := buildGitHubCIPolicy(ctx, f.paths, f.clock)
	if err != nil {
		t.Fatalf("buildGitHubCIPolicy: %v", err)
	}
	t.Cleanup(closeCI)
	out, err := engine.Evaluate(ctx, policy.EvalRequest{
		Subject:    policy.Subject{Kind: policy.SubjectUser, ID: "github-ci-test"},
		Capability: ci.MergeOnGreenCapability, Action: "gh pr merge 12 --squash", Summary: "production entry probe",
	})
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Verdict != policy.VerdictDeny || out.Layer != policy.LayerDenyList {
		t.Fatalf("the production github-ci engine decided %v at %v, want a deny-list deny from the runtime store", out.Verdict, out.Layer)
	}
}

// assertNoRuntimeStoreOnWindows asserts the documented tier-2 refusal: with no
// runtime store, the MCP filter serves nothing and github-ci builds no engine.
func assertNoRuntimeStoreOnWindows(t *testing.T, f policyEngineFixture) {
	t.Helper()
	filter, closeFilter := mcpToolFilter(f.paths, f.clock)
	if closeFilter != nil {
		t.Cleanup(closeFilter)
	}
	if _, ok := filter.(mcp.DenyAllFilter); !ok {
		t.Fatalf("windows built a real MCP filter (%T) over a store that does not exist", filter)
	}
	if _, _, closeCI, err := buildGitHubCIPolicy(context.Background(), f.paths, f.clock); err == nil {
		closeCI()
		t.Fatal("windows built a github-ci engine over a store that does not exist")
	}
}

// denyScanFailStore is a store whose Scan can be made to fail after the engines
// were built over it: the deny-row read at decision time, not construction.
type denyScanFailStore struct {
	provider.Store
	fail atomic.Bool
}

// Scan fails once fail is set, and otherwise reads through.
func (s *denyScanFailStore) Scan(ctx context.Context, namespace, prefix string) (provider.Iterator, error) {
	if s.fail.Load() {
		return nil, errors.New("scan broken for the test")
	}
	return s.Store.Scan(ctx, namespace, prefix)
}

// TestNonDaemonEnginesFailClosedWhenTheDenyReadFails: the deny rows are read
// at decision time on the MCP and github-ci paths, so a read that fails AFTER
// the engines were built must refuse, not fall through to the next layer.
func TestNonDaemonEnginesFailClosedWhenTheDenyReadFails(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	store := &denyScanFailStore{Store: f.store}
	f.store = store
	filter := mcpPolicyFilter(ctx, f.store, f.clock, f.paths)
	engine, _, err := githubCIEngine(ctx, f.store, f.clock, f.paths)
	if err != nil {
		t.Fatalf("githubCIEngine: %v", err)
	}
	req := policy.EvalRequest{
		Subject:    policy.Subject{Kind: policy.SubjectUser, ID: "github-ci-test"},
		Capability: ci.MergeOnGreenCapability, Action: "gh pr merge 12 --squash", Summary: "scan failure probe",
	}
	if !filter.Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatal("control: the read capability is not listed while the deny store reads")
	}
	if out, err := engine.Evaluate(ctx, req); err != nil || out.Layer == policy.LayerDenyList {
		t.Fatalf("control: the deny-list layer decided while the store reads: %v (%v)", out.Layer, err)
	}

	store.fail.Store(true)

	if filter.Allow(ctx, coretools.CapabilityContextRead) {
		t.Fatal("the MCP filter lists a tool although the deny rows cannot be read")
	}
	out, err := engine.Evaluate(ctx, req)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if out.Verdict != policy.VerdictDeny || out.Layer != policy.LayerDenyList || !strings.Contains(out.Reason, "could not be read") {
		t.Fatalf("github-ci decided %v at %v (%s), want a deny-list deny that says the list could not be read", out.Verdict, out.Layer, out.Reason)
	}
}

// TestPolicyEngineRefusesADirectoryConfig: config.toml that is a directory is
// a refusal on every OS and for every user, root included (the unreadable-file
// test cannot run as root), and control: removing it returns the default profile.
func TestPolicyEngineRefusesADirectoryConfig(t *testing.T) {
	ctx := context.Background()
	f := newPolicyEngineFixture(t)
	if err := os.MkdirAll(f.paths.ConfigPath(), 0o700); err != nil {
		t.Fatalf("mkdir config.toml: %v", err)
	}
	if _, ok := mcpPolicyFilter(ctx, f.store, f.clock, f.paths).(mcp.DenyAllFilter); !ok {
		t.Fatal("a config.toml that is a directory did not produce mcp.DenyAllFilter")
	}
	if _, _, err := githubCIEngine(ctx, f.store, f.clock, f.paths); err == nil {
		t.Fatal("github-ci built an engine over a config.toml that is a directory")
	}
	if err := os.Remove(f.paths.ConfigPath()); err != nil {
		t.Fatalf("remove dir: %v", err)
	}
	if _, ok := mcpPolicyFilter(ctx, f.store, f.clock, f.paths).(mcp.DenyAllFilter); ok {
		t.Fatal("control: an absent config.toml produced DenyAllFilter")
	}
}

// noConfigPath is a path provider with no config.toml location.
type noConfigPath struct{ runtime.PathProvider }

func (noConfigPath) ConfigPath() string { return "" }

// TestPolicyEngineRefusesAMissingConfigPath: with no config.toml location the
// [policy] profile cannot be read, so the load refuses instead of selecting the
// default profile.
func TestPolicyEngineRefusesAMissingConfigPath(t *testing.T) {
	f := newPolicyEngineFixture(t)
	for name, paths := range map[string]runtime.PathProvider{"nil": nil, "empty path": noConfigPath{f.paths}} {
		if _, err := loadPolicyConfig(context.Background(), paths); err == nil {
			t.Fatalf("%s: loadPolicyConfig selected a profile with no config.toml path", name)
		}
	}
	if _, _, err := githubCIEngine(context.Background(), f.store, f.clock, noConfigPath{f.paths}); err == nil {
		t.Fatal("githubCIEngine built an engine with no config.toml path")
	}
}
