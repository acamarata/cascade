// Package plugins (perms_rpc_test.go): Purpose: unit tests for the
// perms.grant and perms.revoke RPC handlers.
// Inputs: an in-memory store, a real elevation middleware witness minted
// by attestedCall (update_rpc_test.go), and a fake EffectWriter that
// records intent/confirm/fail calls without touching disk.
// Outputs: deterministic pass/fail; no network, no real audit log.
// Constraints: test home directories and executables stay under t.TempDir();
// no test writes TrustTier or ExecDigest by hand — trusted records come
// only from the witnessed grant path.
// SPORT: internal/plugins perms RPC tests (P1-PLG-11).
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fakeEffectWriter is a minimal audit.EffectWriter that records calls so
// tests can assert intent+confirm pairs without standing up a real log.
type fakeEffectWriter struct {
	mu       sync.Mutex
	begins   []string
	confirms []string
	fails    []string
	beginErr error
	cancel   context.CancelFunc
}

func (f *fakeEffectWriter) BeginEffect(_ context.Context, req audit.EffectRequest) (audit.EffectHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.begins = append(f.begins, req.Key)
	if f.cancel != nil {
		f.cancel()
	}
	return audit.EffectHandle{Key: req.Key, Kind: req.Kind, IntentSeq: uint64(len(f.begins)), IntentID: req.Key}, f.beginErr
}

