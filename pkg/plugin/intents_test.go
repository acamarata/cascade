// Purpose: unit tests plus the runnable godoc Example for intents.go's
//
//	public Resolver contract, its data types, and the VerifiedIndex witness
//	(12-QUALITY-CONSTITUTION.md Art.10 §4/§6 — every pkg/ entry point needs
//	a compiling, doc-visible Example). The real algorithm lives in
//	internal/plugins/resolver, which this pkg/ package cannot import (pkg/
//	never imports internal/), so ExampleResolver_Resolve uses a minimal
//	local stand-in implementing plugin.Resolver directly — the same pattern
//	registry_example_test.go's exampleFetcher establishes for
//	RegistryFetcher.
//
// Constraints: the fixture bytes and public key come from
//
//	registry_client_test.go's realFixtureBytes/realVerifier helpers (same
//	test package), so every witness minted here passes the real production
//	verification (Art.2).
//
// SPORT: pkg/plugin intents tests (ADD) — P1-E24-W5-S50-T3.
package plugin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// exampleResolver is a minimal plugin.Resolver: an installed-first-only
// stand-in for internal/plugins/resolver.NewIntentResolver's real
// three-step algorithm, sufficient to demonstrate the interface's calling
// convention without internal/ access.
type exampleResolver struct{}

func (exampleResolver) Resolve(ctx context.Context, installed plugin.ManifestSet, _ *plugin.VerifiedIndex, intent string) ([]plugin.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindCanceled, err, "example: canceled")
	}
	for _, ip := range installed {
		for _, is := range ip.Manifest.Provides.Intents {
			if ip.Enabled && is.Name == intent {
				return []plugin.Candidate{{
					PluginID: ip.Manifest.ID,
					Source:   plugin.CandidateSourceInstalled,
				}}, nil
			}
		}
	}
	return nil, plugin.ErrIntentNotFound
}

// ExampleResolver_Resolve resolves an intent against one enabled
// installed plugin. No verified registry index is needed on the
// installed-first path, so the witness argument is nil.
func ExampleResolver_Resolve() {
	installed := plugin.ManifestSet{
		{
			Manifest: plugin.Manifest{
				ID:       "cascade-pbd",
				Provides: plugin.Provides{Intents: []plugin.IntentSpec{{Name: "plan-phase"}}},
			},
			Enabled: true,
		},
	}

	var r plugin.Resolver = exampleResolver{}
	cands, err := r.Resolve(context.Background(), installed, nil, "plan-phase")
	if err != nil {
		fmt.Println("resolve error:", err)
		return
	}
	fmt.Println(cands[0].PluginID)
	// Output: cascade-pbd
}

// TestNewVerifiedIndex_RealFixture proves the only constructor accepts the
// real signed fixture and hands back its entries.
func TestNewVerifiedIndex_RealFixture(t *testing.T) {
	idx, err := plugin.NewVerifiedIndex(context.Background(), realVerifier(t), realFixtureBytes(t))
	if err != nil {
		t.Fatalf("NewVerifiedIndex(real fixture) error = %v, want nil", err)
	}
	entries := idx.Entries()
	if len(entries) == 0 {
		t.Fatal("Entries() is empty, want the fixture's published entries")
	}

	// Entries returns a copy: mutating it must not reach the witness.
	first := entries[0].ID
	entries[0].ID = "mutated-by-caller"
	if again := idx.Entries(); again[0].ID != first {
		t.Errorf("Entries()[0].ID = %q after a caller mutated its copy, want %q", again[0].ID, first)
	}
}

