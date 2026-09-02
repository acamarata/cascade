// Package build (this file) implements the coverage-floor gate + ratchet
// from P1-E01-W1-S01-T8 (12-QUALITY-CONSTITUTION.md Art.4): per-package-
// class statement floors, plus a per-package ratchet against a committed
// baseline so CI fails if coverage DROPS for any package, even above its
// floor. It consumes the coverage PROFILE the A-T4 coverage lane already
// produces (`go test -covermode=atomic -coverprofile=coverage.out ./...`)
// — this gate does not run tests itself.
//
// # Statements only — branches are a named, honest gap
//
// Art.4's table has both a Statements and a Branches column. Go's stdlib
// coverage tooling (`go test -cover`, the profile format parsed here) is
// STATEMENT coverage only; it has no branch-coverage mode, and this repo
// has adopted no separate branch-coverage tool (docs/developer/quality-
// gates.md records this as a known gap, not a silent claim of compliance).
// This gate therefore enforces the Statements column only. Claiming to
// enforce a Branches floor with a tool that cannot measure branches would
// be exactly the kind of gate that looks correct and enforces nothing —
// the lesson this ticket exists to avoid — so the gap is named instead.
//
// # Tier mapping
//
// Art.4 names four package classes by example, not by an exhaustive
// directory list. This gate maps a measured package's import path to a
// tier by directory PREFIX, in priority order (first match wins):
//  1. Security/policy/secrets: internal/policy, internal/secrets,
//     internal/audit (≥90%).
//  2. CLI/cmd surface: cmd/** (≥70%).
//  3. Plugins/providers (first-party): providers/**, plugins/** (≥80%).
//  4. Everything else under internal/** defaults to core engine (≥85%) —
//     Art.4's example list (storage, conductor, fleet, context, retrieval,
//     sync, backup) is illustrative of "internal engine/infrastructure
//     packages", not exhaustive, and this repo's internal/ tree already
//     holds packages the example list does not name (runtime, build,
//     buildinfo, testkit, output, plugins-host, storage/storetest, mcp,
//     nodes, notify, events, conversation, provenance) that are equally
//     core-engine infrastructure, not a fifth tier the article never
//     defines.
//
// pkg/** is DELIBERATELY excluded from Art.4 floors: it is the public SDK
// CONTRACT surface (interfaces, mostly zero-statement by construction —
// pkg/provider measures 0.0% purely because it declares interfaces with no
// executable bodies), governed instead by Art.10.5 (dead code) and
// Art.10.6 (godoc), which this ticket's deadcode.go implements separately.
package build

import (
	"bufio"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// CoverageTier names one Art.4 package class.
type CoverageTier string

// The four Art.4 package-class tiers.
const (
	// TierSecurity covers internal/{policy,secrets,audit} (>=90%).
	TierSecurity CoverageTier = "security"
	// TierCore covers the rest of internal/** (>=85%).
	TierCore CoverageTier = "core"
	// TierPlugins covers providers/** and plugins/** (>=80%).
	TierPlugins CoverageTier = "plugins"
	// TierCLI covers cmd/** (>=70%).
	TierCLI CoverageTier = "cli"
)

// coverageTierFloors is Art.4's Statements column, by tier.
var coverageTierFloors = map[CoverageTier]float64{
	TierSecurity: 90.0,
	TierCore:     85.0,
	TierPlugins:  80.0,
	TierCLI:      70.0,
}

// coverageSecurityPackages is the fixed Art.4 security-tier allowlist.
var coverageSecurityPackages = map[string]bool{
	"internal/policy":  true,
	"internal/secrets": true,
	"internal/audit":   true,
}

// PackageTier classifies importPath (module-relative, e.g.
// "internal/build" or "providers/sqlite") into its Art.4 tier and floor.
// ok is false for pkg/** (excluded) and for anything outside the five
// scanned root trees.
func PackageTier(importPath string) (tier CoverageTier, floor float64, ok bool) {
	if strings.HasPrefix(importPath, "pkg/") || importPath == "pkg" {
		return "", 0, false
	}
	if coverageSecurityPackages[importPath] {
		return TierSecurity, coverageTierFloors[TierSecurity], true
	}
	if strings.HasPrefix(importPath, "cmd/") || importPath == "cmd" {
		return TierCLI, coverageTierFloors[TierCLI], true
	}
	if strings.HasPrefix(importPath, "providers/") || strings.HasPrefix(importPath, "plugins/") {
		return TierPlugins, coverageTierFloors[TierPlugins], true
	}
	if strings.HasPrefix(importPath, "internal/") {
		return TierCore, coverageTierFloors[TierCore], true
	}
	return "", 0, false
}

// CoverageStats is one package's aggregated statement coverage from a
// profile.
type CoverageStats struct {
	CoveredStmts int
	TotalStmts   int
}

// Percent returns the covered-statement percentage, 0 for a package with
// zero total statements (nothing to cover — never a violation by itself).
func (s CoverageStats) Percent() float64 {
	if s.TotalStmts == 0 {
		return 0
	}
	return 100 * float64(s.CoveredStmts) / float64(s.TotalStmts)
}

// ParseCoverageProfile parses a Go coverage profile (the format
// `go test -coverprofile=...` writes: a "mode:" header line, then
// "<file>:<startline>.<col>,<endline>.<col> <numstmt> <count>" blocks) and
// aggregates statement counts by PACKAGE — the file field in this format
// is already an import-path-style string (module path + file), so the
// package is everything up to the final "/" segment.
func ParseCoverageProfile(data []byte) (map[string]*CoverageStats, error) {
	out := make(map[string]*CoverageStats)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if lineNo == 1 && strings.HasPrefix(line, "mode:") {
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			return nil, fmt.Errorf("coverage gate: line %d: no ':' found: %q", lineNo, line)
		}
		file := line[:colon]
		fields := strings.Fields(line[colon+1:])
		if len(fields) != 3 {
			return nil, fmt.Errorf("coverage gate: line %d: expected 3 fields after position, got %d: %q", lineNo, len(fields), line)
		}
		numStmt, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("coverage gate: line %d: bad numStmt %q: %w", lineNo, fields[1], err)
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("coverage gate: line %d: bad count %q: %w", lineNo, fields[2], err)
		}
		slash := strings.LastIndex(file, "/")
		if slash < 0 {
			return nil, fmt.Errorf("coverage gate: line %d: file %q has no package directory", lineNo, file)
		}
		pkg := file[:slash]
		if out[pkg] == nil {
			out[pkg] = &CoverageStats{}
		}
		out[pkg].TotalStmts += numStmt
		if count > 0 {
			out[pkg].CoveredStmts += numStmt
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("coverage gate: scanning profile: %w", err)
	}
	return out, nil
}

