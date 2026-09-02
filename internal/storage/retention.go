// Purpose: the retention subsystem — DomainPruner.Prune (windowed batched
//
//	DELETE per domain) and VacuumJob.Run (WAL checkpoint + VACUUM) — on
//	top of the closed R-14.5 domain layout B/S-03.T1 established
//	(domains.go). Both are report-returning methods with no scheduler
//	dependency: C/S-04.T4's cron scheduler (not yet in-tree) wraps them
//	in func(context.Context) error closures at ITS OWN registration
//	site. This ticket ships the runnables only.
//
// Inputs: RetentionConfig (per-domain retention window + registered
//
//	PruneTarget list + VacuumInterval + injected Clock), an open *sql.DB
//	against a cascade.db-shaped database.
//
// Outputs: []PruneReport / PruneReport, VacuumReport, or a *cascade.Error.
// Constraints: as of this ticket, every AllDomains anchor table
//
//	(domain_root, see domains.go) is schema `(id INTEGER PRIMARY KEY)` —
//	it carries NO row-timestamp column, because each domain's real
//	per-row schema is landed by that domain's own owning epic in a later
//	ticket (see AllDomains' OwnerPkg column), not by this one. Retention
//	therefore does not hard-code "prune domain_root" — it prunes exactly
//	the tables a caller REGISTERS via RetentionConfig.Targets, so it
//	never invents a timestamp column that does not exist and never goes
//	stale when a later ticket adds one (R-14.107's "no invented
//	defaults" precedent, applied to schema rather than to config keys:
//	where the closed domain layout declares no row-timestamp column,
//	there is none, and this package does not pretend otherwise). A
//	domain with a non-zero window but no registered target is a
//	configuration error, reported per-domain, not a silent no-op — see
//	Prune's doc comment.
//
//	This ticket's files_scope forbids editing providers/sqlite, so it
//	cannot reach WriteExecutor's unexported submit path (Driver.Tx only
//	exposes the namespace/key provider.Tx interface, never raw SQL); nor
//	can it reach internal/storage/migrate. Retention therefore writes
//	directly against the injected *sql.DB, the SAME pattern domains.go's
//	Bootstrap and health.go's StorageHealthCheck already use for this
//	package's raw-SQL domain-table layer — capability.go documents this
//	identical gap for the same reason ("what is NOT yet consumed by a
//	real caller is the composition-root wiring that turns [this] into
//	providers/sqlite's ... seam"). VacuumJob.Run's doc comment
//	(retention_vacuum.go, split under R-14.117) states precisely what
//	that means for the single-writer invariant.
//
// SPORT: internal.storage.retention.DomainPruner/ADDED,
//
//	internal.storage.retention.VacuumJob/ADDED,
//	internal.storage.retention.RetentionConfig/ADDED (P1-E02-W1-S03-T2).

package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// defaultVacuumInterval is the "weekly VACUUM job" cadence the plan names
// (168h = 7 * 24h). Normalize applies this when VacuumInterval is unset —
// golden-asserted by TestRetentionConfig_Normalize_WeeklyDefault.
const defaultVacuumInterval = 168 * time.Hour

// defaultPruneBatchCap is the number of rows one DELETE round-trip removes
// before Prune re-checks and issues another, so a single prune call never
// holds *sql.DB's write path against a large delete set in one statement.
// Overridable per-config via RetentionConfig.BatchCap (0 = use this
// default, applied by Normalize).
const defaultPruneBatchCap = 500

// PruneTarget names one concrete (table, timestamp-column) pair Prune may
// delete rows from for a domain. TimestampColumn stores whole-second Unix
// time as an INTEGER — the same convention domains.go's stampSchemaVersion
// already uses for applied_at — so cutoff comparison is a plain integer
// WHERE clause, no datetime() parsing. A domain may register zero, one, or
// several targets (e.g. one per real table a later ticket adds under that
// domain); Prune deletes from each independently and sums their
// RowsDeleted into one PruneReport per domain.
type PruneTarget struct {
	// Table is the exact table name (never user input; always a
	// caller-registered literal), quoted via quoteIdent before use.
	// Validated at Prune time (R-14.144, retention_validate.go) against
	// the owning DomainID's TablePrefix — a table that does not belong
	// to that domain is refused as a configuration error before any
	// DELETE is issued, never a silently-wrong prune of a neighbouring
	// domain's rows.
	Table string
	// TimestampColumn is the INTEGER (Unix-seconds) column compared
	// against the retention cutoff. Validated at Prune time
	// (retention_validate.go) to have INTEGER affinity — a wrong-type
	// column (e.g. TEXT holding ISO8601 text) is refused rather than
	// silently comparing false forever. WARNING: zero and negative
	// values in this column are NOT treated as "unset" — they compare as
	// maximally old and WILL be pruned on the very first Prune call. A
	// future domain that uses 0 as a sentinel for "no timestamp yet"
	// will lose those rows; such a domain must use a genuinely absent
	// (NULL) value instead, which this column's NOT NULL-agnostic
	// comparison correctly treats as surviving (NULL < cutoff is never
	// true).
	TimestampColumn string
}

