//go:build !windows

// Purpose: the plugin.add and trust-record tests over the daemon registry:
// an unattested install is challenged and changes nothing, one attestation
// installs exactly once, and an unenrolled, swapped or rewritten trust record
// leaves every elevated verb refused.
// Inputs: the fixtures in daemon_unix_elevation_test.go.
// SPORT: cmd/cascade elevation wiring tests (P1-SEC-03).
package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/acamarata/cascade/internal/elevation"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func pluginAddCall(t *testing.T) pluginAddParams {
	t.Helper()
	return pluginAddParams{ID: "demo", Checksum: "abc123", ManifestBytes: []byte(grantingBuiltinManifest)}
}

// installedPlugins renders the daemon's plugin records.
func installedPlugins(t *testing.T, store provider.Store) string {
	t.Helper()
	recs, err := plugins.ListMetadata(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(recs)
	return string(b)
}

func TestPluginAddRPCRefusesWithoutAttestation(t *testing.T) {
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
	if !d.Registry.Registered("plugin.add") {
		t.Fatal("plugin.add is not registered on the daemon registry")
	}
	before := installedPlugins(t, d.Store)
	raw, _ := json.Marshal(pluginAddCall(t))
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	_, eo := d.Registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: "plugin.add", Params: raw})
	if eo == nil || eo.Code != cascade.RPCCodeElevationRequired || nonceOf(eo) == "" {
		t.Fatalf("unattested plugin.add = %+v, want ELEVATION_REQUIRED with a nonce", eo)
	}
	if after := installedPlugins(t, d.Store); after != before {
		t.Fatalf("a refused plugin.add changed the plugin store:\n before %s\n after  %s", before, after)
	}
}

// countHandlerRuns mounts an innermost middleware that counts how often the
// method's own handler is invoked. It is added after the elevation
// middleware, so it sits below the gate and counts only real handler runs,
// not challenges or refusals.
func countHandlerRuns(reg *rpc.Registry, method string) *atomic.Int32 {
	runs := new(atomic.Int32)
	reg.Use(func(m string, next rpc.HandlerFunc) rpc.HandlerFunc {
		if m != method {
			return next
		}
		return func(ctx context.Context, params json.RawMessage) (any, error) {
			runs.Add(1)
			return next(ctx, params)
		}
	})
	return runs
}

func TestPluginAddRPCWithAttestationProceeds(t *testing.T) {
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
	runs := countHandlerRuns(d.Registry, "plugin.add")
	var result pluginAddRPCResult
	if err := d.Call(context.Background(), "plugin.add", pluginAddCall(t), &result); err != nil {
		t.Fatalf("attested plugin.add: %v", err)
	}
	// A metadata record is overwritten by a repeat install, so the record
	// count cannot see a second run; the handler run count can.
	if n := runs.Load(); n != 1 {
		t.Fatalf("plugin.add handler ran %d times for one attested call, want exactly 1", n)
	}
	if strings.Contains(result.Message, "already") {
		t.Errorf("result = %+v, want a fresh install", result)
	}
	if !strings.Contains(result.Message, "installed") {
		t.Errorf("result = %+v", result)
	}
	recs, err := plugins.ListMetadata(context.Background(), d.Store)
	if err != nil || len(recs) != 1 || recs[0].Name != "demo" {
		t.Fatalf("plugin records = %v (err %v), want exactly demo", recs, err)
	}
}

func TestPluginAddRPCNonceSingleUse(t *testing.T) {
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	raw, _ := json.Marshal(pluginAddCall(t))
	_, eo := d.Registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: "plugin.add", Params: raw})
	nonce := nonceOf(eo)
	att, err := signAttestation(d.KS, runtime.NewSystemClock(), rpc.ParamsHash(raw), nonce)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := json.Marshal(map[string]any{"_attestation": att, "_args": json.RawMessage(raw)})
	req := &rpc.Request{JSONRPC: "2.0", Method: "plugin.add", Params: env}
	runs := countHandlerRuns(d.Registry, "plugin.add")
	if _, eo := d.Registry.Dispatch(owner, req); eo != nil {
		t.Fatalf("first use of the nonce: %+v", eo)
	}
	defer func() {
		if n := runs.Load(); n != 1 {
			t.Errorf("plugin.add handler ran %d times for one attestation, a replay and a cross-request reuse, want 1", n)
		}
	}()
	if _, eo = d.Registry.Dispatch(owner, req); eo == nil {
		t.Fatal("the same attested envelope ran twice")
	}
	// The same attestation carried onto a different request is refused too.
	other := pluginAddParams{ID: "other", ManifestBytes: []byte(strings.Replace(grantingBuiltinManifest, `"demo"`, `"other"`, 1))}
	otherRaw, _ := json.Marshal(other)
	crossEnv, _ := json.Marshal(map[string]any{"_attestation": att, "_args": json.RawMessage(otherRaw)})
	if _, eo := d.Registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: "plugin.add", Params: crossEnv}); eo == nil {
		t.Fatal("an attestation for one request authorised another")
	}
	if recs, _ := plugins.ListMetadata(context.Background(), d.Store); len(recs) != 1 {
		t.Fatalf("%d plugins installed after one attested add and two refusals, want 1", len(recs))
	}
}

