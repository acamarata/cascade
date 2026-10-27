package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestHostNamespaceMatchesReservedStorageNamespace(t *testing.T) {
	// Defense against drift: internal/storage.ReservedPluginHostNamespace
	// cannot be imported here (depguard), so this pins the literal this
	// package redeclares against the one internal/storage owns, in the
	// one place (a _test.go file, exempt from the boundary rule) that can
	// see both.
	const wantFromStorage = "plugin.__host__"
	if HostNamespace != wantFromStorage {
		t.Fatalf("HostNamespace = %q, want %q (must match internal/storage.ReservedPluginHostNamespace)", HostNamespace, wantFromStorage)
	}
}

func TestMetadataCRUD_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	if _, ok, err := LoadMetadata(ctx, store, "demo"); err != nil || ok {
		t.Fatalf("LoadMetadata on empty store: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	rec := PluginMetadata{Name: "demo", InstalledVersion: "1.0.0", Enabled: true, Grants: []string{"net.http"}}
	if err := SaveMetadata(ctx, store, rec); err != nil {
		t.Fatalf("SaveMetadata: %v", err)
	}

	got, ok, err := LoadMetadata(ctx, store, "demo")
	if err != nil || !ok {
		t.Fatalf("LoadMetadata after save: ok=%v err=%v", ok, err)
	}
	if got.InstalledVersion != "1.0.0" || !got.Enabled || len(got.Grants) != 1 {
		t.Fatalf("LoadMetadata returned %+v, want a round trip of %+v", got, rec)
	}

	if err := DeleteMetadata(ctx, store, "demo"); err != nil {
		t.Fatalf("DeleteMetadata: %v", err)
	}
	if _, ok, err := LoadMetadata(ctx, store, "demo"); err != nil || ok {
		t.Fatalf("LoadMetadata after delete: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	// Delete is idempotent per provider.Store.Delete's own contract.
	if err := DeleteMetadata(ctx, store, "demo"); err != nil {
		t.Fatalf("DeleteMetadata on already-absent record: %v, want nil (idempotent)", err)
	}
}

func TestListMetadata_SortedAndIsolatedFromOtherNamespaces(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()

	for _, rec := range []PluginMetadata{
		{Name: "zeta", InstalledVersion: "1.0.0"},
		{Name: "alpha", InstalledVersion: "2.0.0"},
		{Name: "mid", InstalledVersion: "3.0.0"},
	} {
		if err := SaveMetadata(ctx, store, rec); err != nil {
			t.Fatalf("SaveMetadata(%s): %v", rec.Name, err)
		}
	}
	// A write into an unrelated namespace must never surface in ListMetadata.
	if err := store.Put(ctx, "plugin.alpha", "some-other-key", []byte("not metadata")); err != nil {
		t.Fatalf("seed unrelated namespace: %v", err)
	}

	got, err := ListMetadata(ctx, store)
	if err != nil {
		t.Fatalf("ListMetadata: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListMetadata returned %d records, want 3 (got %+v)", len(got), got)
	}
	for i, want := range []string{"alpha", "mid", "zeta"} {
		if got[i].Name != want {
			t.Fatalf("ListMetadata[%d].Name = %q, want %q (not sorted)", i, got[i].Name, want)
		}
	}
}

func TestValidateName_EmptyRefused(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if _, _, err := LoadMetadata(ctx, store, "  "); err == nil {
		t.Fatal("LoadMetadata(blank name) succeeded, want a refusal")
	}
	if err := SaveMetadata(ctx, store, PluginMetadata{}); err == nil {
		t.Fatal("SaveMetadata(empty Name) succeeded, want a refusal")
	}
	if err := DeleteMetadata(ctx, store, ""); err == nil {
		t.Fatal("DeleteMetadata(empty name) succeeded, want a refusal")
	}
}

func TestChangePerms_EmptyCapabilityRefused(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ChangePerms(ctx, store, "demo", "  ", true, true, false); err == nil {
		t.Fatal("ChangePerms with a blank capability succeeded, want a refusal")
	}
	if _, err := ChangePerms(ctx, store, "  ", "net.http", false, true, false); err == nil {
		t.Fatal("ChangePerms revoke on a blank plugin name succeeded, want a refusal")
	}
}

func TestListMetadata_CorruptRecordSurfacesIntegrityError(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := store.Put(ctx, HostNamespace, "metadata/broken", []byte("not json")); err != nil {
		t.Fatalf("seed corrupt record: %v", err)
	}
	if _, err := ListMetadata(ctx, store); err == nil {
		t.Fatal("ListMetadata over a corrupt record succeeded, want KindIntegrity")
	} else if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Fatalf("kind = %v (ok=%v), want KindIntegrity", kind, ok)
	}
}

func TestGrantsExpand(t *testing.T) {
	cases := []struct {
		name       string
		have, want []string
		expand     bool
	}{
		{"nil-have nil-want", nil, nil, false},
		{"nil-have some-want", nil, []string{"net.http"}, true},
		{"subset", []string{"net.http", "fs.read"}, []string{"net.http"}, false},
		{"same set", []string{"net.http"}, []string{"net.http"}, false},
		{"new capability", []string{"net.http"}, []string{"net.http", "fs.write"}, true},
	}
	for _, c := range cases {
		if got := grantsExpand(c.have, c.want); got != c.expand {
			t.Errorf("%s: grantsExpand(%v, %v) = %v, want %v", c.name, c.have, c.want, got, c.expand)
		}
	}
}

func TestChangePerms_DaemonlessElevatedRefusal(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	_, err := ChangePerms(ctx, store, "demo", "net.http", true, false /* daemonAvailable */, false)
	if err == nil {
		t.Fatal("ChangePerms with daemonAvailable=false succeeded, want the daemon-required refusal")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindUnavailable {
		t.Fatalf("ChangePerms daemonless error kind = %v (ok=%v), want KindUnavailable", kind, ok)
	}
}

func TestPluginPerms_NoInputHardError(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := ChangePerms(ctx, store, "demo", "net.http", true, true /* daemonAvailable */, true /* noInput */)
	if err == nil {
		t.Fatal("ChangePerms with noInput=true succeeded, want the CASCADE_NO_INPUT hard error")
	}
	kind, ok := cascade.KindOf(err)
	if !ok || kind != cascade.KindInvalidInput {
		t.Fatalf("ChangePerms no-input error kind = %v (ok=%v), want KindInvalidInput", kind, ok)
	}
}

func TestChangePerms_GrantAndRevoke(t *testing.T) {
	ctx := context.Background()
	store := storetest.NewMemStore()
	if err := SaveMetadata(ctx, store, PluginMetadata{Name: "demo", Grants: []string{"fs.read"}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec, err := ChangePerms(ctx, store, "demo", "net.http", true, true, false)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if len(rec.Grants) != 2 {
		t.Fatalf("after grant, Grants = %v, want 2 entries", rec.Grants)
	}
	// Idempotent re-grant.
	rec2, err := ChangePerms(ctx, store, "demo", "net.http", true, true, false)
	if err != nil || len(rec2.Grants) != 2 {
		t.Fatalf("idempotent re-grant: rec=%+v err=%v", rec2, err)
	}

	rec3, err := ChangePerms(ctx, store, "demo", "net.http", false, true, false)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if len(rec3.Grants) != 1 || rec3.Grants[0] != "fs.read" {
		t.Fatalf("after revoke, Grants = %v, want [fs.read]", rec3.Grants)
	}

	// ChangePerms on an unknown plugin refuses NotFound (with a daemon and
	// input available, so the refusal really is about the name).
	if _, err := ChangePerms(ctx, store, "ghost", "net.http", true, true, false); err == nil {
		t.Fatal("ChangePerms on an unknown plugin succeeded, want KindNotFound")
	}
}

// trustFields renders the three trust fields for a byte comparison.
func trustFields(rec PluginMetadata) string {
	b, _ := json.Marshal([]any{rec.TrustTier, rec.ExecDigest, rec.TrustGrants})
	return string(b)
}

// grantedTrustDemo installs trustManifest, trusted, through the witnessed path.
func grantedTrustDemo(t *testing.T) trustFixture {
	t.Helper()
	f := newTrustFixture(t, "trustdemo")
	if err := attested(t, "plugin.add", "plugin.add", pinnedArgs(t, "trustdemo", trustManifest), f.addHandler()); err != nil {
		t.Fatalf("witnessed plugin.add: %v", err)
	}
	return f
}

// widerTrustManifest is trustManifest at 1.1.0 also asking for oauth:repo.
var widerTrustManifest = strings.Replace(strings.Replace(trustManifest, `"1.0.0"`, `"1.1.0"`, 1),
	`"host.secret_ref"]`, `"host.secret_ref", "oauth:repo"]`, 1)

func TestUpdatePreservesTrustFields(t *testing.T) {
	ctx := context.Background()
	f := grantedTrustDemo(t)
	before, _, _ := LoadMetadata(ctx, f.store, "trustdemo")
	if before.TrustTier != TrustTrusted {
		t.Fatalf("seed record is not trusted: %+v", before)
	}
	// Same runtime, one grant dropped, version bumped: not elevated.
	narrower := strings.Replace(strings.Replace(trustManifest, `"1.0.0"`, `"1.1.0"`, 1), `, "host.secret_ref"`, ``, 1)
	calls := 0
	res, err := UpdatePlugin(ctx, f.store, nil, okHandshaker{calls: &calls}, []byte(narrower), sha256Hex([]byte(narrower)), []byte(narrower), true)
	if err != nil || res.Outcome != UpdateOutcomeUpdated || calls != 1 {
		t.Fatalf("non-elevated update = %+v, %v (handshakes %d)", res, err, calls)
	}
	after, _, _ := LoadMetadata(ctx, f.store, "trustdemo")
	if after.InstalledVersion != "1.1.0" || after.PinnedChecksum != sha256Hex([]byte(narrower)) || trustFields(after) != trustFields(before) {
		t.Fatalf("trust fields %s -> %s across a non-elevated update", trustFields(before), trustFields(after))
	}
	if got := strings.Join(EffectiveGrants(after), ","); got != "net.http:api.github.com" {
		t.Fatalf("EffectiveGrants after narrowing = %s", got)
	}
	// A grant-expanding non-elevated update writes nothing.
	hash := storedRecordHash(t, f.store, "trustdemo")
	res, err = UpdatePlugin(ctx, f.store, nil, okHandshaker{calls: &calls}, []byte(widerTrustManifest), "", nil, true)
	if err != nil || res.Outcome != UpdateOutcomeElevationRequired || storedRecordHash(t, f.store, "trustdemo") != hash {
		t.Fatalf("expanding update = %+v, %v; want ElevationRequired and no write", res, err)
	}
}

// TestAttestedProcessUpdateRegrantsTrust: a witnessed plugin.update of a
// process manifest re-grants trust over the new grant set; without bound
// params, or with a handshake failing after the grant, nothing is written.
func TestAttestedProcessUpdateRegrantsTrust(t *testing.T) {
	ctx := context.Background()
	f := grantedTrustDemo(t)
	args := pinnedArgs(t, "trustdemo", widerTrustManifest)
	before := storedRecordHash(t, f.store, "trustdemo")
	calls := 0
	ok := func() ProcessHandshaker { return okHandshaker{calls: &calls} }
	failing := func() ProcessHandshaker { return alwaysFailHandshaker{err: errors.New("hello_ack timed out")} }
	err := attested(t, UpdateRPCMethod, UpdateRPCMethod, args, UpdateRPCHandler(f.store, failing))
	requireRefusal(t, err, cascade.KindUnavailable, "handshake failed")
	_, err = attestedCall(t, UpdateRPCMethod, args, UpdateRPCHandler(f.store, ok))
	requireRefusal(t, err, cascade.KindPolicyDenied, "no install params bound")
	if calls != 0 || storedRecordHash(t, f.store, "trustdemo") != before {
		t.Fatalf("a refused update handshook %d times or changed the record", calls)
	}
	if err := attested(t, UpdateRPCMethod, UpdateRPCMethod, args, UpdateRPCHandler(f.store, ok)); err != nil {
		t.Fatalf("witnessed plugin.update: %v", err)
	}
	rec, _, _ := LoadMetadata(ctx, f.store, "trustdemo")
	if rec.TrustTier != TrustTrusted || rec.ExecDigest != f.digest || !strings.Contains(strings.Join(rec.TrustGrants, ","), "oauth:repo") || calls != 1 {
		t.Fatalf("record after a witnessed update = %+v (handshakes %d)", rec, calls)
	}
}