// StripModulePrefix converts a profile's full import-path package key
// (e.g. "github.com/acamarata/cascade/internal/build") to its
// module-relative form ("internal/build") by trimming modulePath + "/".
// Keys that are the module path itself, or do not carry the prefix at all,
// are returned unchanged (defensive — never silently drops data).
func StripModulePrefix(pkg, modulePath string) string {
	prefix := modulePath + "/"
	if strings.HasPrefix(pkg, prefix) {
		return strings.TrimPrefix(pkg, prefix)
	}
	return pkg
}

// BaselineEntry is one package's committed ratchet baseline
// (testdata/coverage-baseline.json).
type BaselineEntry struct {
	Tier     string  `json:"tier"`
	Floor    float64 `json:"floor"`
	Baseline float64 `json:"baseline"`
}

// ParseBaseline decodes the coverage-baseline.json format: a flat map of
// module-relative import path (with the module prefix stripped, e.g.
// "internal/build") to BaselineEntry.
func ParseBaseline(data []byte) (map[string]BaselineEntry, error) {
	var out map[string]BaselineEntry
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("coverage gate: parsing baseline: %w", err)
	}
	return out, nil
}

// coverageEpsilon absorbs float rounding noise between two independently
// computed percentages; a drop smaller than this is not a ratchet
// violation.
const coverageEpsilon = 0.05

// CoverageViolation is one package failing either its Art.4 floor or its
// ratchet against the committed baseline.
type CoverageViolation struct {
	Package  string
	Measured float64
	Floor    float64
	Baseline float64
	Reason   string // "floor" or "ratchet"
}

// CheckCoverage compares profile (module-relative package -> stats,
// modulePrefix stripped by the caller — see coveragegate_test.go's
// stripModulePrefix) against each package's Art.4 tier floor and its
// baseline entry. A package present in profile but absent from baseline is
// NOT a violation by itself (a brand-new package has no prior baseline to
// ratchet against) but IS still floor-checked. A package with zero total
// statements (no shippable logic yet — a doc.go placeholder) is skipped
// entirely: there is nothing to measure, and Art.4 floors govern shipped
// behavior, not empty packages.
func CheckCoverage(profile map[string]*CoverageStats, baseline map[string]BaselineEntry) []CoverageViolation {
	var out []CoverageViolation
	for pkg, stats := range profile {
		if stats.TotalStmts == 0 {
			continue
		}
		tier, floor, ok := PackageTier(pkg)
		if !ok {
			continue
		}
		measured := stats.Percent()
		if measured < floor-coverageEpsilon {
			out = append(out, CoverageViolation{Package: pkg, Measured: measured, Floor: floor, Reason: "floor"})
			continue
		}
		if base, present := baseline[pkg]; present && measured < base.Baseline-coverageEpsilon {
			out = append(out, CoverageViolation{Package: pkg, Measured: measured, Floor: floor, Baseline: base.Baseline, Reason: "ratchet"})
		}
		_ = tier
	}
	return out
}
