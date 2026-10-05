package learn

// Purpose: above-scope aggregation (R-21.167). Rolls the repo-level capability
//   rows up into the lang:<Language> and global levels, keying each repo's
//   contribution on hex(blake3(salt || repo_id)) so nothing an aggregate
//   produces can carry the repo id it was built from, and folds the
//   structured finding counts by {family, category, severity}.
// Inputs: the repo-level rows of jobs_capability_score_observations, each
//   repo's latest language from jobs_telemetry_outcomes, jobs_telemetry_finding
//   counts, an injected runtime.Clock and a salt file (EnsureAggregateSalt).
// Outputs: rewritten lang:/global rows and an AggregateSummary whose
//   contributor keys are the salted hashes.
// Constraints: the salt is 32 random bytes in a 0600 file created once
//   (aggregate_salt.go: temp file then hard link, so no reader or crash sees
//   a partial file). Aggregation refuses typed, with no panic and no
//   fallback salt, when the file is absent (KindNotFound), a symlink, not a
//   regular file of exactly 32 bytes (KindIntegrity) or group/world
//   accessible, or its directory is (KindPermissionDenied). The vault is
//   never read and no text column is read.
// SPORT: internal.learn.Aggregator/ADDED (P1-CAP-03).

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// FindingFold is one folded {family, category, severity} count.
type FindingFold struct {
	Family   Family
	Category Category
	Severity Severity
	Count    int64
}

// AggregateSummary is what one Aggregate pass produced; ContributorKeys are
// the sorted, distinct salted hashes of the repos that fed it.
type AggregateSummary struct {
	Cells           int
	ContributorKeys []string
	Findings        []FindingFold
}

// Aggregator rolls repo-level posteriors up to lang and global.
type Aggregator struct {
	db       *sql.DB
	clock    runtime.Clock
	saltPath string
}

// NewAggregator returns an aggregator over db that reads the salt at saltPath.
func NewAggregator(db *sql.DB, clock runtime.Clock, saltPath string) *Aggregator {
	return &Aggregator{db: db, clock: clock, saltPath: saltPath}
}

// Aggregate validates the salt first (refusing before any read), then
// rewrites every lang:/global row from the repo rows decayed to now, in one
// transaction, and returns the summary. Running it twice at one instant
// yields the same rows.
func (a *Aggregator) Aggregate(ctx context.Context) (AggregateSummary, error) {
	if a == nil || a.db == nil || a.clock == nil {
		return AggregateSummary{}, cascade.New(cascade.KindInvalidInput, "learn: Aggregate requires a constructed Aggregator")
	}
	salt, err := loadSalt(a.saltPath)
	if err != nil {
		return AggregateSummary{}, err
	}
	cells, keys, err := a.rollUp(ctx, salt)
	if err != nil {
		return AggregateSummary{}, err
	}
	folds, err := foldFindings(ctx, a.db)
	if err != nil {
		return AggregateSummary{}, err
	}
	sort.Strings(keys)
	return AggregateSummary{Cells: cells, ContributorKeys: keys, Findings: folds}, nil
}

// cellKey and cellAcc accumulate one aggregate cell.
type cellKey struct {
	scope    ScopeKey
	tc, tier string
}

type cellAcc struct {
	alpha, beta float64
	count       int
}

// repoRow is one repo-level observation row.
type repoRow struct {
	repoID, tc, tier string
	scoreRow
}