// TestNewVerifiedIndex_Refusals proves a caller cannot mint a witness
// around the signature check. The poison document is the exact shape a
// hand-built "index" takes: an unsupported schema version, a signature
// that is not base64, and an entry advertising a high-privilege tag.
func TestNewVerifiedIndex_Refusals(t *testing.T) {
	poison, err := json.Marshal(plugin.RegistryIndex{
		SchemaVersion: "99-not-supported",
		Signature:     "!!!not-base64-at-all!!!",
		Entries:       []plugin.RegistryIndexEntry{{ID: "evil-plugin", Tags: []string{"deploy-prod"}}},
	})
	if err != nil {
		t.Fatalf("marshal poison index: %v", err)
	}
	tampered := bytes.Replace(realFixtureBytes(t), []byte("example-linter"), []byte("example-lintor"), 1)

	cases := []struct {
		name string
		data []byte
		kind cascade.Kind
	}{
		{"poison document", poison, cascade.KindInvalidInput},
		{"empty document", []byte(""), cascade.KindInvalidInput},
		{"entries edited after signing", tampered, cascade.KindIntegrity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, err := plugin.NewVerifiedIndex(context.Background(), realVerifier(t), c.data)
			if err == nil {
				t.Fatal("NewVerifiedIndex accepted the document, want refusal")
			}
			if idx != nil {
				t.Fatalf("NewVerifiedIndex returned witness %+v with error %v, want nil", idx, err)
			}
			if kind, ok := cascade.KindOf(err); !ok || kind != c.kind {
				t.Errorf("cascade.KindOf(err) = %v, %v, want %v, true", kind, ok, c.kind)
			}
		})
	}
}

// TestVerifiedIndex_NilEntries pins the nil-receiver behaviour the doc
// comment promises: nil entries, not a panic.
func TestVerifiedIndex_NilEntries(t *testing.T) {
	var idx *plugin.VerifiedIndex
	if got := idx.Entries(); got != nil {
		t.Errorf("(*VerifiedIndex)(nil).Entries() = %+v, want nil", got)
	}
}

func TestAmbiguousIntent_Error(t *testing.T) {
	err := &plugin.AmbiguousIntent{
		Intent: "deploy",
		Candidates: []plugin.Candidate{
			{PluginID: "a"}, {PluginID: "b"}, {PluginID: "weaker"},
		},
		Tied: 2,
	}
	want := `plugin: ambiguous intent "deploy": 2 candidate(s) at top rank, 3 in all`
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestErrUnverifiedIndex_KindOnlyComparison pins BOTH halves of the
// sentinel's documented contract: its Kind, and the reason callers must
// compare it by identity — (*cascade.Error).Is matches on Kind alone, so
// errors.Is cannot tell it apart from the other KindIntegrity sentinels in
// this package. If that ever stops being true, this test goes red and the
// doc comment on ErrUnverifiedIndex needs rewriting.
func TestErrUnverifiedIndex_KindOnlyComparison(t *testing.T) {
	if plugin.ErrUnverifiedIndex.Kind != cascade.KindIntegrity {
		t.Errorf("ErrUnverifiedIndex.Kind = %v, want KindIntegrity", plugin.ErrUnverifiedIndex.Kind)
	}
	if !errors.Is(plugin.ErrSignatureInvalid, plugin.ErrUnverifiedIndex) {
		t.Error("errors.Is(ErrSignatureInvalid, ErrUnverifiedIndex) = false; the Kind-only caveat in the doc comment is now wrong")
	}
	if plugin.ErrSignatureInvalid == plugin.ErrUnverifiedIndex {
		t.Error("ErrSignatureInvalid and ErrUnverifiedIndex are the same value; identity comparison cannot distinguish them")
	}
}

func TestErrIntentNotFound_Kind(t *testing.T) {
	if plugin.ErrIntentNotFound.Kind != cascade.KindNotFound {
		t.Errorf("ErrIntentNotFound.Kind = %v, want KindNotFound", plugin.ErrIntentNotFound.Kind)
	}
}

// TestErrIntentEmpty_Kind pins the blank-intent refusal as caller-input
// invalidity rather than a lookup miss.
func TestErrIntentEmpty_Kind(t *testing.T) {
	if plugin.ErrIntentEmpty.Kind != cascade.KindInvalidInput {
		t.Errorf("ErrIntentEmpty.Kind = %v, want KindInvalidInput", plugin.ErrIntentEmpty.Kind)
	}
}

// TestCandidateSource_WireValues pins the two enum strings, which appear in
// serialized proposals a caller may persist or display.
func TestCandidateSource_WireValues(t *testing.T) {
	if plugin.CandidateSourceInstalled != "installed" {
		t.Errorf("CandidateSourceInstalled = %q, want %q", plugin.CandidateSourceInstalled, "installed")
	}
	if plugin.CandidateSourceRegistry != "registry" {
		t.Errorf("CandidateSourceRegistry = %q, want %q", plugin.CandidateSourceRegistry, "registry")
	}
}
