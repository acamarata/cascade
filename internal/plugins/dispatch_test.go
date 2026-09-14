// Purpose: unit coverage for dispatch.go's ProvisionElevated — the daemon-
// side composition root the "plugin.add" RPC handler (cmd/cascade/
// plugin_rpc.go) calls. Every test opens a real modernc-sqlite database
// file under t.TempDir() (internal/storage/plugin_migrate_test.go's own
// precedent for this exact migrator surface — never a self-authored fake
// schema), and asserts real STORE STATE after the call (LANE-RULES §4:
// never merely that a function was called).
//
// SPORT: internal/plugins dispatch (ADD) — P1-E15-W4-S32-T4 COMPLETION PASS.
package plugins

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver for this file's tests

	"github.com/acamarata/cascade/internal/plugins/wasm"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// wasmManifest mirrors builtinManifest (lifecycle_add_test.go) exactly,
// declaring runtime = "wasm".
const wasmManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "wasm"
`

// remoteManifest mirrors builtinManifest, declaring runtime = "remote"
// with a [remote] endpoint (P1-E15-W4-S33-T4: validateRuntime refuses an
// empty remote.host at parse time, so any remote-tier fixture needs one
// even when the test never actually dials it).
const remoteManifest = `
id = "demo"
name = "Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "remote"

