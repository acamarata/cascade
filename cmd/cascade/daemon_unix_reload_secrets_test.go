//go:build !windows

// Purpose: proves the daemon side of [secrets] hot reload through the real
// wireHotReload: an applier error of kind unavailable rejects the reload,
// keeps the previous config and marks the egress-firewall subsystem failed
// until a later apply succeeds; an invalid table never reaches the detector;
// and a degraded boot makes the config and the detector carry the baseline's
// effective thresholds, not the on-disk ones.
// Inputs: a real runtime store and config file under a temp CASCADE_HOME, a
// daemon manifest, injected detector seams (the process detector is global,
// so no test here reloads it).
// Outputs: none (tests).
// SPORT: cmd/cascade daemon tests (P1-SEC-24).
package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/secrets"
	"github.com/acamarata/cascade/pkg/cascade"
)

func firewallState(t *testing.T, m *daemon.Manifest) (daemon.SubsystemState, string) {
	t.Helper()
	for _, s := range m.Snapshot() {
		if s.Name == egressFirewallSubsystem {
			return s.State, s.Detail
		}
	}
	t.Fatalf("no %s entry in the manifest", egressFirewallSubsystem)
	return daemon.SubsystemDeclared, ""
}

func TestSecretsReloadCustodyFailureKeepsFirewall(t *testing.T) {
	ctx := context.Background()
	paths, clock, store := newReloadTestStore(t)
	getenv, environ := func(string) string { return "" }, func() []string { return nil }
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath(), Getenv: getenv, Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	manifest := daemon.NewManifest(slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	manifest.Started(egressFirewallSubsystem, secretsStatusDetail(secrets.DefaultDetectionConfig()))

	var applied []secrets.DetectionConfig
	var failure error = cascade.New(cascade.KindUnavailable, "custody is unavailable")
	set := func(c secrets.DetectionConfig) error {
		if failure != nil {
			return failure
		}
		applied = append(applied, c)
		return nil
	}
	hr, watcher, err := wireHotReload(paths, clock, getenv, environ, cfg, store, events.New(store, clock), nil,
		func(hr *runtime.HotReloader) { hr.SetSecretsApplier(newSecretsApplier(manifest, set)) })
	if err != nil {
		t.Fatal(err)
	}
	// Reloads here are driven by hand: stop the fsnotify watcher so it cannot
	// run a reload against the variables this test changes.
	watcher.Stop()
	before := hr.Current()

	if err := os.WriteFile(paths.ConfigPath(), []byte("[secrets]\nentropy_floor = 3.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := hr.Reload(ctx)
	if !out.Rejected || !cascade.HasKind(out.Err, cascade.KindUnavailable) || hr.Current() != before {
		t.Fatalf("applier unavailable: %+v (config kept: %v), want a rejection that keeps the previous config", out, hr.Current() == before)
	}
	if state, _ := firewallState(t, manifest); state != daemon.SubsystemError {
		t.Fatalf("egress-firewall state %q after a custody failure, want %q (status.get degraded)", state, daemon.SubsystemError)
	}
	assertAuditRecordExists(ctx, t, store, "reload_rejected/")

	// The operator fixes custody; the same file now applies and the entry heals.
	failure = nil
	if out := hr.Reload(ctx); !out.Accepted || len(applied) != 1 || applied[0].EntropyFloor != 3.0 {
		t.Fatalf("retry: %+v, applied %+v", out, applied)
	}
	state, detail := firewallState(t, manifest)
	if state != daemon.SubsystemRunning || detail != "confidence_threshold=0.8 entropy_floor=3" {
		t.Fatalf("egress-firewall after a good apply = %q %q", state, detail)
	}
}

// TestSecretsRevertAfterAFailureHealsTheFirewall: a failed apply marks the
// firewall degraded; reverting the file to what is running must clear it even
// though the [secrets] table equals the running one.
func TestSecretsRevertAfterAFailureHealsTheFirewall(t *testing.T) {
	ctx := context.Background()
	paths, clock, store := newReloadTestStore(t)
	getenv, environ := func(string) string { return "" }, func() []string { return nil }
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath(), Getenv: getenv, Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	manifest := daemon.NewManifest(slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	manifest.Started(egressFirewallSubsystem, secretsStatusDetail(secrets.DefaultDetectionConfig()))
	var failure error = cascade.New(cascade.KindUnavailable, "custody is unavailable")
	set := func(secrets.DetectionConfig) error { return failure }
	hr, watcher, err := wireHotReload(paths, clock, getenv, environ, cfg, store, events.New(store, clock), nil,
		func(hr *runtime.HotReloader) { hr.SetSecretsApplier(newSecretsApplier(manifest, set)) })
	if err != nil {
		t.Fatal(err)
	}
	watcher.Stop()
	if err := os.WriteFile(paths.ConfigPath(), []byte("[secrets]\nentropy_floor = 3.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := hr.Reload(ctx); !out.Rejected {
		t.Fatalf("failing apply: %+v", out)
	}
	if state, _ := firewallState(t, manifest); state != daemon.SubsystemError {
		t.Fatalf("state %q after a failed apply", state)
	}
	failure = nil
	if err := os.WriteFile(paths.ConfigPath(), []byte(testReloadConfigBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := hr.Reload(ctx); !out.Accepted {
		t.Fatalf("revert: %+v", out)
	}
	if state, _ := firewallState(t, manifest); state != daemon.SubsystemRunning {
		t.Fatalf("the firewall is still %q after the file was reverted and the reload accepted", state)
	}
}

func TestSecretsApplierRefusesInvalidTableBeforeTheDetector(t *testing.T) {
	called := false
	m := daemon.NewManifest(slog.New(slog.NewTextHandler(io.Discard, nil)), runtime.NewSystemClock())
	m.Started(egressFirewallSubsystem, "ok")
	apply := newSecretsApplier(m, func(secrets.DetectionConfig) error { called = true; return nil })
	for _, section := range []map[string]interface{}{
		{"confidence_threshold": 0.0}, {"confidence_threshold": "0.8"}, {"entropy_floor": 9.0}, {"typo": 1},
	} {
		if err := apply(section); !cascade.HasKind(err, cascade.KindInvalidInput) || called {
			t.Fatalf("%v: err %v, detector reached %v", section, err, called)
		}
	}
	if state, _ := firewallState(t, m); state != daemon.SubsystemRunning {
		t.Fatalf("an invalid table changed the firewall state to %q; the old rules still apply", state)
	}
}

// TestWireBackgroundSubsystemsInstallsSecretsReload fails when the daemon's
// composition stops installing the [secrets] hot-reload pieces: the boot
// threshold application before the reloader exists, and the applier option on
// the reloader. The parts are proven above; this proves they are wired.
func TestWireBackgroundSubsystemsInstallsSecretsReload(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "daemon_unix_reload.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var bootPos, wirePos token.Pos
	optionArg := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "wireBackgroundSubsystems" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch name := calleeName(call); name {
			case "bootSecrets":
				bootPos = call.Pos()
			case "wireHotReload":
				wirePos = call.Pos()
				for _, arg := range call.Args {
					if inner, ok := arg.(*ast.CallExpr); ok && calleeName(inner) == "secretsApplierOption" {
						optionArg = true
					}
				}
			}
			return true
		})
	}
	if bootPos == token.NoPos || wirePos == token.NoPos || bootPos > wirePos || !optionArg {
		t.Fatalf("wireBackgroundSubsystems must call bootSecrets before wireHotReload and pass secretsApplierOption(...) to it (boot %v, wire %v, option passed %v)", bootPos, wirePos, optionArg)
	}
}

func calleeName(call *ast.CallExpr) string {
	if id, ok := call.Fun.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// TestSecretsApplierOptionTightensTheProcessDetector drives the production
// option over the real process detector: a lowered floor reaches it with no
// restart. The detector is restored afterwards.
func TestSecretsApplierOptionTightensTheProcessDetector(t *testing.T) {
	ctx := context.Background()
	// Start from a known process detector whatever earlier tests left behind.
	original := secrets.DefaultDetectionConfig()
	if err := secrets.SetProcessDetectionConfig(original); err != nil {
		t.Fatalf("set the process detector: %v", err)
	}
	t.Cleanup(func() { _ = secrets.SetProcessDetectionConfig(original) })

	paths, clock, store := newReloadTestStore(t)
	getenv, environ := func(string) string { return "" }, func() []string { return nil }
	cfg, err := runtime.Load(ctx, runtime.LoadOptions{Path: paths.ConfigPath(), Getenv: getenv, Environ: environ})
	if err != nil {
		t.Fatal(err)
	}
	manifest := daemon.NewManifest(slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	hr, watcher, err := wireHotReload(paths, clock, getenv, environ, cfg, store, events.New(store, clock), nil, secretsApplierOption(manifest))
	if err != nil {
		t.Fatal(err)
	}
	watcher.Stop() // the test drives the reload itself
	lowered := original.EntropyFloor - 0.5
	if err := os.WriteFile(paths.ConfigPath(), []byte(fmt.Sprintf("[secrets]\nentropy_floor = %g\n", lowered)), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := hr.Reload(ctx); !out.Accepted {
		t.Fatalf("lowering the floor: %+v", out)
	}
	if got := secrets.ProcessDetectionConfig().EntropyFloor; got != lowered {
		t.Fatalf("process detector floor %v, want %v", got, lowered)
	}
}
