// Purpose: unit tests for intentResolver.Resolve, white-box (package
//
//	resolver) so the unexported ranking tiers can be asserted directly
//	alongside the public Resolve entry point, against real fixtures — see
//	this package's testdata/README.md for fixture provenance.
//
// Constraints: no network calls; fixtures under testdata/fixtures/ are
//
//	byte-identical copies of already-landed pkg/plugin real-counterpart
//	fixtures (Art.2), and the verified index is re-derived through the real
//	production verifier rather than hand-built.
//
// SPORT: internal/plugins/resolver tests (ADD) — P1-E24-W5-S50-T3.
package resolver

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// testRegistryPublicKeyB64 is the same real Ed25519 public key
// pkg/plugin/registry_client_test.go's testRegistryPublicKeyB64 constant
// holds — the key whose matching private key signed
// pkg/plugin/testdata/registry/index.json, copied here as
// testdata/fixtures/registry-index.json (testdata/README.md). Duplicated
// rather than imported: it lives in a _test.go file in another package,
// unexported.
const testRegistryPublicKeyB64 = "ANBaHR6iUTltVXr71FiLPG2Z2+uXL+0QoyVi6ibc3Po="

func loadManifest(t testing.TB, filename string) plugin.Manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", filename))
	if err != nil {
		t.Fatalf("read fixture %s: %v", filename, err)
	}
	m, err := plugin.ParseManifest(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("parse fixture %s: %v", filename, err)
	}
	return m
}

// fixtureVerifier is the real production verifier, keyed with the real
// public key the landed fixture was signed under.
func fixtureVerifier(t testing.TB) plugin.Ed25519Verifier {
	t.Helper()
	pub, err := base64.StdEncoding.DecodeString(testRegistryPublicKeyB64)
	if err != nil {
		t.Fatalf("decode test public key: %v", err)
	}
	return plugin.Ed25519Verifier{PublicKey: ed25519.PublicKey(pub)}
}

// loadVerifiedIndex mints the witness from the real signed fixture through
// the real production verifier — the only way a *plugin.VerifiedIndex can
// come into existence, and the same verification RegistryClient.Fetch runs
// (Art.2, testdata/README.md).
func loadVerifiedIndex(t testing.TB) *plugin.VerifiedIndex {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "fixtures", "registry-index.json"))
	if err != nil {
		t.Fatalf("read registry fixture: %v", err)
	}
	idx, err := plugin.NewVerifiedIndex(context.Background(), fixtureVerifier(t), data)
	if err != nil {
		t.Fatalf("verify registry fixture: %v", err)
	}
	return idx
}

func TestInstalledFirstLookup(t *testing.T) {
	pbd := plugin.InstalledPlugin{Manifest: loadManifest(t, "example-pbd.toml"), Enabled: true}
	connector := plugin.InstalledPlugin{Manifest: loadManifest(t, "example-connector.toml"), Enabled: true}
	installed := plugin.ManifestSet{pbd, connector}

	r := NewIntentResolver()
	ctx := context.Background()
	// A nil witness: if Resolve ever consulted the registry on an
	// installed-match path, the test would fail with ErrUnverifiedIndex
	// instead of returning the installed Candidate.
	got, err := r.Resolve(ctx, installed, nil, "plan-phase")
	if err != nil {
		t.Fatalf("Resolve(plan-phase) error = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Source != plugin.CandidateSourceInstalled || got[0].PluginID != "cascade-pbd" {
		t.Errorf("Resolve(plan-phase) = %+v, want exactly one installed cascade-pbd", got)
	}

	t.Run("case-insensitive", func(t *testing.T) {
		got, err := r.Resolve(ctx, installed, nil, "PLAN-Phase")
		if err != nil || len(got) != 1 || got[0].PluginID != "cascade-pbd" {
			t.Errorf("Resolve(PLAN-Phase) = %+v, %v, want cascade-pbd, nil", got, err)
		}
	})

	t.Run("second plugin's own intent", func(t *testing.T) {
		got, err := r.Resolve(ctx, installed, nil, "connect-external-source")
		if err != nil || len(got) != 1 || got[0].PluginID != "example-connector" {
			t.Errorf("Resolve(connect-external-source) = %+v, %v, want example-connector, nil", got, err)
		}
	})
}

// TestInstalledFirstLookup_Disabled and TestInstalledFirstLookup_Ambiguous
// are split from TestInstalledFirstLookup (Art.10.3 funlen: 50 physical
// lines including t.Run closures) but share its "TestInstalledFirstLookup"
// name prefix, so the ticket's `-run TestInstalledFirstLookup` check (an
// unanchored regexp match) still selects them.
func TestInstalledFirstLookup_Disabled(t *testing.T) {
	pbd := plugin.InstalledPlugin{Manifest: loadManifest(t, "example-pbd.toml"), Enabled: false}
	got, err := NewIntentResolver().Resolve(context.Background(), plugin.ManifestSet{pbd}, nil, "plan-phase")
	if err != plugin.ErrUnverifiedIndex {
		t.Errorf("Resolve(plan-phase, disabled) error = %v, want ErrUnverifiedIndex (falls through to a nil witness)", err)
	}
	if got != nil {
		t.Errorf("Resolve(plan-phase, disabled) candidates = %+v, want nil", got)
	}
}

func TestInstalledFirstLookup_Ambiguous(t *testing.T) {
	pbd := loadManifest(t, "example-pbd.toml")
	twice := plugin.ManifestSet{
		{Manifest: pbd, Enabled: true},
		{Manifest: plugin.Manifest{ID: "cascade-pbd-fork", Name: "Fork", Provides: pbd.Provides}, Enabled: true},
	}
	got, err := NewIntentResolver().Resolve(context.Background(), twice, nil, "plan-phase")
	var ambiguous *plugin.AmbiguousIntent
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve(plan-phase, twice) error = %v, want *AmbiguousIntent", err)
	}
	if got != nil {
		t.Errorf("Resolve returned %+v alongside the ambiguity, want nil candidates", got)
	}
	if len(ambiguous.Candidates) != 2 || ambiguous.Tied != 2 {
		t.Errorf("AmbiguousIntent = %d candidate(s), Tied %d, want 2 and 2", len(ambiguous.Candidates), ambiguous.Tied)
	}
}

