package runtime

// Purpose: proves Accept persists a signed record only when its proof
// verifies and reports every store failure, and that ApplyEffective and
// EffectiveConfigOf are exact inverses over the guarded keys only.
// Inputs: an in-memory store wrapped to fail Put on demand; synthetic configs.
// Outputs: none (tests).
// Constraints: no keychain.
// SPORT: runtime/boot-baseline-check tests (P1-SEC-04).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/cascade"
)

// fixedEpoch is the injected clock's instant.
var fixedEpoch = time.Unix(1_700_000_000, 0)

// failingPutStore fails the Put whose 1-based index is failAt.
type failingPutStore struct {
	*storetest.MemStore
	puts, failAt int
}

func (s *failingPutStore) Put(ctx context.Context, ns, key string, v []byte) error {
	s.puts++
	if s.puts == s.failAt {
		return errors.New("synthetic disk full")
	}
	return s.MemStore.Put(ctx, ns, key, v)
}

func TestBaselineAcceptReturnsStoreError(t *testing.T) {
	signer, pub := newTestSigner(t)
	// Puts in order: snapshot, audit record, v2 record.
	for _, failAt := range []int{1, 2, 3} {
		store := &failingPutStore{MemStore: storetest.NewMemStore(), failAt: failAt}
		bc := NewBaselineChecker(store, NewFixedClock(fixedEpoch), nil, NewStoreAuditRecorder(store, NewFixedClock(fixedEpoch)))
		bc.SetVerifier(trustedVerifier(pub))
		err := bc.Accept(context.Background(), looserThanFloor, signer.proof(t, fileDigestA))
		if err == nil {
			t.Fatalf("Put %d failed and Accept returned nil", failAt)
		}
		if kind, _ := cascade.KindOf(err); kind != cascade.KindUnavailable {
			t.Fatalf("Put %d failed: kind %v (%v), want unavailable", failAt, kind, err)
		}
	}
	// Positive control: every row persists and the record boots.
	store := storetest.NewMemStore()
	bc := NewBaselineChecker(store, NewFixedClock(fixedEpoch), nil, NewStoreAuditRecorder(store, NewFixedClock(fixedEpoch)))
	bc.SetVerifier(trustedVerifier(pub))
	if err := bc.Accept(context.Background(), looserThanFloor, signer.proof(t, fileDigestA)); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if res := bc.CheckFile(context.Background(), looserThanFloor, signedAs(fileDigestA, looserThanFloor)); res.Outcome != BaselineAccepted {
		t.Fatalf("accepted record boots as %s", res.Outcome.Detail())
	}
}

func TestBaselineAcceptRefusesBadProof(t *testing.T) {
	signer, pub := newTestSigner(t)
	other, _ := newTestSigner(t)
	cases := map[string]struct {
		v     BaselineVerifier
		proof BaselineProof
		kind  cascade.Kind
	}{
		"other key":    {trustedVerifier(pub), other.proof(t, fileDigestA), cascade.KindIntegrity},
		"no verifier":  {nil, signer.proof(t, fileDigestA), cascade.KindIntegrity},
		"other method": {trustedVerifier(pub), BaselineProof{Method: "vault.get", RawParams: signer.proof(t, fileDigestA).RawParams, Attestation: signer.proof(t, fileDigestA).Attestation}, cascade.KindIntegrity},
		"bad params":   {trustedVerifier(pub), BaselineProof{Method: AcceptLooseningMethod, RawParams: []byte(`{}`)}, cascade.KindInvalidInput},
	}
	for name, c := range cases {
		store := storetest.NewMemStore()
		bc := NewBaselineChecker(store, NewFixedClock(fixedEpoch), nil, nil)
		bc.SetVerifier(c.v)
		err := bc.Accept(context.Background(), looserThanFloor, c.proof)
		if kind, _ := cascade.KindOf(err); err == nil || kind != c.kind {
			t.Errorf("%s: err %v, want kind %v", name, err, c.kind)
		}
		if _, gerr := store.Get(context.Background(), auditNamespace, baselineRecordKey); gerr == nil {
			t.Errorf("%s: a record was written", name)
		}
	}
}

