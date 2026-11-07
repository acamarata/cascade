// Purpose: proves newEvidenceKeyFn binds a provider's identity, not its
// name: over a real migrated providers.db and a fake standing-grant source,
// each identity change moves the key, removal is KindNotFound, a keyless
// provider fingerprints "none", a bad key file is refused, no credential
// canary reaches a key or error, and the gateway leg reads no vault.
// Inputs: temp HOME, a migrated providers.db, in-memory credentials.
// Outputs: none.
// Constraints: untagged, no network, no vault, no keychain, no helper run.
// SPORT: cmd/cascade billing evidence (NEW, P1-TOP-19c).

package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/providers/gateway"
	providerregistry "github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

const evkCanary = "evk-canary-value-7f3a"

// evkClock is a settable injected clock.
type evkClock struct{ now time.Time }

func (c *evkClock) Now() time.Time { return c.now }

// evkCreds is a counting CredentialSource over a fixed map.
type evkCreds struct {
	vals  map[string]string
	calls int
}

func (c *evkCreds) Resolve(_ context.Context, ref string) (string, error) {
	c.calls++
	if v, ok := c.vals[ref]; ok {
		return v, nil
	}
	return "", cascade.Newf(cascade.KindNotFound, "test: no credential %q", ref)
}

// evkEnv isolates HOME and opens a migrated registry under it.
func evkEnv(t *testing.T) (string, *providerregistry.Registry, *evkClock) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("CASCADE_HOME", dir)
	clk := &evkClock{now: time.UnixMilli(1_700_000_000_000).UTC()}
	db, err := openMigratedDB(context.Background(), filepath.Join(dir, "providers.db"),
		func(ctx context.Context, db *sql.DB) error {
			return providerregistry.ApplyMigrationSchema(ctx, db, migrate.SQLiteEmitter{}, clk, "", "")
		})
	if err != nil {
		t.Fatalf("openMigratedDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return dir, providerregistry.NewRegistry(db, clk), clk
}

func evkRecord(name, base string) providerregistry.ProviderRecord {
	return providerregistry.ProviderRecord{
		Name: name, Driver: providerregistry.DriverAnthropic, BaseURL: base,
		Auth: providerregistry.AuthKey, AuthRef: providerregistry.VaultKeyRef(name + "-ref"),
		AccountKind: providerregistry.AccountPersonal, Tier: providerregistry.TierMid,
	}
}

func evkKey(t *testing.T, fn topology.EvidenceKeyFn, name string) topology.EvidenceKey {
	t.Helper()
	k, err := fn(context.Background(), topology.AccountID(name))
	if err != nil {
		t.Fatalf("key %s: %v", name, err)
	}
	if strings.Contains(k.Provider+k.InstanceID+k.Endpoint+k.AuthRef+k.CredentialFingerprint, evkCanary) {
		t.Fatalf("credential canary in key %+v", k)
	}
	return k
}

func evkWantErr(t *testing.T, err error, kind cascade.Kind, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %v error containing %q, got nil", kind, msg)
	}
	if got, _ := cascade.KindOf(err); got != kind {
		t.Fatalf("kind = %v, want %v (%v)", got, kind, err)
	}
	if !strings.Contains(err.Error(), msg) || strings.Contains(err.Error(), evkCanary) {
		t.Fatalf("error %q: want %q and no canary", err.Error(), msg)
	}
}

// evkFixture is the shared state of the subtests.
type evkFixture struct {
	dir, keyPath string
	reg          *providerregistry.Registry
	clk          *evkClock
	creds        *evkCreds
}

func (f *evkFixture) newFn(t *testing.T, accts []gateway.Account) topology.EvidenceKeyFn {
	t.Helper()
	fn, err := newEvidenceKeyFn(evidenceKeyDeps{Registry: f.reg, Credentials: f.creds, Gateways: accts, KeyPath: f.keyPath})
	if err != nil {
		t.Fatalf("newEvidenceKeyFn: %v", err)
	}
	return fn
}

func TestEvidenceKeyFnBindsIdentity(t *testing.T) {
	dir, reg, clk := evkEnv(t)
	f := &evkFixture{dir: dir, keyPath: filepath.Join(dir, "evidence.key"), reg: reg, clk: clk,
		creds: &evkCreds{vals: map[string]string{"alpha-ref": evkCanary}}}
	if err := reg.AddProvider(context.Background(), evkRecord("alpha", "https://a.example.invalid")); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	t.Run("registry", func(t *testing.T) { evkRegistryLeg(t, f) })
	t.Run("keyless", func(t *testing.T) { evkKeylessLeg(t, f) })
	t.Run("gateway", func(t *testing.T) { evkGatewayLeg(t, f) })
	t.Run("key file", func(t *testing.T) { evkKeyFileLeg(t, f) })
}

func evkPrelude(t *testing.T, f *evkFixture) (context.Context, string, *providerregistry.Registry, *evkClock, *evkCreds, string) {
	t.Helper()
	return context.Background(), f.dir, f.reg, f.clk, f.creds, f.keyPath
}

