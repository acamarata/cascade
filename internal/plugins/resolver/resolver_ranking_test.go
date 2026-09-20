// Purpose: the registry-lookup ranking tests — tier assignment, the
//
//	exact-beats-prefix winner, the tie that becomes an AmbiguousIntent, and
//	the deliberate absence of id-equality matching. Split from
//	resolver_test.go to keep both files inside the 300-line cap.
//
// Constraints: no network calls. Two index sources are used, both passing
//
//	through the real production verifier: the landed real fixture
//	(testdata/fixtures/registry-index.json) wherever it can express the
//	case, and a document signed in-test with a freshly generated Ed25519
//	key for the one case the landed fixture cannot express (see
//	testdata/README.md § signed-in-test index).
//
// SPORT: internal/plugins/resolver tests (ADD) — P1-E24-W5-S50-T3.
package resolver

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// signedEnvelope is the registry index document's wire shape, exactly as
// pkg/plugin/registry_verify.go splits it: schema_version, the raw entries
// bytes the signature is computed over, and the detached signature.
type signedEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	Entries       json.RawMessage `json:"entries"`
	Signature     string          `json:"signature"`
}

// signIndex builds a real signed index document over entries with a
// freshly generated Ed25519 key and mints the witness from it through the
// real production verifier. Nothing here weakens verification: the bytes
// are signed the way the registry signs them, and plugin.NewVerifiedIndex
// performs the same check it performs on the landed fixture.
func signIndex(t testing.TB, entries []plugin.RegistryIndexEntry) *plugin.VerifiedIndex {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("marshal entries: %v", err)
	}
	// The signature covers schema_version+entries only, never the
	// signature field the envelope adds below.
	payload, err := json.Marshal(struct {
		SchemaVersion string          `json:"schema_version"`
		Entries       json.RawMessage `json:"entries"`
	}{SchemaVersion: plugin.SchemaVersionCurrent, Entries: raw})
	if err != nil {
		t.Fatalf("marshal signed payload: %v", err)
	}
	doc, err := json.Marshal(signedEnvelope{
		SchemaVersion: plugin.SchemaVersionCurrent,
		Entries:       raw,
		Signature:     base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)),
	})
	if err != nil {
		t.Fatalf("marshal index document: %v", err)
	}
	idx, err := plugin.NewVerifiedIndex(context.Background(), plugin.Ed25519Verifier{PublicKey: pub}, doc)
	if err != nil {
		t.Fatalf("verify signed-in-test index: %v", err)
	}
	return idx
}

func TestRegistryLookupRanking(t *testing.T) {
	idx := loadVerifiedIndex(t)
	entries := idx.Entries()
	r := NewIntentResolver()

	tests := []struct {
		name     string
		intent   string
		wantID   string
		wantTier rank
	}{
		{"exact tag match", "quality", "example-linter", rankExactTag},
		// "lint" is a prefix of the tag "linter", so it lands one tier
		// below an exact tag hit.
		{"tag prefix match", "lint", "example-linter", rankTagPrefix},
		// "lints" is a substring of example-linter's description ("Lints
		// example files...") and neither an exact tag nor a tag prefix, so
		// it only reaches the keyword tier.
		{"keyword description match", "lints", "example-linter", rankKeyword},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Resolve(context.Background(), nil, idx, tt.intent)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v, want nil", tt.intent, err)
			}
			if len(got) != 1 || got[0].PluginID != tt.wantID {
				t.Fatalf("Resolve(%q) = %+v, want exactly %s", tt.intent, got, tt.wantID)
			}
			if got[0].Source != plugin.CandidateSourceRegistry {
				t.Errorf("Resolve(%q).Source = %v, want CandidateSourceRegistry", tt.intent, got[0].Source)
			}
			assertTier(t, entries, tt.wantID, tt.intent, tt.wantTier)
		})
	}
}

// assertTier pins which tier the winning entry actually matched at, which
// Resolve's own result cannot show (the tier is deliberately absent from
// plugin.Candidate).
func assertTier(t *testing.T, entries []plugin.RegistryIndexEntry, id, intent string, want rank) {
	t.Helper()
	for _, e := range entries {
		if e.ID != id {
			continue
		}
		got, ok := registryTier(intent, e)
		if !ok || got != want {
			t.Errorf("registryTier(%q, %s) = %d, %v, want %d, true", intent, id, got, ok, want)
		}
		return
	}
	t.Fatalf("entry %s not present in the verified index", id)
}