func TestFailClosedNilIndex(t *testing.T) {
	got, err := NewIntentResolver().Resolve(context.Background(), nil, nil, "no-such-intent")
	if err != plugin.ErrUnverifiedIndex {
		t.Fatalf("Resolve(nil witness) error = %v, want ErrUnverifiedIndex (identity check, not Kind-only)", err)
	}
	if got != nil {
		t.Errorf("Resolve(nil witness) candidates = %+v, want nil (no partial result)", got)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindIntegrity {
		t.Errorf("cascade.KindOf(err) = %v, %v, want KindIntegrity, true", kind, ok)
	}
}

// TestFailClosedUnverifiedIndex proves the structural hole is closed: the
// poison document below (unsupported schema_version, a signature that is
// not even base64, one entry advertising a "deploy-prod" tag) cannot be
// turned into a *plugin.VerifiedIndex by the real verifier, so it can
// never reach the ranking step — and with no witness, Resolve refuses.
func TestFailClosedUnverifiedIndex(t *testing.T) {
	poison, err := json.Marshal(plugin.RegistryIndex{
		SchemaVersion: "99-not-supported",
		Signature:     "!!!not-base64-at-all!!!",
		Entries: []plugin.RegistryIndexEntry{
			{ID: "evil-plugin", Name: "Evil", Tags: []string{"deploy-prod"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal poison index: %v", err)
	}

	witness, err := plugin.NewVerifiedIndex(context.Background(), fixtureVerifier(t), poison)
	if err == nil {
		t.Fatalf("NewVerifiedIndex accepted the poison document, want refusal")
	}
	if witness != nil {
		t.Fatalf("NewVerifiedIndex returned witness %+v alongside error %v, want nil", witness, err)
	}

	got, resolveErr := NewIntentResolver().Resolve(context.Background(), nil, witness, "deploy-prod")
	if resolveErr != plugin.ErrUnverifiedIndex {
		t.Fatalf("Resolve(poison) error = %v, want ErrUnverifiedIndex", resolveErr)
	}
	if got != nil {
		t.Fatalf("Resolve(poison) yielded candidates %+v, want none", got)
	}
}

func TestResolve_NoMatchAndEmptyIntent(t *testing.T) {
	idx := loadVerifiedIndex(t)
	r := NewIntentResolver()
	ctx := context.Background()

	t.Run("no match anywhere", func(t *testing.T) {
		if _, err := r.Resolve(ctx, nil, idx, "totally-unknown-xyz"); err != plugin.ErrIntentNotFound {
			t.Errorf("Resolve(unknown) error = %v, want ErrIntentNotFound", err)
		}
	})

	t.Run("blank intent is invalid input", func(t *testing.T) {
		_, err := r.Resolve(ctx, nil, idx, "   ")
		if err != plugin.ErrIntentEmpty {
			t.Fatalf("Resolve(blank) error = %v, want ErrIntentEmpty", err)
		}
		if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindInvalidInput {
			t.Errorf("cascade.KindOf(err) = %v, %v, want KindInvalidInput, true", kind, ok)
		}
	})
}

func TestResolve_ContextCanceled(t *testing.T) {
	idx := loadVerifiedIndex(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := NewIntentResolver().Resolve(ctx, nil, idx, "quality")
	if got != nil {
		t.Errorf("Resolve(canceled) candidates = %+v, want nil", got)
	}
	if kind, ok := cascade.KindOf(err); !ok || kind != cascade.KindCanceled {
		t.Fatalf("cascade.KindOf(err) = %v, %v (err %v), want KindCanceled, true", kind, ok, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false, want the cause preserved")
	}
}
