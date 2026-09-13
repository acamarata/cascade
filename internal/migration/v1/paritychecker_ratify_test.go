// Purpose: the divergence-ledger ratification path and the loader/
// constructor error paths — split from paritychecker_test.go under the
// 300-line file cap.
//
// SPORT: migration/v1/paritychecker/ADD (P1-E26-W10-S53-T2).
package v1

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/acamarata/cascade/internal/retrieval/recall"
)

// ratifiedQuerier always answers with c-actual, never c-expected, so every
// query it serves has exactly one missing hash: c-expected.
type ratifiedQuerier struct{}

func (ratifiedQuerier) Query(_ context.Context, req recall.Request) (recall.Response, error) {
	return recall.Response{Query: req.Query, Results: []recall.Result{{ChunkID: "c-actual", Rank: 1}}}, nil
}

// TestParityChecker_RatifiedDivergencePasses proves the tripwire pattern:
// a query whose missing/surplus set exactly matches a divergence-ledger
// row is marked Ratified and does not fail the overall report, while the
// same divergence with no ledger row does fail it.
func TestParityChecker_RatifiedDivergencePasses(t *testing.T) {
	checker, err := NewParityChecker(ratifiedQuerier{}, silentLog())
	if err != nil {
		t.Fatalf("NewParityChecker: %v", err)
	}
	queries := []GoldenQuery{
		{QueryID: "q1", Query: "anything", Scope: "project/migration-golden", K: 3,
			ExpectedHashes: []string{"c-expected"}},
	}

	unratified := checker.Check(context.Background(), queries, nil)
	if unratified.Pass {
		t.Error("an unratified divergence must fail the report")
	}

	ledger := []DivergenceEntry{{
		QueryID: "q1", MissingHashes: []string{"c-expected"}, SurplusHashes: []string{"c-actual"},
		Rationale: "test-only: proves the ratification path", RatifiedByTicket: "P1-E26-W10-S53-T2",
	}}
	ratified := checker.Check(context.Background(), queries, ledger)
	if !ratified.Pass {
		t.Errorf("a ratified divergence must pass: %s", ratified.Summary())
	}
	if len(ratified.Queries) != 1 || !ratified.Queries[0].Ratified {
		t.Errorf("Queries = %+v, want the one query marked Ratified", ratified.Queries)
	}
	if !strings.Contains(unratified.Summary(), "q1") {
		t.Errorf("Summary() of a failing report must name the failing query:\n%s", unratified.Summary())
	}
}

// TestNewParityChecker_RefusesMissingCollaborators covers the
// constructor's fail-closed validation.
func TestNewParityChecker_RefusesMissingCollaborators(t *testing.T) {
	if _, err := NewParityChecker(nil, silentLog()); err == nil {
		t.Error("NewParityChecker(nil querier, ...) must refuse")
	}
	if _, err := NewParityChecker(ratifiedQuerier{}, nil); err == nil {
		t.Error("NewParityChecker(..., nil logger) must refuse")
	}
}

// TestLoadGoldenQueries_ErrorPaths covers the loader's typed refusals: a
// missing fixture directory and a malformed JSON file.
func TestLoadGoldenQueries_ErrorPaths(t *testing.T) {
	if _, err := LoadGoldenQueries(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("LoadGoldenQueries over a missing directory must refuse")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, goldenQueriesFileName), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write malformed fixture: %v", err)
	}
	if _, err := LoadGoldenQueries(dir); err == nil {
		t.Error("LoadGoldenQueries over malformed JSON must refuse")
	}
}

// TestLoadDivergenceLedger_ErrorPaths covers the ledger loader: a missing
// file is a real empty ledger, and malformed YAML is refused.
func TestLoadDivergenceLedger_ErrorPaths(t *testing.T) {
	empty, err := LoadDivergenceLedger(t.TempDir())
	if err != nil || empty != nil {
		t.Errorf("LoadDivergenceLedger over a directory with no ledger file = (%v, %v), want (nil, nil)", empty, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, divergenceLedgerFileName), []byte(": not: valid: yaml: ["), 0o644); err != nil {
		t.Fatalf("write malformed ledger: %v", err)
	}
	if _, err := LoadDivergenceLedger(dir); err == nil {
		t.Error("LoadDivergenceLedger over malformed YAML must refuse")
	}
}
