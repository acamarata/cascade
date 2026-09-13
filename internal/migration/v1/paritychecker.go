// Package v1 uses this file to check the rebuilt recall index against v1
// golden recall queries.
//
// Purpose: load the v1 golden query fixtures (testdata/v1-goldens/recall),
// execute each one against the rebuilt index via the real F/S-11 RRF
// query surface (internal/retrieval/recall.Service), and compute the
// per-query content-hash coverage R-14.82 requires to be exact.
// Inputs: GoldenQuery fixtures (query text, K, expected content hashes)
// and DivergenceEntry ledger rows (query id, missing/surplus hashes, a
// ratifying rationale and the ticket that ratified it).
// Outputs: a ParityReport: one QueryCoverage per golden query plus a
// summary Pass — true only when every query's coverage is exact or its
// exact divergence is present in the ledger.
// Constraints: a query that errors (a corrupted vector entry, an
// unavailable leg) is recorded as a failed QueryCoverage and logged; it
// never aborts the remaining queries and never panics.
//
// SPORT: migration/v1/paritychecker/ADD (P1-E26-W10-S53-T2).
package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/pkg/cascade"
	"gopkg.in/yaml.v3"
)

// goldenQueriesFileName is the golden query fixture's name within a
// v1-goldens/recall directory.
const goldenQueriesFileName = "queries.json"

// divergenceLedgerFileName is the divergence ledger's name within a
// v1-goldens/recall directory.
const divergenceLedgerFileName = "divergence-ledger.yaml"

// GoldenQuery is one v1 recall query harvested as a fixture: the query
// text, how many results it asked for, and the content hashes v1's own
// result set carried for it (empty when v1 itself found nothing).
type GoldenQuery struct {
	QueryID        string   `json:"query_id"`
	Query          string   `json:"query"`
	Scope          string   `json:"scope"`
	K              int      `json:"k"`
	ExpectedHashes []string `json:"expected_content_hashes"`
}

// DivergenceEntry is one ratified divergence: a query whose v2 result set
// does not exactly match ExpectedHashes, with the missing/surplus hashes
// named and a stated rationale for why the difference is accepted rather
// than a defect (the tripwire pattern R-14.82 requires).
type DivergenceEntry struct {
	QueryID          string   `yaml:"query_id"`
	MissingHashes    []string `yaml:"missing_hashes"`
	SurplusHashes    []string `yaml:"surplus_hashes"`
	Rationale        string   `yaml:"rationale"`
	RatifiedByTicket string   `yaml:"ratified_by_ticket"`
}

// QueryCoverage is one golden query's outcome.
type QueryCoverage struct {
	QueryID  string
	Coverage float64
	Missing  []string
	Surplus  []string
	Ratified bool
	Err      error
}

// ParityReport is the full golden set's outcome.
type ParityReport struct {
	Queries []QueryCoverage
	Pass    bool
}

// Querier is the query-time surface ParityChecker drives: exactly
// recall.Service's Query method. It is an interface, not the concrete
// type, so a test can substitute a double that fails on demand (the
// "corrupted vector entry" error path) without needing to actually
// corrupt a real vector store; production callers pass a real
// *recall.Service.
type Querier interface {
	Query(ctx context.Context, req recall.Request) (recall.Response, error)
}

// ParityChecker runs golden queries against a Querier and scores them.
type ParityChecker struct {
	recall Querier
	log    *slog.Logger
}

// NewParityChecker returns a ParityChecker over recallSvc. log is
// required; pass slog.New(slog.NewTextHandler(io.Discard, nil)) for a
// silent one.
func NewParityChecker(recallSvc Querier, log *slog.Logger) (*ParityChecker, error) {
	if recallSvc == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "migration v1 recall: no recall querier")
	}
	if log == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "migration v1 recall: no logger")
	}
	return &ParityChecker{recall: recallSvc, log: log}, nil
}

// LoadGoldenQueries reads queries.json from dir.
func LoadGoldenQueries(dir string) ([]GoldenQuery, error) {
	path := filepath.Join(dir, goldenQueriesFileName)
	data, err := os.ReadFile(path) //nolint:gosec // dir is caller-supplied fixture root, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, cascade.Newf(cascade.KindNotFound, "migration v1 recall: no golden queries at %s", path)
		}
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "migration v1 recall: read %s", path)
	}
	var queries []GoldenQuery
	if err := json.Unmarshal(data, &queries); err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "migration v1 recall: parse %s", path)
	}
	return queries, nil
}

