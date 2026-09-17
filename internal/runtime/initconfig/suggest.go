package initconfig

// Purpose: the nearest-match suggestion the strict parser attaches to an
//   unknown key (P1-E16-W4-S35-T7). Split from schema.go so that file
//   stays the schema and this stays the string distance.
// Constraints: a suggestion is only made when something is genuinely
//   close. One bad guess teaches an operator to ignore every later one.
// SPORT: internal/runtime/initconfig schema (ADD) — P1-E16-W4-S35-T7.

import (
	"sort"
	"strings"
)

// knownKeys lists every key the schema accepts, dotted, so a suggestion
// can be made for a misspelling anywhere in the document rather than only
// at the top level.
//
// Written out rather than derived from the struct tags by reflection: the
// list IS the schema's public surface, and a reader comparing this file
// to 08 §2 should see the whole of it in one place.
func knownKeys() []string {
	return []string{
		"schema", "profile",
		"plugins.enable", "plugins.disable", "plugins.harness",
		"providers.name", "providers.kind", "providers.auth",
		"providers.key_env", "providers.base_url", "providers.verify",
		"harnesses.detect", "harnesses.install",
		"server.postgres_dsn_env", "server.redis_url_env", "server.s3_env_prefix",
		"telemetry.enabled",
		"daemon.install",
	}
}

// nearestKey returns the known key closest to got, or "" when nothing is
// close enough to be worth suggesting.
//
// Matching is on the LEAF within the same table first, then on the whole
// dotted key. "plugins.enabel" must suggest "plugins.enable" and not
// "telemetry.enabled", which is closer by raw edit distance and in the
// wrong table.
//
// The threshold matters as much as the metric: proposing "telemetry" to
// somebody who wrote "kubernetes" is noise, and an operator who sees one
// bad suggestion stops reading the next.
func nearestKey(got string) string {
	got = strings.ToLower(strings.TrimSpace(got))
	if got == "" {
		return ""
	}
	keys := knownKeys()
	sort.Strings(keys)
	if table, leaf, dotted := strings.Cut(got, "."); dotted {
		if best := closest(leaf, leavesOf(table, keys)); best != "" {
			return table + "." + best
		}
	}
	return closest(got, keys)
}

// leavesOf returns the leaf names under one table.
func leavesOf(table string, keys []string) []string {
	out := []string{}
	for _, k := range keys {
		if t, leaf, ok := strings.Cut(k, "."); ok && t == table {
			out = append(out, leaf)
		}
	}
	return out
}

// closest returns the candidate nearest got, or "" when none is near
// enough. The threshold scales with what was typed: a three-letter key
// tolerates one edit, a long one tolerates more.
func closest(got string, candidates []string) string {
	best, bestDist := "", len(got)/2+1
	for _, c := range candidates {
		if d := editDistance(got, c); d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// min3 returns the smallest of three ints.
func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