func guardedFixture() *Config {
	return &Config{Extra: map[string]interface{}{
		"policy":    map[string]interface{}{"autonomy_profile": "wide", "note": "keep"},
		"secrets":   map[string]interface{}{"keychain_backend": "file"},
		"sync":      map[string]interface{}{"memory": "server-primary", "interval": int64(30)},
		"nodes":     map[string]interface{}{"trust_tier": "worker-trusted", "max": int64(4)},
		"conductor": map[string]interface{}{"external_routing_enabled": true, "spill_enabled": true, "lanes": int64(3)},
		"memory":    map[string]interface{}{"budget": int64(9)},
	}, Elevation: elevationSection{AllowRemote: true, HelperPubkey: "synthetic-key"}}
}

func TestApplyEffectiveOverwritesOnlyGuardedSections(t *testing.T) {
	cfg := guardedFixture()
	original := guardedFixture()
	shared := cfg.Extra
	ApplyEffective(cfg, MostRestrictiveDefaults())

	if got := EffectiveConfigOf(cfg); ComputeSectionsHash(got) != ComputeSectionsHash(MostRestrictiveDefaults()) {
		t.Fatalf("after ApplyEffective the guarded values are %+v, want the defaults", got)
	}
	unmodeled := map[[2]string]interface{}{
		{"policy", "note"}: "keep", {"sync", "interval"}: int64(30), {"nodes", "max"}: int64(4),
		{"conductor", "lanes"}: int64(3), {"memory", "budget"}: int64(9),
	}
	for k, want := range unmodeled {
		if got := sectionAt(cfg.Extra, k[0])[k[1]]; got != want {
			t.Errorf("%s.%s = %v, want %v untouched", k[0], k[1], got, want)
		}
	}
	if v, ok := sectionAt(cfg.Extra, "conductor")["external_routing_enabled"].(bool); !ok || v {
		t.Fatal("conductor.external_routing_enabled is not written as an explicit false")
	}
	if !reflect.DeepEqual(shared, original.Extra) {
		t.Fatal("ApplyEffective mutated the map the caller shared")
	}
}

func TestEffectiveConfigOfMatchesExtract(t *testing.T) {
	for _, cfg := range []*Config{nil, {}, guardedFixture()} {
		if !reflect.DeepEqual(EffectiveConfigOf(cfg), extractEffectiveConfig(cfg)) {
			t.Fatalf("EffectiveConfigOf differs from the extraction for %+v", cfg)
		}
	}
	// Round trip: applying any extraction and re-extracting is the identity.
	want := extractEffectiveConfig(guardedFixture())
	cfg := &Config{}
	ApplyEffective(cfg, want)
	got, _ := json.Marshal(EffectiveConfigOf(cfg))
	exp, _ := json.Marshal(want)
	if string(got) != string(exp) {
		t.Fatalf("round trip = %s, want %s", got, exp)
	}
}