func (f *fakeEffectWriter) ConfirmEffect(ctx context.Context, h audit.EffectHandle, _ string) (audit.Record, error) {
	if err := ctx.Err(); err != nil {
		return audit.Record{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirms = append(f.confirms, h.Key)
	return audit.Record{}, nil
}

func (f *fakeEffectWriter) FailEffect(ctx context.Context, h audit.EffectHandle, _ string) (audit.Record, error) {
	if err := ctx.Err(); err != nil {
		return audit.Record{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails = append(f.fails, h.Key)
	return audit.Record{}, nil
}

// TestPermsHandlerRefusesWrongWitness covers acceptance [1]: no witness, a
// perms.revoke witness at perms.grant, and a witness whose ParamsHash
// differs from the stashed params each refuse and write nothing.
func TestPermsHandlerRefusesWrongWitness(t *testing.T) {
	isolatePermsTestHome(t)
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo", InstalledVersion: "1.0.0", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"id":"demo","capability":"net.http"}`)
	before := storedRecordHash(t, store, "demo")
	effects := &fakeEffectWriter{}

	// No witness at all.
	if _, err := PermsRPCHandler(store, effects, true)(ctx, args); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("unattested perms.grant = %v", err)
	}
	if len(effects.begins) != 0 {
		t.Fatalf("unattested call began an effect: %v", effects.begins)
	}

	// Witness for the other verb.
	if _, err := attestedCall(t, PermsRevokeRPCMethod, args, InstallMiddleware(nil, store)(PermsRevokeRPCMethod, PermsRPCHandler(store, effects, true))); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("perms.grant under perms.revoke witness = %v", err)
	}
	if len(effects.begins) != 0 {
		t.Fatalf("wrong-verb call began an effect: %v", effects.begins)
	}

	// Witness for this verb but different params.
	var witnessed context.Context
	if _, err := attestedCall(t, PermsGrantRPCMethod, args, func(ctx context.Context, _ json.RawMessage) (any, error) {
		witnessed = withInstallParams(ctx, args)
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}
	other := json.RawMessage(`{"id":"demo","capability":"other.cap"}`)
	if _, err := PermsRPCHandler(store, effects, true)(withInstallParams(witnessed, other), other); !cascade.HasKind(err, cascade.KindElevationRequired) {
		t.Fatalf("params-mismatched perms.grant = %v", err)
	}
	if len(effects.begins) != 0 {
		t.Fatalf("params-mismatched call began an effect: %v", effects.begins)
	}
	if after := storedRecordHash(t, store, "demo"); after != before {
		t.Fatal("a refused call changed metadata")
	}
}

// TestPermsRevokeNarrowsBoth covers acceptance [4]: an attested revoke
// removes the capability from Grants and TrustGrants on a trusted process
// plugin, so EffectiveGrants no longer includes it.
func TestPermsRevokeNarrowsBoth(t *testing.T) {
	isolatePermsTestHome(t)
	ctx := context.Background()
	f := grantedTrustDemo(t)
	store := f.store
	effects := &fakeEffectWriter{}
	args := json.RawMessage(`{"id":"trustdemo","capability":"net.http:api.github.com"}`)

	res, err := attestedCall(t, PermsRevokeRPCMethod, args, InstallMiddleware(nil, store)(PermsRevokeRPCMethod, PermsRPCHandler(store, effects, false)))
	if err != nil {
		t.Fatalf("attested revoke = %v", err)
	}
	view := res.(PermsResult)
	if len(view.After) != 1 || view.After[0] != "host.secret_ref" {
		t.Fatalf("revoke grants_after = %v", view.After)
	}
	got, ok, err := LoadMetadata(ctx, store, "trustdemo")
	if err != nil || !ok {
		t.Fatalf("load after revoke: ok=%v err=%v", ok, err)
	}
	if len(got.Grants) != 1 || got.Grants[0] != "host.secret_ref" {
		t.Fatalf("stored Grants = %v", got.Grants)
	}
	if len(got.TrustGrants) != 1 || got.TrustGrants[0] != "host.secret_ref" {
		t.Fatalf("stored TrustGrants = %v", got.TrustGrants)
	}
	if eg := EffectiveGrants(got); len(eg) != 1 || eg[0] != "host.secret_ref" {
		t.Fatalf("EffectiveGrants = %v", eg)
	}
	if len(effects.begins) != 1 || len(effects.confirms) != 1 {
		t.Fatalf("effects begins=%d confirms=%d", len(effects.begins), len(effects.confirms))
	}
}

func isolatePermsTestHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "CASCADE_HOME"} {
		t.Setenv(key, home)
	}
}

// TestPermsIntentNotificationFailure refuses the write and settles an intent
// that was committed before BeginEffect returned a notification error.
func TestPermsIntentNotificationFailure(t *testing.T) {
	isolatePermsTestHome(t)
	store := storetest.NewMemStore()
	if err := SaveMetadata(t.Context(), store, PluginMetadata{Name: "demo"}); err != nil {
		t.Fatal(err)
	}
	before := storedRecordHash(t, store, "demo")
	wantErr := errors.New("intent notification failed")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	effects := &fakeEffectWriter{beginErr: wantErr, cancel: cancel}
	_, err := commitPermsChange(ctx, store, effects, PermsParams{ID: "demo", Capability: "net.http"}, true, "hash")
	if !errors.Is(err, wantErr) {
		t.Fatalf("grant = %v, want notification failure", err)
	}
	if len(effects.begins) != 1 || len(effects.fails) != 1 || len(effects.confirms) != 0 {
		t.Fatalf("effect lifecycle = %+v", effects)
	}
	if after := storedRecordHash(t, store, "demo"); after != before {
		t.Fatal("failed intent changed metadata")
	}
}

// TestPermsStoreFailureSettlesIntent preserves metadata and records failure
// after an intent succeeds but the metadata commit fails.
func TestPermsStoreFailureSettlesIntent(t *testing.T) {
	isolatePermsTestHome(t)
	store := seededFailingStore(t, func(f *failingStore) { f.failPut = true })
	before := storedRecordHash(t, store, "seeded")
	effects := &fakeEffectWriter{}
	_, err := commitPermsChange(t.Context(), store, effects, PermsParams{ID: "seeded", Capability: "net.http"}, true, "hash")
	if !errors.Is(err, errStoreFailure) {
		t.Fatalf("grant = %v, want store failure", err)
	}
	if len(effects.begins) != 1 || len(effects.fails) != 1 || len(effects.confirms) != 0 {
		t.Fatalf("effect lifecycle = %+v", effects)
	}
	if after := storedRecordHash(t, store, "seeded"); after != before {
		t.Fatal("failed save changed metadata")
	}
}
