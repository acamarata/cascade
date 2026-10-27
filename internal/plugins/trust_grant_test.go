// Purpose: grantProcessTrust through ProvisionElevated: a process-tier record
// is trusted only under a real plugin.add witness (minted by the real
// rpc.ElevationMiddleware in attestedCall) whose params hash covers the exact
// raw params InstallMiddleware bound, naming this manifest and a checksum that
// is the manifest's sha256. Every refusal is KindPolicyDenied and writes
// nothing. The executable is a fake file under t.TempDir().
// SPORT: internal/plugins process trust grant tests (P1-PLG-10).
package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// trustManifest is a process-tier manifest without oauth:repo.
const trustManifest = `
id = "trustdemo"
name = "Trust Demo"
schema = "cascade.plugin/v2"
version = "1.0.0"
host_version = ">=2.0.0"
runtime = "process"
requires = ["net.http:api.github.com", "host.secret_ref"]
`

// fakeExecutable is the body of every fake plugin executable (never run).
const fakeExecutable = "#!/bin/sh\n# fake process plugin for tests; never executed\nexit 0\n"

// trustFixture is a store, a plugin database and an injected executable dir
// holding a fake, owner-writable-only executable for one plugin id.
type trustFixture struct {
	store   *storetest.MemStore
	db      *sql.DB
	domains *storage.PluginDomainRegistry
	exe     string
	digest  string
}