func TestDaemonRegistryUnenrolledRefusesElevated(t *testing.T) {
	d := composeElevated(t, false)
	assertBindingRefusals(t, d.Registry, elevation.ErrHelperNotEnrolled())
}

// assertBindingRefusals dispatches every elevated verb the registry serves,
// plus an uninstall.purge_data canary, unattested, and requires the exact wire
// refusal the trust binding owes (want's code and message), never a
// challenge. plugin.add carries a well-formed install request, so a handler
// that ran would succeed; any other error (a handler's own decode error) fails
// the test, so it cannot pass with the elevation middleware gone.
func assertBindingRefusals(t *testing.T, reg *rpc.Registry, want error) {
	t.Helper()
	canaryRuns := 0
	reg.Register("uninstall.purge_data", func(context.Context, json.RawMessage) (any, error) { canaryRuns++; return "purged", nil })
	wantEO := cascade.NewRPCError(want)
	addRaw, _ := json.Marshal(pluginAddCall(t))
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	checked := 0
	for _, name := range rpc.ElevatedVerbNames() {
		if !reg.Registered(name) {
			continue
		}
		checked++
		params := json.RawMessage(`[]`) // not a JSON object, so every conditional rule elevates it
		if name == "plugin.add" {
			params = addRaw
		}
		_, eo := reg.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: name, Params: params})
		if eo == nil || eo.Code != wantEO.Code || eo.Message != wantEO.Message {
			t.Errorf("%s unattested: %+v, want the binding refusal %d %q", name, eo, wantEO.Code, wantEO.Message)
		}
	}
	if canaryRuns != 0 {
		t.Errorf("the uninstall.purge_data canary ran %d times unattested", canaryRuns)
	}
	if checked < 2 || !reg.Registered("plugin.add") {
		t.Fatalf("%d elevated verbs registered (plugin.add: %v); the loop proved nothing", checked, reg.Registered("plugin.add"))
	}
}

func TestDaemonRegistryNonElevatedPassThrough(t *testing.T) {
	d := composeElevated(t, true)
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if _, eo := d.Registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: "status.get"}); eo != nil {
		t.Fatalf("status.get: %+v", eo)
	}
	seen := 0
	for _, name := range d.Registry.Methods() {
		if !strings.HasPrefix(name, "memory.") && !strings.HasPrefix(name, "recall.") {
			continue
		}
		seen++
		_, eo := d.Registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: name, Params: json.RawMessage(`{}`)})
		if eo != nil && (eo.Code == cascade.RPCCodeElevationRequired || (eo.Data != nil && strings.Contains(eo.Message, "elevat"))) {
			t.Errorf("%s was gated by elevation: %+v", name, eo)
		}
	}
	if seen == 0 {
		t.Fatal("no memory.* or recall.* method registered; the loop proved nothing")
	}
}

func TestAttackerTrustRecordRefused(t *testing.T) {
	// A record naming a key that is not the custody key: every elevated verb
	// refuses, and none offers a challenge nobody could satisfy.
	ks := newSigningKeystore(t)
	useElevationCustody(t, ks)
	deps := newRunTestDeps(t, nonexistentExecutable(t))
	_, _, attackerPub := foreignKey(t)
	enrollKey(t, deps.Paths.DataDir(), attackerPub)
	var reg *rpc.Registry
	_, cleanups, err := composeDaemon(context.Background(), deps, func(r *rpc.Registry, _ *rpc.SSEMux) { reg = r })
	t.Cleanup(func() { runCleanupsLIFO(cleanups) })
	if err != nil {
		t.Fatal(err)
	}
	assertBindingRefusals(t, reg, elevation.ErrAlreadyEnrolled())
}

// A record rewritten AFTER the daemon started changes nothing: trust is bound
// once, so an attestation signed by the rewritten record's key is refused.
func TestRewrittenTrustRecordAfterStartIsIgnored(t *testing.T) {
	d := newElevatedDaemon(t, filepath.Join(t.TempDir(), "data"), true)
	forged := newSigningKeystore(t)
	forgedPub, _ := forged.PubKeyB64()
	rewriteTrust(t, filepath.Join(d.DataDir, "elevation", "trust.json"), forgedPub)
	err := elevatedCaller(d.Registry, forged, runtime.NewSystemClock())(context.Background(), "plugin.add", pluginAddCall(t), nil)
	if err == nil {
		t.Fatal("an attestation signed by the rewritten record's key was accepted")
	}
	if recs, _ := plugins.ListMetadata(context.Background(), d.Store); len(recs) != 0 {
		t.Fatalf("a forged attestation installed a plugin: %v", recs)
	}
}
