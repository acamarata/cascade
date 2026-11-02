//go:build !windows && !integration

// Purpose: acceptance tests for `cascade plugin perms grant|revoke` that
// assert the CLI never writes the store itself and that both verbs are
// marked elevated in the daemon's verb table.
// Inputs: an in-process elevated registry built by newElevatedTestRegistry,
// a temp CASCADE_HOME, and signed dispatch through the real middleware.
// Outputs: TestPermsCLINeverWritesStore, TestPermsVerbsAreElevated and
// TestDaemonVerbsAllMarked stay green.
// Constraints: no net import; HOME/CASCADE_HOME under t.TempDir(); custody
// through the injected test selector only.
// SPORT: cli/plugin-lifecycle/ADD (P1-PLG-11).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/plugins"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
	"github.com/acamarata/cascade/pkg/provider"
)

// TestPermsCLINeverWritesStore asserts that `cascade plugin perms grant`
// with the daemon down refuses naming the daemon requirement and leaves
// the store byte-identical. The CLI must never open the plugin store
// directly; the change is the daemon's attested RPC.
func TestPermsCLINeverWritesStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CASCADE_HOME", home)
	t.Setenv("CASCADE_NO_INPUT", "1")
	deps := testPluginDeps(t)
	store, closeStore, err := openPluginStore(t.Context(), deps.Paths, deps.Clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugins.SaveMetadata(t.Context(), store, plugins.PluginMetadata{Name: "demo", Grants: []string{"fs.read"}}); err != nil {
		t.Fatal(err)
	}
	closeStore()
	storePath := filepath.Join(deps.Paths.DataDir(), "cascade.db")
	before, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	deps.Getenv = func(k string) string {
		if k == "CASCADE_NO_INPUT" {
			return "1"
		}
		return ""
	}
	// Daemon is NOT reachable: Embedded=true forces the daemonless path so
	// the CLI must refuse before it could dial anything.
	_, _, err = runPluginCLIState(t, deps, runtime.DaemonlessState{Embedded: true}, "perms", "grant", "demo", "net.http:example.org")
	if err == nil {
		t.Fatal("plugin perms grant without a daemon succeeded, want daemon_required refusal")
	}
	want := "daemon"
	if got := err.Error(); !strings.Contains(strings.ToLower(got), want) {
		t.Fatalf("error = %q, want it to name %q", got, want)
	}

	after, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read store after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("daemonless permissions command changed the store bytes")
	}
}

// TestPermsVerbsAreElevated asserts that perms.grant and perms.revoke are
// marked elevated on the base verb table (P1-SEC-03 owns the rows; this
// ticket only consumes them).
func TestPermsVerbsAreElevated(t *testing.T) {
	for _, m := range []string{"perms.grant", "perms.revoke"} {
		if !rpc.IsElevatedVerb(m) {
			t.Errorf("IsElevatedVerb(%q) = false, want true", m)
		}
	}
}

