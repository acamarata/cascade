package learn

// Purpose: detect supported recurring failures using a closed proposal rule.
// Inputs: structured outcomes, existing proposals, and an injected clock window.
// Outputs: aligned submissions and triggers; no storage or configuration writes.
// Constraints: beneficiary evidence excluded, fixed thresholds, no inferred edits.
// SPORT: learn/proposal-engine/ADD.

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ProposalLookup reads the latest proposal for idempotent detection.
type ProposalLookup interface {
	LatestBySignature(ctx context.Context, sig string) (Proposal, bool, error)
}

func distinctPatternSources(rows []OutcomeRow) int {
	repos, lanes := map[string]bool{}, map[LaneIdentity]bool{}
	for _, r := range rows {
		if r.RepoID != "" && r.RepoID != "unknown" {
			repos[r.RepoID] = true
		}
		if r.Lane.LaneTier != "" && r.Lane.NodeID != "" && r.Lane.NodeID != "unknown" {
			lanes[r.Lane] = true
		}
	}
	return max(len(repos), len(lanes))
}

// ProposalRule is a closed mapping from a failure tuple to a known change.
type ProposalRule struct {
	Family    Family
	Category  string
	Component string
	Kind      ProposedChangeKind
	Target    TargetID
	Change    Change
}

// ProposalRules returns a fresh copy of the sole P1 proposal rule.
func ProposalRules() []ProposalRule {
	return []ProposalRule{{Family: FamilyToolFailure, Category: "*", Component: "generated-code",
		Kind: "ci", Target: "ci.local_steps", Change: Change{Op: OpAddStep, Path: "ci.local.build",
			Value: `"cascade context generate --check"`}}}
}

type patternGroup struct {
	rows []OutcomeRow
	rule ProposalRule
	pool string
	repo string
}

// DetectPatterns returns pending proposal inputs without applying or storing them.
// The earliest outcome's lane is the beneficiary of each signature. It never
// supports its own proposal. Lookup and reader errors refuse the entire pass.
func DetectPatterns(ctx context.Context, r OutcomeReader, existing ProposalLookup, now time.Time, window time.Duration) ([]Submission, []Trigger, error) {
	if r == nil || existing == nil || now.IsZero() || window <= 0 || window > Window {
		return nil, nil, cascade.New(cascade.KindInvalidInput, "learn: invalid pattern detection inputs")
	}
	from := now.Add(-window)
	rows, err := r.ReadWindow(ctx, from, now)
	if err != nil {
		return nil, nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: detection outcome read refused")
	}
	groups, err := collectPatternGroups(rows, from, now)
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(groups))
	for sig := range groups {
		keys = append(keys, sig)
	}
	sort.Strings(keys)
	var subs []Submission
	var triggers []Trigger
	for _, sig := range keys {
		g := groups[sig]
		beneficiary, support, err := patternSupport(g)
		if errors.Is(err, ErrInsufficientSupport) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		sub, found, err := newPatternSubmission(ctx, existing, sig, g.rule, support)
		if err != nil {
			return nil, nil, err
		}
		if found {
			continue
		}
		subs = append(subs, sub)
		triggers = append(triggers, Trigger{Signature: sig, Occurrences: len(support),
			DistinctSources: distinctPatternSources(support), Beneficiary: beneficiary,
			WindowStart: from, WindowEnd: now, Kind: g.rule.Kind})
	}
	return subs, triggers, nil
}

func newPatternSubmission(ctx context.Context, existing ProposalLookup, sig string, rule ProposalRule, support []OutcomeRow) (Submission, bool, error) {
	_, found, err := existing.LatestBySignature(ctx, sig)
	if err != nil {
		return Submission{}, false, cascade.Wrap(cascade.KindUnavailable, err, "learn: detection proposal lookup refused")
	}
	if found {
		return Submission{}, true, nil
	}
	sub := patternSubmission(sig, rule, support)
	if _, err := ClassifyChange(sub.Target, sub.Change); err != nil {
		return Submission{}, false, err
	}
	return sub, false, validateSubmission(sub)
}

// patternSupport deterministically selects the earliest lane for the target
// repo, then removes that lane from support gathered across matching repos.
func patternSupport(g *patternGroup) (LaneIdentity, []OutcomeRow, error) {
	sort.Slice(g.rows, func(i, j int) bool {
		if g.rows[i].CompletedAt.Equal(g.rows[j].CompletedAt) {
			return g.rows[i].OutcomeID < g.rows[j].OutcomeID
		}
		return g.rows[i].CompletedAt.Before(g.rows[j].CompletedAt)
	})
	var beneficiary LaneIdentity
	for _, row := range g.rows {
		if row.RepoID == g.repo {
			beneficiary = row.Lane
			break
		}
	}
	support, err := ValidateSupport(beneficiary, g.rows)
	return beneficiary, support, err
}

func collectPatternGroups(rows []OutcomeRow, from, to time.Time) (map[string]*patternGroup, error) {
	groups := map[string]*patternGroup{}
	pools := map[string][]OutcomeRow{}
	for _, row := range rows {
		if row.CompletedAt.Before(from) || row.CompletedAt.After(to) {
			continue
		}
		if err := validatePatternRow(row); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, f := range row.Findings {
			if err := validateFindingInputs(f); err != nil {
				return nil, err
			}
			if _, _, err := DecodeFamilyCategory(string(f.Family), string(f.Category)); err != nil {
				return nil, err
			}
			if _, err := DecodeSeverity(string(f.Severity)); err != nil {
				return nil, err
			}
			if f.Count == 0 {
				continue
			}
			for _, rule := range ProposalRules() {
				if rule.Family != f.Family || rule.Component != row.Component || (rule.Category != "*" && rule.Category != string(f.Category)) {
					continue
				}
				sig := FailureSignature(row.RepoID, row.Component, row.TaskClass, row.RiskClass, f.Family, f.Category)
				if seen[sig] {
					continue
				}
				seen[sig] = true
				pool := FailureSignature("", row.Component, row.TaskClass, row.RiskClass, f.Family, f.Category)
				if groups[sig] == nil {
					groups[sig] = &patternGroup{rule: rule, pool: pool, repo: row.RepoID}
				}
				pools[pool] = append(pools[pool], row)
			}
		}
	}
	for _, g := range groups {
		g.rows = append([]OutcomeRow(nil), pools[g.pool]...)
	}
	return groups, nil
}

func patternSubmission(sig string, rule ProposalRule, rows []OutcomeRow) Submission {
	sub := Submission{Source: SourceRef{Kind: SourceDetector, ID: sig},
		Target: string(rule.Target), Change: rule.Change, Confidence: 1}
	for _, row := range rows {
		lane := FailureSignature("", "", row.Lane.LaneTier, row.Lane.NodeID, "", "")
		sub.Evidence = append(sub.Evidence, EvidenceRef{Kind: "telemetry_outcome",
			ID: strconv.FormatInt(row.OutcomeID, 10), RepoID: row.RepoID, LaneID: lane})
	}
	return sub
}