func evkRegistryLeg(t *testing.T, f *evkFixture) {
	ctx, _, reg, clk, creds, _ := evkPrelude(t, f)
	fn := f.newFn(t, nil)
	newFn := func(a []gateway.Account) topology.EvidenceKeyFn { return f.newFn(t, a) }
	base := evkKey(t, fn, "alpha")
	if again := evkKey(t, newFn(nil), "alpha"); again != base {
		t.Fatalf("unchanged provider: %+v != %+v", again, base)
	}
	if len(base.CredentialFingerprint) != 32 || base.AuthRef != "alpha-ref" || !strings.HasPrefix(base.InstanceID, "alpha@") {
		t.Fatalf("key shape %+v", base)
	}
	creds.vals["alpha-ref"] = evkCanary + "-rotated"
	if k := evkKey(t, fn, "alpha"); k.CredentialFingerprint == base.CredentialFingerprint {
		t.Fatal("vault value change kept the fingerprint")
	}
	creds.vals["alpha-ref"] = evkCanary
	if k := evkKey(t, fn, "alpha"); k != base {
		t.Fatalf("restored vault value: %+v != %+v", k, base)
	}
	for _, mut := range []func(*providerregistry.ProviderRecord){
		func(r *providerregistry.ProviderRecord) { r.BaseURL = "https://b.example.invalid" },
		func(r *providerregistry.ProviderRecord) { r.AuthRef = "alpha-ref2" },
	} {
		rec := evkRecord("alpha", "https://a.example.invalid")
		mut(&rec)
		creds.vals[rec.AuthRef.String()] = evkCanary
		if err := reg.UpsertProvider(ctx, rec); err != nil {
			t.Fatalf("UpsertProvider: %v", err)
		}
		if k := evkKey(t, fn, "alpha"); k == base {
			t.Fatalf("identity change kept the key %+v", k)
		}
	}
	if err := reg.DeleteProvider(ctx, "alpha"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	_, err := fn(ctx, "alpha")
	evkWantErr(t, err, cascade.KindNotFound, `provider "alpha"`)
	clk.now = clk.now.Add(time.Hour)
	if err := reg.AddProvider(ctx, evkRecord("alpha", "https://a.example.invalid")); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if k := evkKey(t, fn, "alpha"); k.InstanceID == base.InstanceID || k == base {
		t.Fatalf("new CreatedAt kept the key %+v", k)
	}
}

func evkKeylessLeg(t *testing.T, f *evkFixture) {
	ctx, _, reg, _, creds, _ := evkPrelude(t, f)
	fn := f.newFn(t, nil)
	rec := evkRecord("local", "http://127.0.0.1:11434")
	rec.Driver = providerregistry.DriverOllama
	if err := reg.AddProvider(ctx, rec); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	before := creds.calls
	if k := evkKey(t, fn, "local"); k.CredentialFingerprint != "none" {
		t.Fatalf("keyless fingerprint %q", k.CredentialFingerprint)
	}
	if creds.calls != before {
		t.Fatal("keyless provider read the vault")
	}
}

func evkGatewayLeg(t *testing.T, f *evkFixture) {
	ctx, dir, reg, _, creds, _ := evkPrelude(t, f)
	newFn := func(a []gateway.Account) topology.EvidenceKeyFn { return f.newFn(t, a) }
	acct := gateway.Account{Name: "gw1", BaseURL: "https://gw.example.invalid", Header: gateway.HeaderBearer,
		Credential: gateway.Credential{Kind: gateway.CredentialHelper,
			HelperPath: filepath.Join(dir, "no-such-helper"), HelperSHA256: strings.Repeat("a", 64)}}
	rec := evkRecord("gwp", acct.BaseURL)
	rec.Auth, rec.AuthRef = providerregistry.AuthGateway, "gateway:gw1"
	if err := reg.AddProvider(ctx, rec); err != nil {
		t.Fatalf("AddProvider: %v", err)
	}
	before := creds.calls
	base := evkKey(t, newFn([]gateway.Account{acct}), "gwp")
	if len(base.CredentialFingerprint) != 32 {
		t.Fatalf("gateway key %+v", base)
	}
	sha := acct
	sha.Credential.HelperSHA256 = strings.Repeat("b", 64)
	if k := evkKey(t, newFn([]gateway.Account{sha}), "gwp"); k == base {
		t.Fatal("HelperSHA256 change kept the key")
	}
	moved := acct
	moved.BaseURL = "https://gw2.example.invalid"
	rec.BaseURL = moved.BaseURL
	if err := reg.UpsertProvider(ctx, rec); err != nil {
		t.Fatalf("UpsertProvider: %v", err)
	}
	if k := evkKey(t, newFn([]gateway.Account{moved}), "gwp"); k == base {
		t.Fatal("BaseURL change kept the key")
	}
	_, err := newFn(nil)(ctx, "gwp")
	evkWantErr(t, err, cascade.KindPermissionDenied, "gateway account gw1 is not admitted")
	if creds.calls != before {
		t.Fatalf("gateway key read the vault %d times", creds.calls-before)
	}
	if _, err := os.Stat(acct.Credential.HelperPath); !os.IsNotExist(err) {
		t.Fatal("helper path touched")
	}
}

func evkKeyFileLeg(t *testing.T, f *evkFixture) {
	_, dir, reg, _, creds, keyPath := evkPrelude(t, f)
	if info, err := os.Stat(keyPath); err != nil || (goruntime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("created key file: %v %v", info, err)
	}
	deps := evidenceKeyDeps{Registry: reg, Credentials: creds}
	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(short, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps.KeyPath = short
	_, err := newEvidenceKeyFn(deps)
	evkWantErr(t, err, cascade.KindIntegrity, "holds 3 bytes, want 32")
	deps.KeyPath = t.TempDir()
	_, err = newEvidenceKeyFn(deps)
	evkWantErr(t, err, cascade.KindIntegrity, "is not a regular file")
	if goruntime.GOOS == "windows" {
		return
	}
	wide := filepath.Join(dir, "wide.key")
	if err := os.WriteFile(wide, make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wide, 0o644); err != nil {
		t.Fatal(err)
	}
	deps.KeyPath = wide
	_, err = newEvidenceKeyFn(deps)
	evkWantErr(t, err, cascade.KindIntegrity, "want 0600")
}