// LoadDivergenceLedger reads divergence-ledger.yaml from dir. A missing
// file is a real, empty ledger (no divergence has ever been ratified),
// not an error.
func LoadDivergenceLedger(dir string) ([]DivergenceEntry, error) {
	path := filepath.Join(dir, divergenceLedgerFileName)
	data, err := os.ReadFile(path) //nolint:gosec // dir is caller-supplied fixture root, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, cascade.Wrapf(cascade.KindUnavailable, err, "migration v1 recall: read %s", path)
	}
	var ledger []DivergenceEntry
	if err := yaml.Unmarshal(data, &ledger); err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err, "migration v1 recall: parse %s", path)
	}
	return ledger, nil
}

// Check runs every query in queries against the checker's Querier and
// scores it against ledger's ratified divergences. Pass is true only when
// every query is exact or its exact divergence is ratified.
func (p *ParityChecker) Check(
	ctx context.Context, queries []GoldenQuery, ledger []DivergenceEntry,
) ParityReport {
	byQuery := ledgerByQuery(ledger)
	report := ParityReport{Pass: true}
	for _, q := range queries {
		cov := p.checkOne(ctx, q, byQuery[q.QueryID])
		if !cov.Ratified && (len(cov.Missing) > 0 || cov.Err != nil) {
			report.Pass = false
		}
		report.Queries = append(report.Queries, cov)
	}
	return report
}

// checkOne runs one query. A Querier error is never fatal to the report:
// it is logged and recorded on the QueryCoverage, and Check moves on to
// the next query.
func (p *ParityChecker) checkOne(ctx context.Context, q GoldenQuery, ratified *DivergenceEntry) QueryCoverage {
	k := q.K
	if k <= 0 {
		k = recall.DefaultK
	}
	resp, err := p.recall.Query(ctx, recall.Request{Query: q.Query, Scope: q.Scope, K: k})
	if err != nil {
		p.log.Warn("migration v1 recall: golden query failed", "query_id", q.QueryID, "error", err)
		return QueryCoverage{QueryID: q.QueryID, Err: err}
	}
	actual := make(map[string]bool, len(resp.Results))
	for _, r := range resp.Results {
		actual[r.ChunkID] = true
	}
	ratio, missing := coverageOf(q.ExpectedHashes, actual)
	surplus := surplusOf(q.ExpectedHashes, actual)
	cov := QueryCoverage{QueryID: q.QueryID, Coverage: ratio, Missing: missing, Surplus: surplus}
	if ratified != nil && sameSet(ratified.MissingHashes, missing) && sameSet(ratified.SurplusHashes, surplus) {
		cov.Ratified = true
		p.log.Warn("migration v1 recall: ratified golden divergence",
			"query_id", q.QueryID, "ticket", ratified.RatifiedByTicket, "rationale", ratified.Rationale)
	}
	return cov
}

// coverageOf reports the fraction of expected that actual contains, and
// the expected hashes actual is missing. A query with no expected hashes
// (v1 itself found nothing) is vacuously fully covered: there is nothing
// for v2 to have missed.
func coverageOf(expected []string, actual map[string]bool) (float64, []string) {
	if len(expected) == 0 {
		return 1.0, nil
	}
	var found int
	var missing []string
	for _, h := range expected {
		if actual[h] {
			found++
		} else {
			missing = append(missing, h)
		}
	}
	return float64(found) / float64(len(expected)), missing
}

// surplusOf returns the actual hashes not present in expected.
func surplusOf(expected []string, actual map[string]bool) []string {
	want := make(map[string]bool, len(expected))
	for _, h := range expected {
		want[h] = true
	}
	var surplus []string
	for h := range actual {
		if !want[h] {
			surplus = append(surplus, h)
		}
	}
	sort.Strings(surplus)
	return surplus
}

// ledgerByQuery indexes ledger by query id for O(1) lookup during Check.
func ledgerByQuery(ledger []DivergenceEntry) map[string]*DivergenceEntry {
	out := make(map[string]*DivergenceEntry, len(ledger))
	for i := range ledger {
		out[ledger[i].QueryID] = &ledger[i]
	}
	return out
}

// sameSet reports whether a and b hold the same strings, ignoring order.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as, bs := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// Summary renders a one-line-per-query diff report, for a failing Check's
// caller to print: exactly the missing/surplus diff the ticket requires a
// failed parity check to emit.
func (r ParityReport) Summary() string {
	var s string
	for _, q := range r.Queries {
		if q.Err != nil {
			s += fmt.Sprintf("%s: ERROR %v\n", q.QueryID, q.Err)
			continue
		}
		if q.Coverage >= 1.0 || q.Ratified {
			continue
		}
		s += fmt.Sprintf("%s: coverage=%.2f missing=%v surplus=%v\n", q.QueryID, q.Coverage, q.Missing, q.Surplus)
	}
	return s
}
