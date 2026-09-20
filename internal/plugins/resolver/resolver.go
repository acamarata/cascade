// Package resolver implements pkg/plugin.Resolver: mapping an intent
// string to the installed or registry plugin(s) that can satisfy it.
//
// Purpose: the concrete, stateless Resolve algorithm behind the
//
//	pkg/plugin.Resolver interface — installed-first lookup, then
//	registry-ranked lookup, then the ambiguity/fail-closed protocols
//	pkg/plugin/intents.go documents on the interface itself.
//
// Inputs: a context (cancellation only), a plugin.ManifestSet snapshot, a
//
//	*plugin.VerifiedIndex witness, and the intent string — all supplied
//	per call by the caller (the planned conversational install flow,
//	P1-E24-W5-S50-T4).
//
// Outputs: the ranked []plugin.Candidate, or one of
//
//	plugin.ErrIntentEmpty, plugin.ErrUnverifiedIndex,
//	plugin.ErrIntentNotFound, or *plugin.AmbiguousIntent.
//
// Constraints: no network I/O, no bare time.Now, no side effects — a pure
//
//	function of its arguments (Art.7). The ranking tier type is
//	unexported: a tier is this package's business, not the SDK's.
//	internal/plugins/resolver has no production caller yet;
//	NewIntentResolver is listed in internal/build/testonly-allow.json
//	pending P1-E24-W5-S50-T4 (the conversational install flow's
//	internal/plugins/cascadepa_install_wiring.go host seam), which is the
//	ticket's own documented first consumer.
//
// SPORT: internal/plugins/resolver (ADD) — P1-E24-W5-S50-T3.
package resolver

import (
	"context"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// NewIntentResolver constructs the production plugin.Resolver: a stateless
// value with no fields, since Resolve takes every input as an argument.
func NewIntentResolver() plugin.Resolver {
	return intentResolver{}
}

// intentResolver is unexported: callers program against plugin.Resolver,
// never this concrete type (NewIntentResolver's return type is the
// interface, matching pkg/plugin/registry_client.go's own
// injected-seam-over-concrete-type convention).
type intentResolver struct{}

// rank is the registry-lookup match tier, strongest (lowest) first. It is
// deliberately 1-based: a zero rank is not a tier, so the zero value can
// never be mistaken for "matched at the strongest tier" the way an
// iota-from-zero tier can when it doubles as a no-match sentinel.
type rank int

const (
	// rankExactTag: one of the entry's tags equals the intent exactly.
	rankExactTag rank = 1
	// rankTagPrefix: one of the entry's tags has the intent as a prefix.
	rankTagPrefix rank = 2
	// rankKeyword: the intent appears as a substring of the entry's name
	// or description.
	rankKeyword rank = 3
)

// rankedCandidate pairs a candidate with the tier it matched at, so the
// tier can drive sorting and the tie count without appearing anywhere in
// the exported plugin.Candidate.
type rankedCandidate struct {
	cand plugin.Candidate
	tier rank
}

// Resolve implements plugin.Resolver. See pkg/plugin/intents.go's
// Resolver.Resolve doc comment for the full three-step contract; this
// method is the orchestration of the unexported steps below. ctx must be
// non-nil (ordinary Go convention) and is checked for cancellation before
// any work begins.
func (intentResolver) Resolve(ctx context.Context, installed plugin.ManifestSet, index *plugin.VerifiedIndex, intent string) ([]plugin.Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, cascade.Wrapf(cascade.KindCanceled, err, "plugin: intent resolution abandoned before any lookup")
	}
	needle := strings.ToLower(strings.TrimSpace(intent))
	if needle == "" {
		return nil, plugin.ErrIntentEmpty
	}

	switch hits := installedMatches(needle, installed); {
	case len(hits) == 1:
		return hits, nil
	case len(hits) > 1:
		return nil, &plugin.AmbiguousIntent{Intent: intent, Candidates: hits, Tied: len(hits)}
	}

	// Fail closed (06-FORGE-SPEC.md §5.20): with no verification witness
	// there is nothing to rank, and an unverified entry is never ranked.
	if index == nil {
		return nil, plugin.ErrUnverifiedIndex
	}

	ranked := rankRegistry(needle, index.Entries())
	if len(ranked) == 0 {
		return nil, plugin.ErrIntentNotFound
	}
	cands, tied := flatten(ranked)
	if tied > 1 {
		return nil, &plugin.AmbiguousIntent{Intent: intent, Candidates: cands, Tied: tied}
	}
	return cands, nil
}