// RetentionConfig configures one DomainPruner.Prune / VacuumJob.Run pair.
// Reading these fields from the TOML [storage] config (C/S-04.T1) is out
// of this ticket's scope (08-INIT-CONFIG-SPEC.md §3 declares no retention
// key today — R-14.107's precedent: where the spec declares none, there
// is none) — RetentionConfig is constructed by the caller.
type RetentionConfig struct {
	// DomainRetention maps a domain to its retention window. A zero
	// value (the map default for an absent key, or an explicit 0) means
	// "never prune this domain" — Prune skips it entirely, issuing no
	// DELETE and returning RowsDeleted: 0, nil error for it.
	DomainRetention map[DomainID]time.Duration
	// Targets registers, per domain, which (table, timestamp column)
	// pairs Prune may delete from. A domain with a non-zero
	// DomainRetention window but no entry here is a configuration
	// error for that domain (see Prune's doc comment) — never a silent
	// no-op, and never a guess at domain_root's nonexistent timestamp
	// column.
	Targets map[DomainID][]PruneTarget
	// VacuumInterval is the cadence C/S-04.T4's scheduler is meant to
	// run VacuumJob at. A zero value is normalized to
	// defaultVacuumInterval (168h, weekly) by Normalize. This field is
	// documentation/config plumbing only — nothing in this ticket reads
	// it to decide when to run; there is no scheduler yet (see
	// package doc).
	VacuumInterval time.Duration
	// BatchCap is the max rows one DELETE round-trip removes. Zero is
	// normalized to defaultPruneBatchCap (500) by Normalize.
	BatchCap int
	// Clock supplies "now" for the retention cutoff (now - window).
	// Required — Prune returns a KindInvalidInput error if nil rather
	// than silently falling back to the wall clock (Art.7.3, R-14.136:
	// time-dependent logic is driven by an injected clock, never a
	// sleep or a bare time.Now).
	Clock Clock
}

// Normalize returns a copy of cfg with VacuumInterval and BatchCap's zero
// values replaced by their documented defaults (168h weekly, 500-row
// batch cap respectively). Normalize never mutates cfg itself and never
// touches DomainRetention, Targets, or Clock — those have no default to
// apply (a zero DomainRetention window is meaningful, not "unset").
func (cfg RetentionConfig) Normalize() RetentionConfig {
	out := cfg
	if out.VacuumInterval == 0 {
		out.VacuumInterval = defaultVacuumInterval
	}
	if out.BatchCap == 0 {
		out.BatchCap = defaultPruneBatchCap
	}
	return out
}

// PruneReport summarizes one domain's Prune pass.
type PruneReport struct {
	Domain      DomainID
	RowsDeleted int
	Elapsed     time.Duration
	// Err is this domain's own failure, if any — nil on success. Prune
	// also returns a joined error aggregating every non-nil Err across
	// domains (see Prune's doc comment); Err on the individual report is
	// what lets a caller identify WHICH domain failed without string-
	// matching the joined error's text.
	Err error
}

// VacuumReport summarizes one VacuumJob.Run call.
type VacuumReport struct {
	FileSizeBefore int64
	FileSizeAfter  int64
	Elapsed        time.Duration
}

// DomainPruner runs RetentionConfig-driven windowed prunes. The zero value
// is ready to use (DomainPruner carries no state of its own — every input
// is a Prune argument).
type DomainPruner struct{}

// Prune iterates AllDomains in their deterministic declaration order (see
// domains.go) and, for each domain whose cfg.DomainRetention window is
// non-zero, deletes rows older than the window from every PruneTarget
// cfg.Targets registers for that domain. Skip rules, in order:
//
//   - DomainRetention[id] == 0 (including the map-default zero value for
//     an unlisted domain): skip, PruneReport{RowsDeleted: 0}, nil Err —
//     the documented no-op, never issuing any DELETE.
//   - DomainRetention[id] != 0 but Targets[id] is empty: this domain
//     wanted pruning but has nothing registered to prune — a
//     configuration error (KindInvalidInput), reported as this domain's
//     Err and folded into the joined return error, WITHOUT skipping any
//     sibling domain.
//
// A per-target DELETE failure (e.g. the table does not exist, or the
// database rejects the write) is likewise recorded as that domain's Err
// and folded into the joined error — Prune never short-circuits the
// whole run on one domain's failure (Art.1: sibling domains still get
// their real prune, proven by TestPruneAggregateError). The returned
// error is nil iff every domain's Err is nil.
//
// Idempotency (§5.9): a target with nothing left older than the cutoff
// deletes 0 rows and returns nil Err — calling Prune twice in a row against
// the same domain leaves the second call's RowsDeleted at 0 for every
// target, never negative, never an error, proven by
// TestPruneIdempotent.
func (DomainPruner) Prune(ctx context.Context, db *sql.DB, cfg RetentionConfig) ([]PruneReport, error) {
	if cfg.Clock == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "storage: Prune requires a non-nil Clock")
	}
	cfg = cfg.Normalize()
	if err := validateBatchCap(cfg.BatchCap); err != nil {
		return nil, err
	}

	reports := make([]PruneReport, 0, len(AllDomains))
	var errs []error
	for _, meta := range AllDomains {
		report := pruneDomain(ctx, db, cfg, meta)
		reports = append(reports, report)
		if report.Err != nil {
			errs = append(errs, fmt.Errorf("domain %s: %w", meta.ID, report.Err))
		}
	}
	if len(errs) > 0 {
		return reports, cascade.Wrap(cascade.KindUnavailable, errors.Join(errs...), "storage: prune had per-domain failures")
	}
	return reports, nil
}