func newTrustFixture(t *testing.T, id string) trustFixture {
	t.Helper()
	dir := t.TempDir()
	name := id
	if goruntime.GOOS == "windows" {
		name += ".exe"
	}
	exe := filepath.Join(dir, name)
	if err := os.WriteFile(exe, []byte(fakeExecutable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetExecutableDirForTest(dir))
	return trustFixture{store: storetest.NewMemStore(), db: openDispatchTestDB(t),
		domains: storage.NewPluginDomainRegistry(), exe: exe, digest: sha256Hex([]byte(fakeExecutable))}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// installArgs is the plugin.add / plugin.update wire params.
func installArgs(t *testing.T, id, checksum, manifest string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(boundInstallParams{ID: id, Checksum: checksum, ManifestBytes: []byte(manifest)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// pinnedArgs is installArgs with the manifest's own sha256 as the checksum.
func pinnedArgs(t *testing.T, id, manifest string) json.RawMessage {
	return installArgs(t, id, sha256Hex([]byte(manifest)), manifest)
}

// provision is what cmd/cascade's plugin.add handler does with its params.
func (f trustFixture) provision(ctx context.Context, m plugin.Manifest, checksum string) (PluginMetadata, error) {
	return ProvisionElevated(ctx, f.db, migrate.SQLiteEmitter{}, dispatchFixedClock{}, "", "", f.store, f.domains, m, checksum, false, nil)
}

// addHandler decodes raw the way the production plugin.add handler does.
func (f trustFixture) addHandler() rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p boundInstallParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		m, err := plugin.ParseManifest(bytes.NewReader(p.ManifestBytes))
		if err != nil {
			return nil, err
		}
		return f.provision(ctx, m, p.Checksum)
	}
}

// attested dispatches raw through the real elevation middleware gating gate,
// then InstallMiddleware for verb, then handler.
func attested(t *testing.T, gate, verb string, raw json.RawMessage, handler rpc.HandlerFunc) error {
	t.Helper()
	_, err := attestedCall(t, gate, raw, InstallMiddleware(nil, nil)(verb, handler))
	return err
}

// requireNoTrustedRecord asserts a KindPolicyDenied refusal that wrote no
// record and claimed no storage domain.
func (f trustFixture) requireNoTrustedRecord(t *testing.T, err error, id string, fragments ...string) {
	t.Helper()
	requireRefusal(t, err, cascade.KindPolicyDenied, fragments...)
	if _, ok, lerr := LoadMetadata(context.Background(), f.store, id); lerr != nil || ok {
		t.Fatalf("a refused grant left a record: ok=%v err=%v", ok, lerr)
	}
	if _, ok := f.domains.Version(id); ok {
		t.Fatalf("a refused grant claimed %s's storage domain", id)
	}
}

// Positive control: the same path the refusals below use grants trust.
func TestProvisionElevatedGrantsUnderMatchingWitness(t *testing.T) {
	f := newTrustFixture(t, "trustdemo")
	if err := attested(t, "plugin.add", "plugin.add", pinnedArgs(t, "trustdemo", trustManifest), f.addHandler()); err != nil {
		t.Fatalf("witnessed plugin.add: %v", err)
	}
	rec, ok, err := LoadMetadata(context.Background(), f.store, "trustdemo")
	if err != nil || !ok {
		t.Fatalf("no record after a witnessed add: ok=%v err=%v", ok, err)
	}
	want := "net.http:api.github.com,host.secret_ref"
	if rec.TrustTier != TrustTrusted || rec.ExecDigest != f.digest || strings.Join(rec.TrustGrants, ",") != want {
		t.Fatalf("record = %+v, want trusted, digest %s, trust grants %s", rec, f.digest, want)
	}
	if got := strings.Join(EffectiveGrants(rec), ","); got != want {
		t.Fatalf("EffectiveGrants = %s, want %s", got, want)
	}
}

func TestProvisionElevatedNilWitnessRefuses(t *testing.T) {
	f := newTrustFixture(t, "trustdemo")
	raw := pinnedArgs(t, "trustdemo", trustManifest)
	// InstallMiddleware binds the params, but no elevation ran.
	_, err := InstallMiddleware(nil, nil)("plugin.add", f.addHandler())(context.Background(), raw)
	f.requireNoTrustedRecord(t, err, "trustdemo", "no verified elevation witness", "cascade plugin add")
	// Neither a direct ProvisionElevated call.
	_, err = f.provision(context.Background(), mustParseManifest(t, trustManifest), sha256Hex([]byte(trustManifest)))
	f.requireNoTrustedRecord(t, err, "trustdemo", "no verified elevation witness")
}

func TestProvisionElevatedWrongMethodWitnessRefuses(t *testing.T) {
	f := newTrustFixture(t, "trustdemo")
	err := attested(t, UpdateRPCMethod, "plugin.add", pinnedArgs(t, "trustdemo", trustManifest), f.addHandler())
	f.requireNoTrustedRecord(t, err, "trustdemo", "covers plugin.update, not plugin.add")
}

func TestProvisionElevatedWitnessHashMismatchRefuses(t *testing.T) {
	f := newTrustFixture(t, "trustdemo")
	verified := pinnedArgs(t, "trustdemo", trustManifest)
	// A valid manifest that also asks for oauth:repo, pinned by its own
	// checksum, swapped in after the elevation middleware verified the call.
	wider := strings.Replace(trustManifest, `"host.secret_ref"]`, `"host.secret_ref", "oauth:repo"]`, 1)
	swap := func(ctx context.Context, _ json.RawMessage) (any, error) {
		return InstallMiddleware(nil, nil)("plugin.add", f.addHandler())(ctx, pinnedArgs(t, "trustdemo", wider))
	}
	_, err := attestedCall(t, "plugin.add", verified, swap)
	f.requireNoTrustedRecord(t, err, "trustdemo", "does not cover these params")
}

func TestProvisionElevatedParamsDecodeMismatchRefuses(t *testing.T) {
	other := strings.Replace(trustManifest, `"1.0.0"`, `"1.0.1"`, 1)
	renamed := strings.Replace(trustManifest, `"trustdemo"`, `"otherdemo"`, 1)
	pinned := sha256Hex([]byte(trustManifest))
	for name, tc := range map[string]struct{ provisioned, checksum, fragment string }{
		"another manifest": {other, pinned, "name another manifest"},
		"another id":       {renamed, pinned, "name another plugin id"},
		"another checksum": {trustManifest, strings.ToUpper(pinned), "carry another checksum"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newTrustFixture(t, "trustdemo")
			raw := pinnedArgs(t, "trustdemo", trustManifest)
			handler := func(ctx context.Context, _ json.RawMessage) (any, error) {
				return f.provision(ctx, mustParseManifest(t, tc.provisioned), tc.checksum)
			}
			err := attested(t, "plugin.add", "plugin.add", raw, handler)
			requireRefusal(t, err, cascade.KindPolicyDenied, tc.fragment)
			for _, id := range []string{"trustdemo", "otherdemo"} {
				if _, ok, _ := LoadMetadata(context.Background(), f.store, id); ok {
					t.Fatalf("a refused grant wrote %s", id)
				}
			}
		})
	}
}

func TestProvisionElevatedEmptyChecksumNeverTrusted(t *testing.T) {
	f := newTrustFixture(t, "trustdemo")
	err := attested(t, "plugin.add", "plugin.add", installArgs(t, "trustdemo", "", trustManifest), f.addHandler())
	f.requireNoTrustedRecord(t, err, "trustdemo", "no checksum pins the manifest")
}

func TestProvisionElevatedChecksumMustMatchManifest(t *testing.T) {
	f := newTrustFixture(t, "trustdemo")
	wrong := sha256Hex([]byte("some other bytes"))
	err := attested(t, "plugin.add", "plugin.add", installArgs(t, "trustdemo", wrong, trustManifest), f.addHandler())
	f.requireNoTrustedRecord(t, err, "trustdemo", "not the sha256 of the manifest bytes")
}

// A witnessed add whose executable is missing or group-writable refuses.
func TestProvisionElevatedRefusesUntrustableExecutable(t *testing.T) {
	f := newTrustFixture(t, "trustdemo")
	if goruntime.GOOS != "windows" {
		if err := os.Chmod(f.exe, 0o775); err != nil {
			t.Fatal(err)
		}
		err := attested(t, "plugin.add", "plugin.add", pinnedArgs(t, "trustdemo", trustManifest), f.addHandler())
		f.requireNoTrustedRecord(t, err, "trustdemo", "("+reasonWritable+")")
	}
	if err := os.Remove(f.exe); err != nil {
		t.Fatal(err)
	}
	err := attested(t, "plugin.add", "plugin.add", pinnedArgs(t, "trustdemo", trustManifest), f.addHandler())
	f.requireNoTrustedRecord(t, err, "trustdemo", "("+reasonMissing+")")
}

// A non-elevated update never leaves a trusted record with an empty pin: with
// no checksum of the new manifest bytes it is refused and writes nothing.
func TestUpdateOfTrustedRecordNeedsManifestPin(t *testing.T) {
	f := grantedTrustDemo(t)
	before := storedRecordHash(t, f.store, "trustdemo")
	bumped := strings.Replace(trustManifest, `"1.0.0"`, `"1.0.1"`, 1)
	calls := 0
	_, err := UpdatePlugin(context.Background(), f.store, nil, okHandshaker{calls: &calls}, []byte(bumped), "", nil, true)
	requireRefusal(t, err, cascade.KindPolicyDenied, "sha256 of the new manifest bytes")
	if calls != 0 || storedRecordHash(t, f.store, "trustdemo") != before {
		t.Fatal("a refused update handshook or wrote")
	}
}

// Expansion is measured against EffectiveGrants: stored grants widened by a
// perms grant hide nothing, and an untrusted process record keeps nothing.
func TestUpdateExpansionUsesEffectiveGrants(t *testing.T) {
	ctx := context.Background()
	f := grantedTrustDemo(t)
	if _, err := ChangePerms(ctx, f.store, "trustdemo", "oauth:repo", true, true, false); err != nil {
		t.Fatal(err)
	}
	calls := 0
	res, err := UpdatePlugin(ctx, f.store, nil, okHandshaker{calls: &calls}, []byte(widerTrustManifest), sha256Hex([]byte(widerTrustManifest)), []byte(widerTrustManifest), true)
	if err != nil || res.Outcome != UpdateOutcomeElevationRequired || calls != 0 {
		t.Fatalf("update to a manifest adding oauth:repo after a store-only grant = %+v, %v; want ElevationRequired", res, err)
	}
	untrusted := PluginMetadata{Name: "trustdemo", InstalledVersion: "1.0.0", Enabled: true, RuntimeMode: plugin.RuntimeProcess,
		Grants: []string{"net.http:api.github.com", "host.secret_ref"}}
	if err := SaveMetadata(ctx, f.store, untrusted); err != nil {
		t.Fatal(err)
	}
	same := strings.Replace(trustManifest, `"1.0.0"`, `"1.0.1"`, 1)
	res, err = UpdatePlugin(ctx, f.store, nil, okHandshaker{calls: &calls}, []byte(same), "", nil, true)
	if err != nil || res.Outcome != UpdateOutcomeElevationRequired || calls != 0 {
		t.Fatalf("update of an untrusted process record = %+v, %v; want ElevationRequired", res, err)
	}
}

// An attested process-tier plugin.update meets the trust grant first: a wrong
// checksum is the grant's policy-denied refusal, and a granted candidate with
// no handshaker (the daemon today) is refused as unconfirmed, writing nothing.
func TestAttestedProcessUpdateRefusalOrder(t *testing.T) {
	f := grantedTrustDemo(t)
	before := storedRecordHash(t, f.store, "trustdemo")
	wrong := installArgs(t, "trustdemo", sha256Hex([]byte("other bytes")), widerTrustManifest)
	err := attested(t, UpdateRPCMethod, UpdateRPCMethod, wrong, UpdateRPCHandler(f.store, nil))
	requireRefusal(t, err, cascade.KindPolicyDenied, "not the sha256 of the manifest bytes")
	err = attested(t, UpdateRPCMethod, UpdateRPCMethod, pinnedArgs(t, "trustdemo", widerTrustManifest), UpdateRPCHandler(f.store, nil))
	requireRefusal(t, err, cascade.KindUnavailable, "cannot confirm the process handshake")
	if storedRecordHash(t, f.store, "trustdemo") != before {
		t.Fatal("a refused attested update wrote")
	}
}