func TestRestrictiveFallbackNeverLooserThanConfig(t *testing.T) {
	configs := []EffectiveConfig{
		{},
		extractEffectiveConfig(guardedFixture()),
		{Policy: PolicySection{AutonomyProfile: "strict"}, Sync: SyncSection{Classes: map[string]string{"memory": "local-only", "config/config": "server-primary"}}},
	}
	for _, onDisk := range configs {
		fb := RestrictiveFallback(onDisk)
		// policy: an absent profile resolves to "balanced"; the fallback names strict.
		if fb.Policy.AutonomyProfile != "strict" {
			t.Fatalf("autonomy_profile fallback %q, want strict", fb.Policy.AutonomyProfile)
		}
		// sync: every domain the config names is local-only (rank 0); none is dropped
		// to its compiled class, which may be looser than the config's own value.
		for domain := range onDisk.Sync.Classes {
			if fb.Sync.Classes[domain] != "local-only" {
				t.Fatalf("sync %s fallback %q, want local-only", domain, fb.Sync.Classes[domain])
			}
		}
		if len(fb.Sync.Classes) != len(onDisk.Sync.Classes) {
			t.Fatalf("fallback names domains the config does not: %v", fb.Sync.Classes)
		}
		// booleans: false is the tightest value and is written explicitly.
		if fb.Conductor.ExternalRoutingEnabled || fb.Conductor.SpillEnabled || fb.Elevation.AllowRemote {
			t.Fatalf("a boolean fallback is enabled: %+v", fb)
		}
	}
	// A degraded boot of an explicit strict config stays strict end to end.
	cfg := &Config{Extra: map[string]interface{}{"policy": map[string]interface{}{"autonomy_profile": "strict"}}}
	ApplyEffective(cfg, RestrictiveFallback(EffectiveConfigOf(cfg)))
	if got := stringAt(cfg.Extra, "policy", "autonomy_profile"); got != "strict" {
		t.Fatalf("degraded strict config runs autonomy_profile %q", got)
	}
}

func writeTestConfig(t *testing.T, path, body string, perm os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatal(err)
	}
}

func emptyEnvOpts(path string) LoadOptions {
	return LoadOptions{Path: path, Getenv: func(string) string { return "" }, Environ: func() []string { return nil }}
}

func TestConfigFileParseNeverRereads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestConfig(t, path, "[conductor]\nexternal_routing_enabled = false\n", 0o600)
	f, err := ReadConfigFile(path)
	if err != nil || !f.Found {
		t.Fatalf("read: %v %v", f.Found, err)
	}
	// Swapped after the read (and after any digest of it).
	writeTestConfig(t, path, "[conductor]\nexternal_routing_enabled = true\n", 0o600)
	cfg, err := f.Parse(context.Background(), emptyEnvOpts(path))
	if err != nil {
		t.Fatal(err)
	}
	if EffectiveConfigOf(cfg).Conductor.ExternalRoutingEnabled {
		t.Fatal("Parse reread the swapped file; it must parse the bytes that were read")
	}
	if f.Digest() != ConfigDigest([]byte("[conductor]\nexternal_routing_enabled = false\n")) {
		t.Fatal("Digest is not the digest of the bytes read")
	}
}

func TestConfigFileParseMatchesLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestConfig(t, path, "[conductor]\nspill_enabled = true\n[policy]\nautonomy_profile = \"strict\"\n", 0o600)
	opts := emptyEnvOpts(path)
	opts.Environ = func() []string { return []string{"CASCADE_CONDUCTOR__EXTERNAL_ROUTING_ENABLED=true"} }
	loaded, err := Load(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	f, err := ReadConfigFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := f.Parse(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, parsed) {
		t.Fatalf("Parse differs from Load:\n%+v\n%+v", parsed, loaded)
	}
	missing, err := ReadConfigFile(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil || missing.Found {
		t.Fatalf("absent file: found=%v err=%v", missing.Found, err)
	}
	if _, err := missing.Parse(context.Background(), opts); err != nil {
		t.Fatalf("absent file parse: %v", err)
	}
	writeTestConfig(t, path, "[conductor\n", 0o600)
	bad, _ := ReadConfigFile(path)
	if _, err := bad.Parse(context.Background(), opts); err == nil {
		t.Fatal("malformed TOML parsed")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Parse(cancelled, opts); err == nil {
		t.Fatal("cancelled context parsed")
	}
}

func TestReadConfigFileRefusesWorldWritable(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("windows reports no unix permission bits, so a 0666 file cannot be told from any other")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	writeTestConfig(t, path, "[conductor]\n", 0o666)
	_, err := ReadConfigFile(path)
	if kind, _ := cascade.KindOf(err); err == nil || kind != cascade.KindPermissionDenied {
		t.Fatalf("world-writable config: err %v, want permission_denied", err)
	}
}
