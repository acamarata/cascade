// Package build (this file) closes AUD-040's other gap in coveragegate.go's
// gate: coverage-baseline.json is a hand-curated ratchet floor list that
// coveragegate.go's own doc comment already names as covering only 11
// packages, while PackageTier assigns an Art.4 floor to well over a
// hundred. CheckCoverage's doc comment states outright that "a package
// present in profile but absent from baseline is NOT a violation" — so a
// new package can measure any coverage at all, ship, and never be
// ratcheted again, silently, forever. This file adds the missing half:
// BASELINE completeness (as opposed to coveragegate_completeness.go's
// PROFILE completeness) — every profiled, floor-bearing package with
// statements must carry a baseline entry — plus the generator logic that
// fills that baseline mechanically from a measured profile, so a missing
// entry is a one-command fix, never a hand-typed one.
//
// # Never touches an existing entry, never claims more than measured
//
// AddMissingBaselineEntries copies every existing entry through
// unchanged — it has no code path that can overwrite one — and computes a
// new entry's baseline value with baselineFloorTruncate1dp, which rounds
// DOWN. A generated baseline entry must never assert more coverage than
// the profile that produced it actually measured; rounding up even by
// 0.05 would be exactly the kind of gate that looks stricter than reality.
//
// # FormatBaselineJSON matches the tracked file byte-for-byte
//
// coverage-baseline.json is hand-authored today in a fixed shape (2-space
// indent, `{ "tier": ..., "floor": N.N, "baseline": N.N }` per line, one
// decimal place on every float, sorted keys). FormatBaselineJSON
// reproduces that exact shape rather than delegating to
// encoding/json.MarshalIndent (which would drop trailing ".0" on whole
// numbers and cannot control brace spacing), so an entry this generator
// did not touch serializes to the identical bytes it started with —
// asserted by TestCoverageBaselineAddMissingNeverChangesExisting.
package build

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// CheckBaselineCompleteness reports one CoverageViolation{Reason:
// "no-baseline"} for every package in profile that (1) has > 0 total
// statements (a zero-statement package has nothing to ratchet, exactly
// like CheckCoverage's own skip), (2) PackageTier assigns an Art.4 floor
// to (pkg/** and anything outside the five scanned trees are never
// baseline-checked, same exclusion as the floor/ratchet check), and (3)
// has no entry in baseline. This is independent of CheckCoverage's
// floor/ratchet logic: a package can pass its floor and still be
// "no-baseline" if nobody has ever committed a ratchet entry for it.
func CheckBaselineCompleteness(profile map[string]*CoverageStats, baseline map[string]BaselineEntry) []CoverageViolation {
	var out []CoverageViolation
	for pkg, stats := range profile {
		if stats == nil || stats.TotalStmts == 0 {
			continue
		}
		_, floor, ok := PackageTier(pkg)
		if !ok {
			continue
		}
		if _, present := baseline[pkg]; present {
			continue
		}
		out = append(out, CoverageViolation{
			Package:  pkg,
			Measured: stats.Percent(),
			Floor:    floor,
			Reason:   "no-baseline",
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Package < out[j].Package })
	return out
}

// baselineFloorTruncate1dp truncates v to one decimal place, rounding DOWN
// only — see this file's package doc, "never claims more than measured".
func baselineFloorTruncate1dp(v float64) float64 {
	return math.Floor(v*10) / 10
}

// AddMissingBaselineEntries returns a NEW baseline map built from existing
// (copied through, never mutated in place, and every existing key's VALUE
// is copied unchanged — this function has no code path that can alter
// one) plus one added entry for every package in profile that (1) has > 0
// total statements, (2) PackageTierFor(pkg, roots[pkg]) assigns an Art.4
// floor to — roots carries R-14.239's composition-root classification so
// a plugin's own main package gets the CLI floor, not its library
// siblings' — and (3) has no entry in existing. added lists the newly
// added package keys, sorted, for the caller to report. roots may be nil.
func AddMissingBaselineEntries(
	existing map[string]BaselineEntry, profile map[string]*CoverageStats, roots map[string]bool,
) (out map[string]BaselineEntry, added []string) {
	out = make(map[string]BaselineEntry, len(existing)+len(profile))
	for k, v := range existing {
		out[k] = v
	}
	for pkg, stats := range profile {
		if stats == nil || stats.TotalStmts == 0 {
			continue
		}
		if _, present := existing[pkg]; present {
			continue
		}
		tier, floor, ok := PackageTierFor(pkg, roots[pkg])
		if !ok {
			continue
		}
		out[pkg] = BaselineEntry{
			Tier:     string(tier),
			Floor:    floor,
			Baseline: baselineFloorTruncate1dp(stats.Percent()),
		}
		added = append(added, pkg)
	}
	sort.Strings(added)
	return out, added
}

// FormatBaselineJSON renders entries in coverage-baseline.json's exact
// tracked shape — see this file's package doc for why this is not
// encoding/json.MarshalIndent. Every line but the last carries a TRAILING
// comma. This is the ONLY renderer: the generator always re-renders the
// whole file through it, so the output is a deterministic function of the
// entries and a run that adds nothing leaves the file byte-identical
// (TestCoverageBaseline_TrackedFileIsCanonical).
func FormatBaselineJSON(entries map[string]BaselineEntry) []byte {
	keys := sortedBaselineKeys(entries)
	var b strings.Builder
	b.WriteString("{\n")
	for i, k := range keys {
		b.WriteString(renderBaselineLine(k, entries[k]))
		if i < len(keys)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

// sortedBaselineKeys returns entries' keys, sorted.
func sortedBaselineKeys(entries map[string]BaselineEntry) []string {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderBaselineLine renders one entry's line with NO trailing comma and
// NO trailing newline — callers add both as their position requires.
func renderBaselineLine(key string, e BaselineEntry) string {
	return fmt.Sprintf("  %q: { \"tier\": %q, \"floor\": %s, \"baseline\": %s }",
		key, e.Tier, formatBaselineFloat(e.Floor), formatBaselineFloat(e.Baseline))
}

// formatBaselineFloat renders v with exactly one decimal place, matching
// every value already committed to coverage-baseline.json (e.g. "90.0",
// "87.6" — never bare "90" or a longer mantissa).
func formatBaselineFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', 1, 64)
}