// TestRegistryLookupRanking_ExactBeatsPrefix is the case the landed fixture
// cannot express: two entries matching one intent at DIFFERENT tiers. The
// exact-tag entry must win outright, and a stronger tier must not be
// reported as a tie.
func TestRegistryLookupRanking_ExactBeatsPrefix(t *testing.T) {
	// The exact-tag entry sorts LAST alphabetically on purpose: with ids
	// that agreed with tier order, deleting the tier clause and leaving
	// only the id tie-break would keep this test green. The confirming
	// review caught that; the ids are chosen so only the tier can win.
	idx := signIndex(t, []plugin.RegistryIndexEntry{
		{ID: "aa-prefix-only", Name: "Prefix Only", Tags: []string{"deployment"}},
		{ID: "zz-exact-tag", Name: "Exact Tag", Tags: []string{"deploy"}},
	})

	got, err := NewIntentResolver().Resolve(context.Background(), nil, idx, "deploy")
	if err != nil {
		// Includes *AmbiguousIntent: a stronger tier reported as a tie
		// would arrive here, so this one check covers that case too.
		t.Fatalf("Resolve(deploy) error = %v, want nil (one strongest candidate, not an ambiguity)", err)
	}
	if len(got) != 2 {
		t.Fatalf("Resolve(deploy) returned %d candidate(s), want 2 (the weaker match is kept, never dropped)", len(got))
	}
	if got[0].PluginID != "zz-exact-tag" {
		t.Errorf("Resolve(deploy)[0] = %s, want zz-exact-tag (exact tag outranks tag prefix, and outranks id order)", got[0].PluginID)
	}
	if got[1].PluginID != "aa-prefix-only" {
		t.Errorf("Resolve(deploy)[1] = %s, want aa-prefix-only", got[1].PluginID)
	}
}

// TestRegistryLookupRanking_IDIsNotRanked pins the removal of id-equality
// matching: an intent string equal to an entry's id, with nothing in that
// entry's tags, name, or description matching it, resolves to nothing. An
// id is a name, not a declaration that the plugin provides that intent.
func TestRegistryLookupRanking_IDIsNotRanked(t *testing.T) {
	idx := signIndex(t, []plugin.RegistryIndexEntry{
		{ID: "github-push", Name: "Pusher", Description: "Sends commits upstream", Tags: []string{"vcs"}},
	})

	got, err := NewIntentResolver().Resolve(context.Background(), nil, idx, "github-push")
	if err != plugin.ErrIntentNotFound {
		t.Fatalf("Resolve(github-push) error = %v, want ErrIntentNotFound (id equality is not a tier)", err)
	}
	if got != nil {
		t.Errorf("Resolve(github-push) = %+v, want no candidate", got)
	}
}

func TestAmbiguousIntent(t *testing.T) {
	idx := loadVerifiedIndex(t)

	got, err := NewIntentResolver().Resolve(context.Background(), nil, idx, "example")
	var ambiguous *plugin.AmbiguousIntent
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve(example) error = %v, want *AmbiguousIntent", err)
	}
	if got != nil {
		t.Errorf("Resolve(example) returned %+v alongside the ambiguity, want nil", got)
	}
	if ambiguous.Intent != "example" {
		t.Errorf("AmbiguousIntent.Intent = %q, want %q", ambiguous.Intent, "example")
	}
	if len(ambiguous.Candidates) != 2 {
		t.Fatalf("AmbiguousIntent.Candidates has %d entries, want 2 (no candidate silently dropped)", len(ambiguous.Candidates))
	}
	if ambiguous.Tied != 2 {
		t.Errorf("AmbiguousIntent.Tied = %d, want 2 (both entries carry the exact tag \"example\")", ambiguous.Tied)
	}
	// Candidates[:Tied] is the tied group, and every member of it must sit
	// at the same, strongest tier — here rankExactTag, since both fixture
	// entries publish the tag "example".
	for _, c := range ambiguous.Candidates[:ambiguous.Tied] {
		tier, ok := registryTier("example", c.RegistryEntry)
		if !ok || tier != rankExactTag {
			t.Errorf("tied candidate %s matched at tier %d (ok %v), want %d", c.PluginID, tier, ok, rankExactTag)
		}
	}
}