// installedMatches implements step 1: every enabled plugin in installed
// whose manifest declares needle as a case-insensitive exact intent-name
// match, ordered by plugin id for determinism across calls (Art.11 — a
// map/sort-order flake is never acceptable). Zero hits means the caller
// proceeds to the registry step; more than one is an ambiguity the registry
// cannot resolve, since what is installed already explains it.
func installedMatches(needle string, installed plugin.ManifestSet) []plugin.Candidate {
	var hits []plugin.Candidate
	for _, ip := range installed {
		if !ip.Enabled || !manifestDeclaresIntent(ip.Manifest, needle) {
			continue
		}
		hits = append(hits, plugin.Candidate{
			PluginID: ip.Manifest.ID,
			Name:     ip.Manifest.Name,
			Source:   plugin.CandidateSourceInstalled,
			Manifest: ip.Manifest,
		})
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].PluginID < hits[j].PluginID })
	return hits
}

// manifestDeclaresIntent reports whether m's Provides.Intents contains
// needle as a case-insensitive exact match. needle is already lowercased
// and trimmed by the caller.
func manifestDeclaresIntent(m plugin.Manifest, needle string) bool {
	for _, is := range m.Provides.Intents {
		if strings.ToLower(strings.TrimSpace(is.Name)) == needle {
			return true
		}
	}
	return false
}

// rankRegistry implements step 2: one rankedCandidate per registry entry
// that matches needle at any tier, sorted strongest tier first with a
// plugin-id tie-break so the order is identical on every call. No matching
// entry is silently dropped; Resolve decides what to do with a tie at the
// strongest tier.
//
// registry.go's RegistryIndexEntry carries no provides.intents field —
// unlike a plugin.Manifest, the registry index publishes only id, name,
// description, and free-text tags (see testdata/README.md). Tags are
// therefore this step's proxy for provides.intents: a plugin author who
// wants an entry to surface for an intent publishes that intent name as a
// tag. The entry's ID is deliberately NOT matched at any tier: the
// contract ranks by intent name, and an id that happens to equal an intent
// string is a coincidence of naming, not a declaration that the plugin
// provides that intent.
func rankRegistry(needle string, entries []plugin.RegistryIndexEntry) []rankedCandidate {
	var out []rankedCandidate
	for _, e := range entries {
		tier, ok := registryTier(needle, e)
		if !ok {
			continue
		}
		out = append(out, rankedCandidate{
			cand: plugin.Candidate{
				PluginID:      e.ID,
				Name:          e.Name,
				Source:        plugin.CandidateSourceRegistry,
				RegistryEntry: e,
			},
			tier: tier,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].tier != out[j].tier {
			return out[i].tier < out[j].tier
		}
		return out[i].cand.PluginID < out[j].cand.PluginID
	})
	return out
}

// registryTier reports the strongest tier e matches needle at, checking
// tiers strongest-first so an entry that would also satisfy a weaker tier
// is never mis-ranked:
//
//	rankExactTag  — one of e's tags equals needle.
//	rankTagPrefix — one of e's tags HAS needle as its prefix (one
//	                direction only: "lint" matches the tag "linter";
//	                the tag "lint" does not match the intent "linter").
//	rankKeyword   — needle appears as a substring of e's name or
//	                description.
//
// ok is false when none of the three matches. needle is already lowercased
// and trimmed by the caller.
func registryTier(needle string, e plugin.RegistryIndexEntry) (rank, bool) {
	if tagsAny(e.Tags, func(tag string) bool { return tag == needle }) {
		return rankExactTag, true
	}
	if tagsAny(e.Tags, func(tag string) bool { return strings.HasPrefix(tag, needle) }) {
		return rankTagPrefix, true
	}
	if strings.Contains(strings.ToLower(e.Name), needle) ||
		strings.Contains(strings.ToLower(e.Description), needle) {
		return rankKeyword, true
	}
	return 0, false
}

// tagsAny reports whether match accepts any of tags, lowercased.
func tagsAny(tags []string, match func(tag string) bool) bool {
	for _, t := range tags {
		if match(strings.ToLower(strings.TrimSpace(t))) {
			return true
		}
	}
	return false
}

// flatten drops the unexported tier from an already-sorted ranked list and
// reports how many leading entries share the strongest tier — the tie
// count plugin.AmbiguousIntent.Tied carries. The sort in rankRegistry
// guarantees that group is contiguous at the front, so the count is a
// single linear scan and cands[:tied] is exactly the tied group.
func flatten(ranked []rankedCandidate) (cands []plugin.Candidate, tied int) {
	if len(ranked) == 0 {
		return nil, 0
	}
	cands = make([]plugin.Candidate, 0, len(ranked))
	top := ranked[0].tier
	for _, rc := range ranked {
		cands = append(cands, rc.cand)
		if rc.tier == top {
			tied++
		}
	}
	return cands, tied
}
