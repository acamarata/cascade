// Purpose: FuzzIntentResolve fuzzes the intent-string argument to
//
//	Resolve against a fixed, real installed set and a fixed, real verified
//	index (06-FORGE-SPEC.md §5.7 — FuzzXxx required for a ticket adding a
//	parser/decoder; Resolve's string-matching/ranking logic is this
//	ticket's decoder-shaped surface). Seed corpus at
//	testdata/fuzz/FuzzIntentResolve/ (R-21.266 — package-local, one
//	package per fuzz check).
//
// Constraints: no network calls; must never panic for any input, and must
//
//	uphold the fail-closed invariant (a non-nil error never accompanies a
//	non-empty candidate slice) for arbitrary byte input.
//
// SPORT: internal/plugins/resolver fuzz tests (ADD) — P1-E24-W5-S50-T3.
package resolver

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/pkg/plugin"
)

// FuzzIntentResolve fuzzes only the intent string: the installed set and
// registry index are fixed, real fixtures (loaded once, outside the fuzz
// closure) rather than fuzzed bytes, since mutating THEM would fuzz
// plugin.ParseManifest/Ed25519Verifier.VerifyIndex, which already have
// their own dedicated fuzz targets in pkg/plugin.
func FuzzIntentResolve(f *testing.F) {
	f.Add("github:push")
	f.Add("")
	f.Add("PLAN-PHASE")
	f.Add("quality")
	f.Add("example")
	f.Add("\x00\xff invalid utf8 \xc3\x28")
	f.Add("plan-phase build-ticket")

	installed := plugin.ManifestSet{
		{Manifest: loadManifest(f, "example-pbd.toml"), Enabled: true},
		{Manifest: loadManifest(f, "example-connector.toml"), Enabled: true},
	}
	idx := loadVerifiedIndex(f)
	r := NewIntentResolver()
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, intent string) {
		got, err := r.Resolve(ctx, installed, idx, intent)

		if err == nil {
			if len(got) == 0 {
				t.Fatalf("Resolve(%q) returned nil error with no candidate", intent)
			}
			if got[0].PluginID == "" {
				t.Fatalf("Resolve(%q) returned a winner with an empty PluginID: %+v", intent, got[0])
			}
			return
		}

		// Fail-closed invariant: an error never carries candidates with it.
		if got != nil {
			t.Fatalf("Resolve(%q) returned error %v alongside %d candidate(s)", intent, err, len(got))
		}

		var ambiguous *plugin.AmbiguousIntent
		if errors.As(err, &ambiguous) {
			if ambiguous.Tied < 2 || ambiguous.Tied > len(ambiguous.Candidates) {
				t.Fatalf("Resolve(%q) returned *AmbiguousIntent with Tied %d over %d candidate(s)",
					intent, ambiguous.Tied, len(ambiguous.Candidates))
			}
			return
		}

		// Identity comparison, never errors.Is: (*cascade.Error).Is
		// matches on Kind alone, so errors.Is would accept any integrity
		// or not-found error as one of these three.
		if err != plugin.ErrIntentEmpty && err != plugin.ErrIntentNotFound && err != plugin.ErrUnverifiedIndex {
			t.Fatalf("Resolve(%q) returned unrecognized error %v (type %T)", intent, err, err)
		}
	})
}
