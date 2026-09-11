package jobs

// Purpose: the R-16.37 change-footprint risk classifier plus the
//
//	R-21.182 footprint union and its unlowerable Critical floor, and the
//	R-21.257 reachability seam.
//
// Inputs: a footprint path set, the distinct repository ids the plan
//
//	input resolves to, a probe root for existing-file content checks,
//	and an optional ReachabilityFn.
//
// Outputs: RiskClass, or a fail-closed error from the reachability seam.
//
// Constraints: pure function over its inputs (Art.7: no bare time/rand,
//
//	no network -- content probes read only existing files under the
//	given root, never fetch). The Critical floor is evaluated FIRST and
//	is unlowerable by any later rule; the R-16.37 rules are then
//	evaluated Critical -> High -> Low -> else Normal, first match wins.
//
// SPORT: jobs/risk-classifier/ADD (P1-E29-W6-S59-T4).

import (
	"context"
	"os"
	"path"
	"strings"
)

// RiskClass is the closed R-16.37 risk-class vocabulary. riskRank below
// gives it a severity ordering distinct from the RULE EVALUATION order
// (Critical, High, Low, else Normal is a first-match precedence, not a
// severity ranking): Low is the least risky (docs-only), Normal is the
// baseline default, High and Critical add gates on top. R-21.146's
// monotonicity rule ("class only increases") is defined against this
// severity ranking.
type RiskClass string

// The closed four-member RiskClass vocabulary, in severity order.
const (
	RiskClassLow      RiskClass = "low"
	RiskClassNormal   RiskClass = "normal"
	RiskClassHigh     RiskClass = "high"
	RiskClassCritical RiskClass = "critical"
)

// riskRank is the severity ordering RiskClass's zero-value-free four
// members carry: low < normal < high < critical.
var riskRank = map[RiskClass]int{
	RiskClassLow:      0,
	RiskClassNormal:   1,
	RiskClassHigh:     2,
	RiskClassCritical: 3,
}

// ReachabilityFn is the R-21.257 optional injected seam: given the
// pre/post-image footprint, it returns the additional paths symbol
// reachability adds to the union (e.g. call sites reachable into an
// auth/secret/schema package). A nil ReachabilityFn means no expansion
// -- the W6 behavior, since AG/S-67.T3 (W7) is the first implementer.
// This type never imports a graph or classifier package.
type ReachabilityFn func(ctx context.Context, paths []string) ([]string, error)

// criticalFloorCategory names which R-21.182 floor category a path
// matched, for Escalation.TriggeringPaths/journal-quality diagnostics.
type criticalFloorCategory string

const (
	floorGateTable        criticalFloorCategory = "gate table"
	floorClassifierTable  criticalFloorCategory = "classifier table"
	floorPolicyFile       criticalFloorCategory = "policy file"
	floorEgressClass      criticalFloorCategory = "egress-class file"
	floorGeneratedHarness criticalFloorCategory = "generated harness artifact"
)

// criticalFloorMatch reports whether p falls in one of the R-21.182
// floor's five categories. R-21.182 names the categories by ROLE, not
// by an enumerated path list; this is this ticket's own derivation from
// the concrete files those roles name elsewhere in the tree: the
// internal/build gate files (this repo's "gate tables"), this ticket's
// own risk/gate-set files plus K/S-22.T4's task-class table
// ("classifier tables"), internal/policy (R-16.37's own "policy file"
// wording), internal/secrets as the egress choke point (R-21.255/260),
// and the compiled-in harness templates (R-21.187, "generated harness
// artifact"). A later ticket adding its own gate or classifier table
// extends this list; narrowing it needs a T0 ruling.
func criticalFloorMatch(p string) (criticalFloorCategory, bool) {
	switch {
	case strings.HasPrefix(p, "internal/build/") &&
		(strings.Contains(p, "gate") || strings.HasSuffix(p, "-allow.json") ||
			path.Base(p) == "licenses.go" || path.Base(p) == "egress_allow.go"):
		return floorGateTable, true
	case p == "internal/jobs/risk.go" || p == "internal/jobs/riskgates.go" ||
		p == "internal/conductor/task_classes.go":
		return floorClassifierTable, true
	case strings.HasPrefix(p, "internal/policy/"):
		return floorPolicyFile, true
	case strings.HasPrefix(p, "internal/secrets/"):
		return floorEgressClass, true
	case strings.HasPrefix(p, "internal/repo/templates/") || strings.HasPrefix(p, ".github/workflows/"):
		return floorGeneratedHarness, true
	default:
		return "", false
	}
}