// rollUp sums every repo row, decayed to now, into its lang and global cell
// (a repo with no recorded language feeds global only), keys each repo's
// contribution on its salted hash, and rewrites the lang:/global rows in one
// transaction. It returns the cell count and the distinct hash keys.
func (a *Aggregator) rollUp(ctx context.Context, salt []byte) (int, []string, error) {
	langs, err := repoLanguages(ctx, a.db)
	if err != nil {
		return 0, nil, err
	}
	repoRows, err := readRepoRows(ctx, a.db)
	if err != nil {
		return 0, nil, err
	}
	now := a.clock.Now().UTC()
	acc, contrib := map[cellKey]*cellAcc{}, map[string]struct{}{}
	for _, r := range repoRows {
		contrib[hashRepoID(salt, r.repoID)] = struct{}{}
		scopes := []ScopeKey{scopeGlobal}
		if lang, ok := langs[r.repoID]; ok {
			scopes = append(scopes, langScope(lang))
		}
		for _, sc := range scopes {
			k := cellKey{sc, r.tc, r.tier}
			if acc[k] == nil {
				acc[k] = &cellAcc{}
			}
			acc[k].alpha += decayed(r.alpha, r.lastUpdated, now)
			acc[k].beta += decayed(r.beta, r.lastUpdated, now)
			acc[k].count += r.count
		}
	}
	keys := make([]string, 0, len(contrib))
	for k := range contrib {
		keys = append(keys, k)
	}
	return len(acc), keys, a.rewrite(ctx, acc, now)
}

// readRepoRows loads every repo-level row, closing the cursor before return.
func readRepoRows(ctx context.Context, db *sql.DB) ([]repoRow, error) {
	rows, err := db.QueryContext(ctx, `SELECT scope_key, task_class, tier, alpha, beta, observation_count, last_updated FROM `+
		tableCapabilityScore+` WHERE scope_key LIKE 'repo:%'`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: read repo capability rows for aggregation")
	}
	defer func() { _ = rows.Close() }()
	var out []repoRow
	for rows.Next() {
		var r repoRow
		var scope string
		var last int64
		if err := rows.Scan(&scope, &r.tc, &r.tier, &r.alpha, &r.beta, &r.count, &last); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: scan repo capability row")
		}
		r.repoID, r.lastUpdated = strings.TrimPrefix(scope, scopeRepoPrefix), time.Unix(last, 0).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: iterate repo capability rows")
	}
	return out, nil
}

// rewrite replaces every lang:/global row with acc in one transaction.
func (a *Aggregator) rewrite(ctx context.Context, acc map[cellKey]*cellAcc, now time.Time) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: begin aggregation transaction")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+tableCapabilityScore+` WHERE scope_key = 'global' OR scope_key LIKE 'lang:%'`); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: clear aggregate capability rows")
	}
	for k, c := range acc {
		if _, err := tx.ExecContext(ctx, `INSERT INTO `+tableCapabilityScore+
			` (scope_key, task_class, tier, alpha, beta, observation_count, last_updated) VALUES (?,?,?,?,?,?,?)`,
			string(k.scope), k.tc, k.tier, c.alpha, c.beta, c.count, now.Unix()); err != nil {
			return cascade.Wrap(cascade.KindUnavailable, err, "learn: write aggregate capability row")
		}
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "learn: commit aggregation")
	}
	return nil
}

// repoLanguages maps each repo id to the language of its latest outcome.
func repoLanguages(ctx context.Context, db *sql.DB) (map[string]Language, error) {
	rows, err := db.QueryContext(ctx, `SELECT repo_id, language FROM `+tableTelemetryOutcomes+` ORDER BY created_at, id`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: read repo languages for aggregation")
	}
	defer func() { _ = rows.Close() }()
	out := map[string]Language{}
	for rows.Next() {
		var repo, raw string
		if err := rows.Scan(&repo, &raw); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: scan repo language")
		}
		if lang, derr := DecodeLanguage(raw); derr == nil {
			out[repo] = lang
		}
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: iterate repo languages")
	}
	return out, nil
}

// foldFindings sums jobs_telemetry_finding counts by {family, category,
// severity}. Only the three closed-enum columns and count are read.
func foldFindings(ctx context.Context, db *sql.DB) ([]FindingFold, error) {
	rows, err := db.QueryContext(ctx, `SELECT family, category, severity, SUM(count) FROM `+tableTelemetryFinding+
		` GROUP BY family, category, severity ORDER BY family, category, severity`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: fold finding counts")
	}
	defer func() { _ = rows.Close() }()
	var out []FindingFold
	for rows.Next() {
		var f FindingFold
		if err := rows.Scan(&f.Family, &f.Category, &f.Severity, &f.Count); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: scan finding fold")
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: iterate finding folds")
	}
	return out, nil
}
