//go:build !windows

// Purpose: proves the daemon checks the config baseline before any
// background subsystem is built, runs the most-restrictive guarded values
// for a missing or divergent baseline, and reports it in status.get.
// Inputs: temp config files, an in-memory or temp-dir store, composed
// daemons over a fresh HOME / CASCADE_HOME.
// Outputs: helpers the config.accept_loosening tests reuse.
// Constraints: no keychain; synthetic configs only.
// SPORT: cmd/cascade config baseline tests (P1-SEC-04).
package main

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

const (
	tightToml       = "[conductor]\nexternal_routing_enabled = false\n"
	routingLooser   = "[conductor]\nexternal_routing_enabled = true\n"
	spillLooserToml = "[conductor]\nspill_enabled = true\n"
)

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testLoadOpts(path string) runtime.LoadOptions {
	return runtime.LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}
}

// bootCheck writes body to path, loads it, and runs checkBootBaseline.
func bootCheck(t *testing.T, store provider.Store, path, body string, verify runtime.BaselineVerifier) (*runtime.Config, *configBaseline) {
	t.Helper()
	return bootCheckOpts(t, store, path, body, verify, testLoadOpts(path))
}

// bootCheckOpts is bootCheck loading with opts (an environment, say).
func bootCheckOpts(t *testing.T, store provider.Store, path, body string, verify runtime.BaselineVerifier, opts runtime.LoadOptions) (*runtime.Config, *configBaseline) {
	t.Helper()
	writeConfig(t, path, body)
	cfg, err := runtime.Load(context.Background(), opts)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	clock := runtime.NewSystemClock()
	bus := events.New(store, clock)
	t.Cleanup(func() { _ = bus.Close() })
	return cfg, checkBootBaseline(context.Background(), cfg, path, store, bus, clock, verify)
}

func boolKey(cfg *runtime.Config, key string) bool {
	switch key {
	case "spill_enabled":
		return runtime.EffectiveConfigOf(cfg).Conductor.SpillEnabled
	default:
		return runtime.EffectiveConfigOf(cfg).Conductor.ExternalRoutingEnabled
	}
}

// composeBaselineDaemon composes a daemon over deps with body as config.toml
// ("" for no file) and returns its registry and a close func.
func composeBaselineDaemon(t *testing.T, deps daemonDeps, body string) (*rpc.Registry, func()) {
	t.Helper()
	if body == "" {
		_ = os.Remove(deps.Paths.ConfigPath())
	} else {
		writeConfig(t, deps.Paths.ConfigPath(), body)
	}
	var reg *rpc.Registry
	_, cleanups, err := composeDaemon(context.Background(), deps, func(r *rpc.Registry, _ *rpc.SSEMux) { reg = r })
	closed := false
	closeFn := func() {
		if !closed {
			closed = true
			runCleanupsLIFO(cleanups)
		}
	}
	t.Cleanup(closeFn)
	if err != nil {
		t.Fatalf("composeDaemon: %v", err)
	}
	return reg, closeFn
}

