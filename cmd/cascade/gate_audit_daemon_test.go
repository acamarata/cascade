//go:build !windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// daemonSink is the production sink pointed at an in-process daemon.
func daemonSink(d pluginDaemon) *autoGateSink {
	return &autoGateSink{dial: func() (gateCall, error) { return d.Call, nil },
		openDirect: func(context.Context) (gateSink, error) { return nil, errors.New("fake: direct path used") }}
}

// TestLocalVerbsWorkWithTheDaemonUp: with a daemon holding the store, vault
// get, vault grant and a node verb pass with a valid proof and their rows
// land in the daemon's audit log.
func TestLocalVerbsWorkWithTheDaemonUp(t *testing.T) {
	d := composePluginDaemon(t)
	f := newGateFixture(t, nil)
	f.gate.sink = daemonSink(d)
	deps, spy := seededVault(t, f.gate)
	if out, _, err := runVault(t, deps, "", "get", "FAKE_NAME"); err != nil || out != fakeSecretValue || spy.gets != 1 {
		t.Fatalf("daemon-up vault get = (%q, %v) gets=%d", out, err, spy.gets)
	}
	deps.Paths = grantPaths{dir: t.TempDir()}
	if _, _, err := runVault(t, deps, "", "grant", "provider.fake.key"); err != nil {
		t.Fatalf("daemon-up vault grant = %v", err)
	}
	if err := requireElevation(t.Context(), f.gate, "node.revoke"); err != nil {
		t.Fatalf("daemon-up node.revoke = %v", err)
	}
	rows, _ := storedAuditRecords(t, d.Store)
	for _, want := range []string{`"vault.access"`, `"elevation.grant"`, `"node.revoke"`, `"elevation.local"`} {
		if !strings.Contains(rows, want) {
			t.Fatalf("daemon audit log lacks %s", want)
		}
	}
}

// TestDaemonLostMidCallRefuses: a daemon that answered once and then fails
// refuses the call; it never falls back to a direct write.
func TestDaemonLostMidCallRefuses(t *testing.T) {
	d := composePluginDaemon(t)
	f := newGateFixture(t, nil)
	calls := 0
	f.gate.sink = &autoGateSink{dial: func() (gateCall, error) {
		return func(ctx context.Context, m string, p, out any) error {
			if calls++; calls > 1 {
				return cascade.New(cascade.KindUnavailable, "fake: daemon gone")
			}
			return d.Call(ctx, m, p, out)
		}, nil
	}, openDirect: func(context.Context) (gateSink, error) { return nil, errors.New("fake: direct path used") }}
	err := f.gate.Authorize(t.Context(), "vault.get")
	if err == nil || !strings.Contains(err.Error(), "daemon gone") || strings.Contains(err.Error(), "direct path") {
		t.Fatalf("daemon lost mid-call = %v", err)
	}
	deps, spy := seededVault(t, f.gate)
	calls = 1
	if _, _, err := runVault(t, deps, "", "get", "FAKE_NAME"); err == nil || spy.gets != 0 {
		t.Fatalf("vault get with the daemon gone = %v, gets %d", err, spy.gets)
	}
}

// TestGateAppendIsOwnerOnly: a principal-bearing caller and a caller-less
// call of audit.gate_append are refused; the owner is acknowledged.
func TestGateAppendIsOwnerOnly(t *testing.T) {
	d := composePluginDaemon(t)
	row, _ := json.Marshal(gateRow{Kind: "elevation.attempt", Verb: "vault.get", ParamsHash: fakeHash})
	dispatch := func(ctx context.Context) (any, *rpc.ErrorObject) {
		return d.Registry.Dispatch(ctx, &rpc.Request{JSONRPC: "2.0", Method: gateAppendMethod, Params: row})
	}
	if _, eo := dispatch(principal.WithCaller(t.Context(), principal.Caller{Principal: &principal.Principal{}})); eo == nil {
		t.Fatal("a principal-bearing caller appended a gate row")
	}
	if _, eo := dispatch(t.Context()); eo == nil {
		t.Fatal("a caller-less call appended a gate row")
	}
	if res, eo := dispatch(principal.WithCaller(t.Context(), principal.Caller{OwnerCLI: true})); eo != nil || res.(gateAppendAck).Seq == 0 {
		t.Fatalf("owner append = %v %v", res, eo)
	}
}

// TestPluginVerbsGoThroughTheDaemon: with a daemon holding the store, add
// and perms are the daemon's attested calls and the CLI never opens the
// store; no-input refuses with attempt then deny.
func TestPluginVerbsGoThroughTheDaemon(t *testing.T) {
	d := composePluginDaemon(t)
	prevAdd, prevPerms := pluginAddElevatedCall, pluginPermsElevatedCall
	t.Cleanup(func() { pluginAddElevatedCall, pluginPermsElevatedCall = prevAdd, prevPerms })
	pluginAddElevatedCall = func(ctx context.Context, _ pluginDeps, _ io.Writer, p pluginAddParams, out any) error {
		return d.Call(ctx, "plugin.add", p, out)
	}
	pluginPermsElevatedCall = func(ctx context.Context, _ pluginDeps, _ io.Writer, m string, p plugins.PermsParams, out any) error {
		return d.Call(ctx, m, p, out)
	}
	deps, f := pluginGateFixture(t)
	deps.Paths = d.Deps.Paths
	up := runtime.DaemonlessState{Embedded: false}
	if _, _, err := runPluginCLIState(t, deps, up, "add", writeManifest(t, grantingBuiltinManifest)); err != nil {
		t.Fatalf("daemon-up add = %v", err)
	}
	for _, verb := range []string{"grant", "revoke"} {
		if _, _, err := runPluginCLIState(t, deps, up, "perms", verb, "demo", "fake.capability"); err != nil {
			t.Fatalf("daemon-up perms %s = %v", verb, err)
		}
		if has := strings.Contains(installedPlugins(t, d.Store), "fake.capability"); has != (verb == "grant") {
			t.Fatalf("after perms %s the daemon record has the capability = %v", verb, has)
		}
	}
	deps.Getenv = func(k string) string { return map[string]string{"CASCADE_NO_INPUT": "1"}[k] }
	_, _, err := runPluginCLIState(t, deps, up, "perms", "grant", "demo", "fake.capability")
	assertDenied(t, "daemon-up perms with no input", f, err, "CASCADE_NO_INPUT", "no_input")
	if _, err := plugins.PermsRPCHandler(d.Store, true)(t.Context(), json.RawMessage(`{"name":"demo","capability":"x"}`)); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("perms.grant without a witness = %v", err)
	}
}