[remote]
host = "127.0.0.1"
port = 1
`

type dispatchFixedClock struct{ t time.Time }

func (c dispatchFixedClock) Now() time.Time { return c.t }

// openDispatchTestDB opens a real modernc-sqlite database file under
// t.TempDir(), matching internal/storage/plugin_migrate_test.go's own
// openMigrateTestDB precedent.
func openDispatchTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "dispatch-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustParseManifest(t *testing.T, source string) plugin.Manifest {
	t.Helper()
	m, err := plugin.ParseManifest(strings.NewReader(source))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	return m
}

// TestProvisionElevated_ProcessTierRefuses is the mutation-provable proof
// for process.TrustTierUntrusted's real wiring: no path in this tree ever
// marks a manifest's process.TrustTier above Untrusted, so a process-tier
// elevated install must ALWAYS refuse via the real
// process.ProcessRuntime.Launch trust gate — never write a metadata
// record for a runtime that can never start.
func TestProvisionElevated_ProcessTierRefuses(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	m := mustParseManifest(t, processManifest)

	_, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "", false, nil)
	if err == nil {
		t.Fatal("ProvisionElevated: want a refusal for a process-tier manifest, got nil error")
	}
	if !strings.Contains(err.Error(), "trust_tier") {
		t.Errorf("ProvisionElevated error = %q, want it to contain %q (the real process.wrapUntrusted message)",
			err.Error(), "trust_tier")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindPolicyDenied {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindPolicyDenied, true)", kind, ok)
	}

	// Real STORE STATE: no metadata record was written for a plugin whose
	// runtime can never start.
	if _, ok, lerr := LoadMetadata(ctx, store, "demo"); lerr != nil || ok {
		t.Errorf("LoadMetadata after refused process-tier add: ok=%v err=%v, want ok=false err=nil", ok, lerr)
	}
	// Real STATE: the storage domain was never claimed either — the
	// refusal happens before provisionStorageDomain runs.
	if _, ok := domains.Version("demo"); ok {
		t.Errorf("domains.Version(%q) claimed after a refused install, want unclaimed", "demo")
	}
}

// TestProvisionElevated_WasmTierInstalls is the mutation-provable proof
// for wasm.NewRuntime/DefaultResourceLimits/HostABIVersion's real wiring:
// a wasm-tier elevated install constructs a real wazero runtime and
// persists its reported HostABIVersion into the committed metadata
// record — real STORE STATE, not merely that NewRuntime was called.
func TestProvisionElevated_WasmTierInstalls(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	m := mustParseManifest(t, wasmManifest)

	rec, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "", false, nil)
	if err != nil {
		t.Fatalf("ProvisionElevated: %v", err)
	}
	if rec.HostABIVersion != wasm.HostABIVersion() {
		t.Errorf("rec.HostABIVersion = %d, want %d (wasm.HostABIVersion())", rec.HostABIVersion, wasm.HostABIVersion())
	}
	if rec.HostABIVersion == 0 {
		t.Fatal("rec.HostABIVersion = 0, want a real nonzero ABI version")
	}

	// Real STORE STATE: LoadMetadata (a fresh read, not the in-memory
	// rec) must show the same ABI version and RuntimeMode.
	loaded, ok, lerr := LoadMetadata(ctx, store, "demo")
	if lerr != nil || !ok {
		t.Fatalf("LoadMetadata after wasm-tier add: ok=%v err=%v, want ok=true err=nil", ok, lerr)
	}
	if loaded.HostABIVersion != wasm.HostABIVersion() {
		t.Errorf("LoadMetadata.HostABIVersion = %d, want %d", loaded.HostABIVersion, wasm.HostABIVersion())
	}
	if loaded.RuntimeMode != plugin.RuntimeWasm {
		t.Errorf("LoadMetadata.RuntimeMode = %q, want %q", loaded.RuntimeMode, plugin.RuntimeWasm)
	}
	if !loaded.Enabled {
		t.Error("LoadMetadata.Enabled = false, want true")
	}

	// Real STATE: the storage domain registry (storage.NewPluginDomainRegistry/
	// Register) really claimed this plugin's domain.
	if v, ok := domains.Version("demo"); !ok || v != 1 {
		t.Errorf("domains.Version(%q) = (%d, %v), want (1, true)", "demo", v, ok)
	}
}

// TestProvisionElevated_BuiltinTierInstalls proves the builtin path
// (no runtime construction) still provisions real storage.NewPluginStorage/
// NewPluginMigrator state and commits a metadata record.
func TestProvisionElevated_BuiltinTierInstalls(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	m := mustParseManifest(t, grantingManifest) // builtin-tier, requires=["net.http"]

	rec, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "abc123", false, nil)
	if err != nil {
		t.Fatalf("ProvisionElevated: %v", err)
	}
	if rec.HostABIVersion != 0 {
		t.Errorf("rec.HostABIVersion = %d for a builtin-tier plugin, want 0", rec.HostABIVersion)
	}
	if rec.PinnedChecksum != "abc123" {
		t.Errorf("rec.PinnedChecksum = %q, want %q", rec.PinnedChecksum, "abc123")
	}
	if len(rec.Grants) != 1 || rec.Grants[0] != "net.http" {
		t.Errorf("rec.Grants = %v, want [net.http]", rec.Grants)
	}

	if v, ok := domains.Version("demo"); !ok || v != 1 {
		t.Errorf("domains.Version(%q) = (%d, %v), want (1, true)", "demo", v, ok)
	}

	// Re-provisioning the SAME plugin (e.g. a later `plugin update`
	// elevation) must not fail against the already-claimed domain —
	// PluginDomainRegistry.Register is documented idempotent for the
	// same owner.
	if _, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "abc123", false, nil); err != nil {
		t.Fatalf("second ProvisionElevated for the same plugin id: %v", err)
	}
}

// TestProvisionElevated_RemoteTierRefuses proves a remote-runtime
// manifest refuses with a typed, named error rather than silently
// succeeding or panicking — P1-E15-W4-S33-T4 owns building it for real.
func TestProvisionElevated_RemoteTierRefuses(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	db := openDispatchTestDB(t)
	domains := storage.NewPluginDomainRegistry()
	m := mustParseManifest(t, remoteManifest)

	_, err := ProvisionElevated(ctx, db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", store, domains, m, "", false, nil)
	if err == nil {
		t.Fatal("ProvisionElevated: want a refusal for a remote-tier manifest, got nil error")
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindUnsupported {
		t.Errorf("cascade.KindOf(err) = (%v, %v), want (KindUnsupported, true)", kind, ok)
	}
	if _, ok, _ := LoadMetadata(ctx, store, "demo"); ok {
		t.Error("LoadMetadata after refused remote-tier add: ok=true, want false")
	}
}

// The flag=true, real-handshake path (ProvisionElevated with
// enableRemoteRuntime=true over a real loopback server) needs a real
// socket, which internal/build's TestNoNetworkUnitTest_RealTreeGreen
// gate forbids in any non-integration _test.go tree-wide (AGENT-BRIEF /
// LANE-RULES §6). That proof lives in
// dispatch_remote_integration_test.go (`//go:build integration`)
// instead; found the hard way when this file originally imported
// net/net-http/httptest directly and the gate caught it.