// baselineStatus reads status.get's health and the config-baseline line.
func baselineStatus(t *testing.T, reg *rpc.Registry) (health, state, detail string) {
	t.Helper()
	ctx := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	res, eo := reg.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: "status.get"})
	if eo != nil {
		t.Fatalf("status.get: %v", eo)
	}
	raw, _ := json.Marshal(res)
	var st struct {
		Health     string `json:"health"`
		Subsystems []struct {
			Name   string `json:"name"`
			State  string `json:"state"`
			Detail string `json:"detail"`
		} `json:"subsystems"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	for _, s := range st.Subsystems {
		if s.Name == configBaselineSubsystem {
			return st.Health, s.State, s.Detail
		}
	}
	t.Fatalf("status.get has no %s subsystem: %s", configBaselineSubsystem, raw)
	return "", "", ""
}

func freshDaemonHome(t *testing.T) daemonDeps {
	t.Helper()
	home := t.TempDir()
	for _, name := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(name, home)
	}
	return newRunTestDeps(t, nonexistentExecutable(t))
}

func TestBootBaselineCheckedBeforeBackgroundSubsystems(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "daemon_unix.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]token.Pos{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "composeDaemon" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, lit := n.(*ast.FuncLit); lit {
				return false // a call inside a closure is not on the boot path
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				if _, seen := pos[id.Name]; !seen {
					pos[id.Name] = call.Pos()
				}
			}
			return true
		})
	}
	check, okCheck := pos["checkBootBaseline"]
	wire, okWire := pos["wireBackgroundSubsystems"]
	if !okCheck || !okWire {
		t.Fatalf("composeDaemon calls checkBootBaseline=%v wireBackgroundSubsystems=%v; want both", okCheck, okWire)
	}
	if check >= wire {
		t.Fatalf("checkBootBaseline (%s) runs after wireBackgroundSubsystems (%s)", fset.Position(check), fset.Position(wire))
	}
}

func TestDaemonBootDivergentLooserRunsRestrictive(t *testing.T) {
	store := storetest.NewMemStore()
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, b := bootCheck(t, store, path, tightToml, nil); b.Outcome != runtime.BaselineFirstBootAccepted {
		t.Fatalf("config A boot: %s", b.Detail)
	}
	cfg, b := bootCheck(t, store, path, routingLooser, nil)
	if boolKey(cfg, "external_routing_enabled") {
		t.Fatal("the divergent looser value is in the effective config")
	}
	if b.Detail != "baseline_divergent" {
		t.Fatalf("detail = %q, want baseline_divergent", b.Detail)
	}

	deps := freshDaemonHome(t)
	_, closeA := composeBaselineDaemon(t, deps, tightToml)
	closeA()
	reg, _ := composeBaselineDaemon(t, deps, routingLooser)
	health, state, detail := baselineStatus(t, reg)
	if health != "degraded" || state != "error" || detail != "baseline_divergent" {
		t.Fatalf("status.get health=%q config-baseline=%q/%q, want degraded error/baseline_divergent", health, state, detail)
	}
}

func TestDaemonBootMissingBaselineDefaultConfigAccepted(t *testing.T) {
	store := storetest.NewMemStore()
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, b := bootCheck(t, store, path, "", nil)
	if b.Outcome != runtime.BaselineFirstBootAccepted || b.Outcome.Degraded() {
		t.Fatalf("default config, no record: %s", b.Detail)
	}
	if runtime.ComputeSectionsHash(runtime.EffectiveConfigOf(cfg)) != runtime.ComputeSectionsHash(runtime.MostRestrictiveDefaults()) {
		t.Fatal("the shipped default config was changed")
	}
	if _, err := store.Get(context.Background(), "audit", "config_baseline"); err != nil {
		t.Fatalf("no v1 baseline recorded: %v", err)
	}

	reg, _ := composeBaselineDaemon(t, freshDaemonHome(t), "")
	health, state, detail := baselineStatus(t, reg)
	if health != "ok" || state != "running" || detail != "baseline_first_boot_accepted" {
		t.Fatalf("status.get health=%q config-baseline=%q/%q, want ok running/baseline_first_boot_accepted", health, state, detail)
	}
}

func TestDaemonBootMissingBaselineLooserRunsRestrictive(t *testing.T) {
	store := storetest.NewMemStore()
	cfg, b := bootCheck(t, store, filepath.Join(t.TempDir(), "config.toml"), spillLooserToml, nil)
	if boolKey(cfg, "spill_enabled") {
		t.Fatal("spill_enabled=true ran with no baseline")
	}
	if b.Detail != "baseline_missing" || !b.Outcome.Degraded() {
		t.Fatalf("detail = %q, want baseline_missing (degraded)", b.Detail)
	}
	if _, err := store.Get(context.Background(), "audit", "config_baseline"); err == nil {
		t.Fatal("a looser config with no baseline wrote one")
	}
}

func TestBootBaselineForgedStoreRowStillRestrictive(t *testing.T) {
	store := storetest.NewMemStore()
	path := filepath.Join(t.TempDir(), "config.toml")
	bootCheck(t, store, path, tightToml, nil)
	// An attacker with store access rewrites the row to match the looser file.
	writeConfig(t, path, routingLooser)
	loose, err := runtime.Load(context.Background(), testLoadOpts(path))
	if err != nil {
		t.Fatal(err)
	}
	eff := runtime.EffectiveConfigOf(loose)
	row, _ := json.Marshal(map[string]any{"v": 1, "sections_hash": runtime.ComputeSectionsHash(eff), "sections": []string{"policy", "secrets", "sync", "nodes", "conductor", "elevation"}})
	snap, _ := json.Marshal(eff)
	_ = store.Put(context.Background(), "audit", "config_baseline", row)
	_ = store.Put(context.Background(), "audit", "config_baseline_snapshot", snap)
	cfg, b := bootCheck(t, store, path, routingLooser, func(runtime.BaselineProof) error { return nil })
	if boolKey(cfg, "external_routing_enabled") || b.Detail != "baseline_unsigned_looser" {
		t.Fatalf("forged row: routing=%v detail=%q, want restrictive baseline_unsigned_looser", boolKey(cfg, "external_routing_enabled"), b.Detail)
	}
}

func TestDaemonBootEnvOverrideOfGuardedValueRefused(t *testing.T) {
	f := newAcceptFixture(t)
	writeConfig(t, f.path, routingLooser)
	if err := f.d.Call(context.Background(), runtime.AcceptLooseningMethod, digestParams(routingLooser), nil); err != nil {
		t.Fatalf("accept: %v", err)
	}
	env := map[string]string{"CASCADE_CONDUCTOR__SPILL_ENABLED": "true"}
	opts := testLoadOpts(f.path)
	opts.Getenv = func(k string) string { return env[k] }
	opts.Environ = func() []string { return []string{"CASCADE_CONDUCTOR__SPILL_ENABLED=true"} }
	probe, err := runtime.Load(context.Background(), opts)
	if err != nil || !boolKey(probe, "spill_enabled") {
		t.Fatalf("the env override does not apply (err %v); this test would prove nothing", err)
	}
	// An attacker with store access forges the unsigned sections_hash to the
	// env-overridden values; the signature still covers the file bytes only.
	var rec map[string]any
	raw, _ := f.d.Store.Get(context.Background(), "audit", "config_baseline")
	_ = json.Unmarshal(raw, &rec)
	rec["sections_hash"] = runtime.ComputeSectionsHash(runtime.EffectiveConfigOf(probe))
	forged, _ := json.Marshal(rec)
	_ = f.d.Store.Put(context.Background(), "audit", "config_baseline", forged)

	cfg, b := bootCheckOpts(t, f.d.Store, f.path, routingLooser, productionBaselineVerifier(f.d.DataDir, runtime.NewSystemClock()), opts)
	if boolKey(cfg, "spill_enabled") || boolKey(cfg, "external_routing_enabled") || !b.Outcome.Degraded() {
		t.Fatalf("env override of a guarded value: %s spill=%v", b.Detail, boolKey(cfg, "spill_enabled"))
	}
}
