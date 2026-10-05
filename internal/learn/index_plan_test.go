package learn

// Purpose: the read-path indices are used. EXPLAIN QUERY PLAN of the scorer's
//   repo-language lookup and the estimate source's decision join must search
//   an index, never scan the unbounded outcome or decision table.
// SPORT: internal.learn.SQLiteEstimateSource/TESTED (P1-CAP-03).

import (
	"context"
	"strings"
	"testing"
)

// planDetails returns the EXPLAIN QUERY PLAN detail lines of q.
func planDetails(t *testing.T, q string, args ...any) []string {
	t.Helper()
	db := openMigratedDB(t, tmplOutcome)
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil || len(out) == 0 {
		t.Fatalf("plan rows = %d, err %v", len(out), err)
	}
	return out
}

// planUses reports whether some plan line is a SEARCH of table (or its alias)
// using index, covering or not.
func planUses(plan []string, table, index string) bool {
	for _, d := range plan {
		if strings.HasPrefix(d, "SEARCH "+table+" ") && strings.Contains(d, "INDEX "+index+" ") {
			return true
		}
	}
	return false
}

func TestReadPathIndicesAreUsed(t *testing.T) {
	lang := planDetails(t, repoLanguageQuery, "repo-x")
	if !planUses(lang, tableTelemetryOutcomes, "idx_jobs_telemetry_outcomes_repo_created") {
		t.Errorf("repo-language plan = %q, want a SEARCH of %s using idx_jobs_telemetry_outcomes_repo_created", lang, tableTelemetryOutcomes)
	}
	obs := planDetails(t, observedQuery, "tier-1", "code", estimateWindow)
	if !planUses(obs, "d", "idx_jobs_scheduler_decisions_tier_class") {
		t.Errorf("estimate plan = %q, want a SEARCH of the decisions alias d using idx_jobs_scheduler_decisions_tier_class", obs)
	}
}