// unionFootprint computes the R-21.182 footprint: pre-image paths union
// post-image paths (both already folded into footprint by the caller --
// a rename's caller supplies both names) union the reachability
// expansion. A nil reachFn contributes nothing. A non-nil reachFn is
// called ONCE; its error is returned to the caller, which fails closed
// to Critical rather than falling back to the path-only union.
func unionFootprint(ctx context.Context, footprint []string, reachFn ReachabilityFn) ([]string, error) {
	if reachFn == nil {
		return footprint, nil
	}
	extra, err := reachFn(ctx, footprint)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(footprint)+len(extra))
	out := make([]string, 0, len(footprint)+len(extra))
	for _, p := range append(append([]string{}, footprint...), extra...) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// classifyFootprint is the pure R-16.37/R-21.182 classifier. footprint
// is the already-unioned path set (see unionFootprint); repositories is
// the distinct set of repository ids the plan input resolves to
// (always length 1 for a single-session W6 plan; the >=2-repositories
// rule below is exercised by direct unit test with a synthetic value,
// since no W6 PlanInput field carries more than one repository id yet
// -- an absent-constant, like the reachability seam, until a
// multi-repository plan input exists); probeRoot is the existing-file
// content-probe root (empty/absent root skips content probes -- a
// path-rule match still applies, only content markers do not).
func classifyFootprint(footprint []string, repositories []string, probeRoot string) RiskClass {
	for _, p := range footprint {
		if _, ok := criticalFloorMatch(p); ok {
			return RiskClassCritical
		}
	}

	if matchesCriticalPaths(footprint) {
		return RiskClassCritical
	}
	if hasDropAlterMigration(footprint, probeRoot) {
		return RiskClassCritical
	}

	if len(distinct(repositories)) >= 2 {
		return RiskClassHigh
	}
	if matchesHighPaths(footprint) {
		return RiskClassHigh
	}
	if hasContentMarker(footprint, probeRoot, "sync/atomic") || hasContentMarker(footprint, probeRoot, "go func") {
		return RiskClassHigh
	}
	if hasPkgPath(footprint) {
		return RiskClassHigh
	}

	if len(footprint) > 0 && allDocsOrMarkdown(footprint) {
		return RiskClassLow
	}

	return RiskClassNormal
}

// criticalPathRules are the R-16.37 Critical path-prefix set (excluding
// the DROP/ALTER migration content probe and the provider-auth glob,
// each handled separately), verbatim from the ticket contract.
var criticalPathRules = []string{
	"internal/secrets/", "internal/policy/", "internal/elevation/",
}

func matchesCriticalPaths(footprint []string) bool {
	for _, p := range footprint {
		for _, rule := range criticalPathRules {
			if strings.HasPrefix(p, rule) {
				return true
			}
		}
		if p == ".goreleaser.yaml" || p == "install.sh" {
			return true
		}
		if isProviderAuthPath(p) {
			return true
		}
	}
	return false
}

// matchesHighPaths implements R-16.37's High path rule: any
// **/migrations/** or *.sql path (a migration path WITHOUT a DROP/ALTER
// marker is High, not Critical -- the DROP/ALTER content probe is what
// promotes a migration path to Critical, checked separately).
func matchesHighPaths(footprint []string) bool {
	for _, p := range footprint {
		if strings.Contains(p, "/migrations/") || strings.HasSuffix(p, ".sql") {
			return true
		}
	}
	return false
}

// isProviderAuthPath matches providers/*/auth* per R-16.37's Critical
// set.
func isProviderAuthPath(p string) bool {
	if !strings.HasPrefix(p, "providers/") {
		return false
	}
	rest := strings.TrimPrefix(p, "providers/")
	segs := strings.SplitN(rest, "/", 2)
	if len(segs) < 2 {
		return false
	}
	return strings.HasPrefix(segs[1], "auth")
}

func hasPkgPath(footprint []string) bool {
	for _, p := range footprint {
		if strings.HasPrefix(p, "pkg/") {
			return true
		}
	}
	return false
}

func allDocsOrMarkdown(footprint []string) bool {
	for _, p := range footprint {
		if !strings.HasPrefix(p, "docs/") && !strings.HasSuffix(p, ".md") {
			return false
		}
	}
	return true
}

func distinct(vs []string) []string {
	seen := make(map[string]bool, len(vs))
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// readProbeFile reads an existing file under root for a content probe.
// An absent path or absent root contributes no marker match -- content
// probes read only existing files (HOW-4's own rule).
func readProbeFile(root, relPath string) (string, bool) {
	if root == "" {
		return "", false
	}
	b, err := os.ReadFile(path.Join(root, relPath))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func hasContentMarker(footprint []string, root, marker string) bool {
	for _, p := range footprint {
		content, ok := readProbeFile(root, p)
		if !ok {
			continue
		}
		if strings.Contains(content, marker) {
			return true
		}
	}
	return false
}

func hasDropAlterMigration(footprint []string, root string) bool {
	for _, p := range footprint {
		if !strings.Contains(p, "/migrations/") {
			continue
		}
		content, ok := readProbeFile(root, p)
		if !ok {
			continue
		}
		upper := strings.ToUpper(content)
		if strings.Contains(upper, "DROP ") || strings.Contains(upper, "ALTER ") {
			return true
		}
	}
	return false
}
