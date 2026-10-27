//go:build !windows

// Purpose: plugin.update and plugin.registered over the daemon registry:
// an unattested update is challenged and changes nothing, a non-owner is
// denied, one attestation commits exactly once, and the plugin-install
// middleware publishes one plugin.registered per successful add or update
// and none on a refusal.
// Inputs: the elevation fixtures in daemon_unix_elevation_fixture_test.go.
// SPORT: cmd/cascade plugin.update RPC tests.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

func demoUpdate(manifest string) plugins.UpdateParams {
	return plugins.UpdateParams{ID: "demo", ManifestBytes: []byte(manifest)}
}

// pluginDaemon is a composeDaemon registry plus the store and bus its
// wiring was built over, captured by a test registration (the daemon holds
// the database exclusively, so a second connection cannot read it).
type pluginDaemon struct {
	composedElevated
	Store provider.Store
	Bus   *events.Bus
}

func composePluginDaemon(t *testing.T) pluginDaemon {
	t.Helper()
	var d pluginDaemon
	addTestRegistration(t, daemonRegistration{Name: "test-capture-plugin-store", Phase: phaseLate, Order: 9001,
		Wire: func(w *daemonWiring) error { d.Store, d.Bus = w.Store, w.Bus; return nil }})
	d.composedElevated = composeElevated(t, true)
	if d.Store == nil || d.Bus == nil {
		t.Fatal("the capture registration did not run")
	}
	return d
}

// record renders demo's stored record as JSON ("" when absent).
func (d pluginDaemon) record(t *testing.T) string {
	t.Helper()
	rec, ok, err := plugins.LoadMetadata(context.Background(), d.Store, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return ""
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// registered replays the daemon's plugin lifecycle events.
func (d pluginDaemon) registered(t *testing.T) []string {
	t.Helper()
	evs, err := d.Bus.Replay(context.Background(), plugins.PluginEventNamespace, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(evs))
	for _, ev := range evs {
		out = append(out, string(ev.Kind)+" "+string(ev.Payload))
	}
	return out
}

func TestPluginUpdateRPCRequiresAttestation(t *testing.T) {
	d := composePluginDaemon(t)
	if err := d.Call(context.Background(), "plugin.add", pluginAddCall(t), nil); err != nil {
		t.Fatalf("seed plugin.add: %v", err)
	}
	before := d.record(t)
	if before == "" {
		t.Fatal("the seed install wrote no record")
	}
	raw, _ := json.Marshal(demoUpdate(strings.Replace(grantingBuiltinManifest, `"1.0.0"`, `"1.1.0"`, 1)))
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	_, eo := d.Registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: plugins.UpdateRPCMethod, Params: raw})
	if eo == nil || eo.Code != cascade.RPCCodeElevationRequired || nonceOf(eo) == "" {
		t.Fatalf("unattested plugin.update = %+v, want ELEVATION_REQUIRED with a nonce", eo)
	}
	stranger := principal.WithCaller(context.Background(), principal.Caller{Principal: &principal.Principal{}})
	if _, eo := d.Registry.Dispatch(stranger, &rpc.Request{JSONRPC: "2.0", Method: plugins.UpdateRPCMethod, Params: raw}); eo == nil || eo.Code != cascade.RPCCodePermissionDenied {
		t.Fatalf("non-owner plugin.update = %+v, want PERMISSION_DENIED", eo)
	}
	if after := d.record(t); after != before {
		t.Fatalf("a refused plugin.update changed the record:\n before %s\n after  %s", before, after)
	}
}

// TestPluginUpdateRPCCommitsAfterAttestation drives the CLI end to end: the
// update needs elevation, the elevated call reaches the daemon registry,
// whose handler commits exactly once into the daemon's store, and the CLI's
// own store is never written.
func TestPluginUpdateRPCCommitsAfterAttestation(t *testing.T) {
	reg, call := newElevatedTestRegistry(t, filepath.Join(t.TempDir(), "data"))
	if err := call(context.Background(), "plugin.add", pluginAddCall(t), nil); err != nil {
		t.Fatalf("seed plugin.add: %v", err)
	}
	runs := countHandlerRuns(reg, plugins.UpdateRPCMethod)
	swapUpdateElevatedCall(t, func(ctx context.Context, _ pluginDeps, _ io.Writer, req plugins.UpdateParams, out any) error {
		return call(ctx, plugins.UpdateRPCMethod, req, out)
	})
	deps := testPluginDeps(t)
	seedDemoV1(t, deps)
	before := recordJSON(t, deps, "demo")
	stdout, _, err := runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: false},
		"update", "demo", "--from", writeManifest(t, expandingDemoManifest))
	if err != nil {
		t.Fatalf("attested update: %v", err)
	}
	if n := runs.Load(); n != 1 {
		t.Fatalf("plugin.update handler ran %d times, want exactly 1", n)
	}
	if !strings.Contains(stdout, "updated demo to v1.1.0") {
		t.Fatalf("stdout = %q, want the daemon's commit message", stdout)
	}
	if after := recordJSON(t, deps, "demo"); after != before {
		t.Fatalf("the CLI wrote its own store: %s -> %s", before, after)
	}
}