// TestPermsGrantRPCRequiresAttestation asserts that an unattested
// perms.grant over the composeDaemon registry refuses with
// ELEVATION_REQUIRED and leaves the store unchanged.
func TestPermsGrantRPCRequiresAttestation(t *testing.T) {
	ctx := context.Background()
	var store provider.Store
	addTestRegistration(t, daemonRegistration{Name: "test-capture-perms-store", Phase: phaseLate, Order: 9003,
		Wire: func(w *daemonWiring) error { store = w.Store; return nil }})
	reg, _ := newElevatedTestRegistry(t, filepath.Join(t.TempDir(), "data"))
	if store == nil {
		t.Fatal("the capture registration did not run")
	}
	// Seed a plugin record so the handler has something to mutate.
	seed := plugins.PluginMetadata{Name: "demo", RuntimeMode: plugin.RuntimeProcess, Grants: []string{"net.http:example.org"}}
	if err := plugins.SaveMetadata(ctx, store, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}
	before, _, err := plugins.LoadMetadata(ctx, store, "demo")
	if err != nil {
		t.Fatalf("load before: %v", err)
	}
	params := plugins.PermsParams{ID: "demo", Capability: "fs.read:/tmp"}
	raw, _ := json.Marshal(params)
	owner := principal.WithCaller(ctx, principal.Caller{OwnerCLI: true})
	_, eo := reg.Dispatch(owner, &rpc.Request{JSONRPC: "2.0", Method: "perms.grant", Params: raw})
	if eo == nil || eo.Code != cascade.RPCCodeElevationRequired {
		t.Fatalf("unsigned perms.grant = %+v, want ELEVATION_REQUIRED", eo)
	}
	after, ok, err := plugins.LoadMetadata(ctx, store, "demo")
	if err != nil || !ok {
		t.Fatalf("load after: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("store changed without attestation: before=%+v after=%+v", before, after)
	}
}

// TestPermsGrantRPCWidensAuthorityOnlyWithWitness asserts that an attested
// perms.grant widens Grants and TrustGrants exactly once and writes an
// intent+confirm effect pair.
func TestPermsGrantRPCWidensAuthorityOnlyWithWitness(t *testing.T) {
	ctx := context.Background()
	var store provider.Store
	var log *audit.Log
	addTestRegistration(t, daemonRegistration{Name: "test-capture-perms-grant-store", Phase: phaseLate, Order: 9004,
		Wire: func(w *daemonWiring) error {
			store = w.Store
			var err error
			log, err = openAuditLog(w.Store, w.Clock, w.Bus, w.Paths)
			return err
		}})
	fakeGithubExecutable(t)
	_, call := newElevatedTestRegistry(t, filepath.Join(t.TempDir(), "data"))
	if store == nil {
		t.Fatal("the capture registration did not run")
	}
	seed := installPermsTestPlugin(t, call, store)
	params := plugins.PermsParams{ID: seed.Name, Capability: "net.http:example.org"}
	for range 2 {
		if err := call(ctx, "perms.grant", params, nil); err != nil {
			t.Fatalf("signed perms.grant: %v", err)
		}
	}
	rec, ok, err := plugins.LoadMetadata(ctx, store, seed.Name)
	if err != nil || !ok {
		t.Fatalf("load after grant: ok=%v err=%v", ok, err)
	}
	wantGrants := append(append([]string(nil), seed.Grants...), params.Capability)
	if !reflect.DeepEqual(rec.Grants, wantGrants) {
		t.Fatalf("Grants = %v, want %v", rec.Grants, wantGrants)
	}
	if !reflect.DeepEqual(rec.TrustGrants, wantGrants) {
		t.Fatalf("TrustGrants = %v, want %v", rec.TrustGrants, wantGrants)
	}
	if got := plugins.EffectiveGrants(rec); !reflect.DeepEqual(got, wantGrants) {
		t.Fatalf("EffectiveGrants = %v, want %v", got, wantGrants)
	}
	assertPermsEffects(t, log, params, 2)
}

func installPermsTestPlugin(t *testing.T, call func(context.Context, string, any, any) error, store provider.Store) plugins.PluginMetadata {
	t.Helper()
	manifest, err := os.ReadFile(filepath.Join("..", "..", "plugins", "github", "manifest.toml"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(manifest)
	p := pluginAddParams{ID: "cascade-github", Checksum: hex.EncodeToString(sum[:]), ManifestBytes: manifest}
	if err := call(t.Context(), "plugin.add", p, nil); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := plugins.LoadMetadata(t.Context(), store, p.ID)
	if err != nil || !ok || rec.TrustTier != plugins.TrustTrusted || rec.ExecDigest == "" {
		t.Fatalf("witnessed install = %+v, %v", rec, err)
	}
	return rec
}

func assertPermsEffects(t *testing.T, log *audit.Log, params plugins.PermsParams, count int) {
	t.Helper()
	page, err := log.Query(t.Context(), audit.Filter{Kinds: []audit.Kind{audit.KindExternalEffect}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(params)
	var records []audit.Record
	for _, rec := range page.Records {
		if rec.Action == "plugin.perms.grant" {
			records = append(records, rec)
		}
	}
	if len(records) != count*2 {
		t.Fatalf("perms effect records = %d, want %d", len(records), count*2)
	}
	for i := 0; i < len(records); i += 2 {
		intent, confirm := records[i], records[i+1]
		if intent.EffectPhase != "intent" || confirm.EffectPhase != "confirmed" || intent.EffectKey != confirm.EffectKey ||
			!strings.HasPrefix(intent.EffectKey, "plugin.perms:"+params.ID+":") || intent.ParamsHash != rpc.ParamsHash(raw) {
			t.Fatalf("invalid perms effect pair: %+v, %+v", intent, confirm)
		}
	}
}

// TestDaemonVerbsAllMarked asserts that every daemon verb registered by
// this ticket is present in ElevatedVerbNames. P1-SEC-03 owns the rows;
// this test only consumes them.
func TestDaemonVerbsAllMarked(t *testing.T) {
	names := rpc.ElevatedVerbNames()
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	for _, m := range []string{"perms.grant", "perms.revoke"} {
		if _, ok := set[m]; !ok {
			t.Errorf("ElevatedVerbNames missing %q", m)
		}
	}
}