func TestPluginAddEmitsPluginRegisteredEvent(t *testing.T) {
	d := composePluginDaemon(t)
	want := `plugin.registered {"id":"demo","version":"1.0.0","runtime":"builtin"}`
	if err := d.Call(context.Background(), "plugin.add", pluginAddCall(t), nil); err != nil {
		t.Fatalf("attested plugin.add: %v", err)
	}
	if got := d.registered(t); len(got) != 1 || got[0] != want {
		t.Fatalf("events after one commit = %q, want exactly [%s]", got, want)
	}
	// Refusals: an attested add whose handler refuses (the manifest names
	// another id) while demo's record exists, and an unattested add.
	mismatch := pluginAddParams{ID: "demo", ManifestBytes: []byte(strings.Replace(grantingBuiltinManifest, `"demo"`, `"other"`, 1))}
	if err := d.Call(context.Background(), "plugin.add", mismatch, nil); err == nil {
		t.Fatal("an id-mismatched plugin.add succeeded")
	}
	raw, _ := json.Marshal(pluginAddCall(t))
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if _, eo := d.Registry.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: "plugin.add", Params: raw}); eo == nil {
		t.Fatal("an unattested plugin.add ran")
	}
	if got := d.registered(t); len(got) != 1 || got[0] != want {
		t.Fatalf("events after two refusals = %q, want still exactly [%s]", got, want)
	}
}

func TestPluginUpdateEmitsPluginRegisteredEvent(t *testing.T) {
	d := composePluginDaemon(t)
	ctx := context.Background()
	if err := d.Call(ctx, "plugin.add", pluginAddCall(t), nil); err != nil {
		t.Fatalf("seed plugin.add: %v", err)
	}
	if err := d.Call(ctx, plugins.UpdateRPCMethod, demoUpdate(expandingDemoManifest), nil); err != nil {
		t.Fatalf("attested plugin.update: %v", err)
	}
	if got := d.registered(t); len(got) != 2 || !strings.Contains(got[1], `"version":"1.1.0"`) {
		t.Fatalf("events after the update commit = %q, want the add's and the update's", got)
	}
	process := strings.Replace(expandingDemoManifest, `"builtin"`, `"process"`, 1)
	if err := d.Call(ctx, plugins.UpdateRPCMethod, demoUpdate(process), nil); err == nil {
		t.Fatal("a process-tier update with no handshaker committed")
	}
	got := d.registered(t)
	want := []string{
		`plugin.registered {"id":"demo","version":"1.0.0","runtime":"builtin"}`,
		`plugin.registered {"id":"demo","version":"1.1.0","runtime":"builtin"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events = %q, want %q", got, want)
	}
	for _, ev := range got {
		if strings.Contains(ev, "net.http") || strings.Contains(ev, "abc123") || strings.Contains(ev, "manifest") {
			t.Fatalf("event %s carries a grant or param value", ev)
		}
	}
}

// TestPluginUpdateDaemonUpNeverOpensTheDaemonsStore: with the daemon up, the
// CLI shares the daemon's database path, which the daemon holds
// exclusively. Every update (grant-expanding or not) must go to the daemon's
// plugin.update without the CLI opening that database.
func TestPluginUpdateDaemonUpNeverOpensTheDaemonsStore(t *testing.T) {
	d := composePluginDaemon(t)
	ctx := context.Background()
	if err := d.Call(ctx, "plugin.add", pluginAddCall(t), nil); err != nil {
		t.Fatalf("seed plugin.add: %v", err)
	}
	dialed := 0
	swapUpdateElevatedCall(t, func(ctx context.Context, _ pluginDeps, _ io.Writer, req plugins.UpdateParams, out any) error {
		dialed++
		return d.Call(ctx, plugins.UpdateRPCMethod, req, out)
	})
	deps := testPluginDeps(t)
	deps.Paths = d.Deps.Paths // the one database the daemon holds
	daemonUp := runtime.DaemonlessState{Embedded: false}
	steps := []struct{ version, manifest string }{
		{"1.1.0", expandingDemoManifest},                                           // grant-expanding
		{"1.2.0", strings.Replace(expandingDemoManifest, `"1.1.0"`, `"1.2.0"`, 1)}, // same grants, version bump only
	}
	for i, step := range steps {
		stdout, _, err := runPluginCLIState(t, deps, daemonUp, "update", "demo", "--from", writeManifest(t, step.manifest))
		if err != nil {
			t.Fatalf("daemon-up update to %s: %v", step.version, err)
		}
		if dialed != i+1 || !strings.Contains(stdout, "updated demo to v"+step.version) {
			t.Fatalf("update to %s: dialed %d, stdout %q", step.version, dialed, stdout)
		}
		if rec := d.record(t); !strings.Contains(rec, `"`+step.version+`"`) {
			t.Fatalf("daemon record after the update to %s = %s", step.version, rec)
		}
	}
}

// TestPluginAddRPCRemoteRuntimeRefused: an attested plugin.add of a
// remote-runtime manifest over the composeDaemon registry is refused with
// the DEF-P2 identifier, writes no record and publishes nothing.
func TestPluginAddRPCRemoteRuntimeRefused(t *testing.T) {
	d := composePluginDaemon(t)
	remote := grantingBuiltinManifest + "\n[remote]\nhost = \"127.0.0.1\"\nport = 1\n"
	remote = strings.Replace(remote, `runtime = "builtin"`, `runtime = "remote"`, 1)
	err := d.Call(context.Background(), "plugin.add", pluginAddParams{ID: "demo", ManifestBytes: []byte(remote)}, nil)
	var eo *rpc.ErrorObject
	if !errors.As(err, &eo) || eo.Code != cascade.RPCCodeUnsupported || !strings.Contains(eo.Message, "DEF-P2-remote-plugin-runtime") {
		t.Fatalf("attested remote plugin.add = %v, want UNSUPPORTED naming DEF-P2-remote-plugin-runtime", err)
	}
	if rec := d.record(t); rec != "" {
		t.Fatalf("a refused remote add wrote %s", rec)
	}
	if got := d.registered(t); len(got) != 0 {
		t.Fatalf("events = %q, want none", got)
	}
}
