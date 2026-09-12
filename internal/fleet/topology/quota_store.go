// Purpose: the sessions_quota_bucket and sessions_quota_snapshot TableDefs
//
//	(both in the EXISTING `sessions` storage domain, R-21.22 -- table
//	prefix only, no new DomainID) and QuotaStore, the CRUD surface over
//	them. sessions_quota_bucket is a READ-ONLY OBSERVATION CACHE per
//	R-21.114: UpsertBucket is its only writer, called from a provider-
//	observation reconciliation path, never from a dispatch/reservation
//	path (availability.go's Available takes no store reference at all,
//	which is what makes that guarantee structural rather than
//	conventional).
//
// Inputs: a *sql.DB already migrated via ApplyMigrationSchema, and an
//
//	injected clock for snapshot timestamps.
//
// Outputs: Bucket/QuotaSnapshot rows, or a pkg/cascade taxonomy error.
// Constraints: buckets upsert by (domain_id, dimension); snapshot rows are
//
//	append-only (no UpdateSnapshot/DeleteSnapshot method exists).
//
// SPORT: fleet/topology/quota_store/ADD (P1-E40-W9-S77-T3).

package topology

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/acamarata/cascade/internal/storage/migrate"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Table names, prefixed with the `sessions` domain's TablePrefix
// (internal/storage/domains.go's DomainSessions entry), matching this
// package's own schema.go "<domain>_<table>" convention.
const (
	tableQuotaBucket   = "sessions_quota_bucket"
	tableQuotaSnapshot = "sessions_quota_snapshot"
)

// quotaBucketTable is the sessions_quota_bucket TableDef: one row per
// (domain_id, dimension), composite primary key.
func quotaBucketTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableQuotaBucket,
		Columns: []migrate.ColumnDef{
			{Name: "domain_id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "dimension", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "name", Type: migrate.TypeText, NotNull: true},
			{Name: "bucket_limit", Type: migrate.TypeInteger, NotNull: true},
			{Name: "remaining_fraction", Type: migrate.TypeReal, NotNull: true},
			{Name: "reset_at", Type: migrate.TypeInteger},
			{Name: "window", Type: migrate.TypeText, NotNull: true},
			{Name: "source", Type: migrate.TypeText, NotNull: true},
			{Name: "confidence", Type: migrate.TypeReal, NotNull: true},
			{Name: "observed_at", Type: migrate.TypeInteger},
			{Name: "limit_scope_id", Type: migrate.TypeText, NotNull: true},
			{Name: "capacity_observed", Type: migrate.TypeInteger, NotNull: true},
			{Name: "committed_since_observation", Type: migrate.TypeInteger, NotNull: true},
			{Name: "window_id", Type: migrate.TypeText, NotNull: true},
			{Name: "version", Type: migrate.TypeInteger, NotNull: true},
		},
	}
}

// quotaSnapshotTable is the sessions_quota_snapshot TableDef: append-only,
// one row per Snapshot(ctx) call.
func quotaSnapshotTable() migrate.TableDef {
	return migrate.TableDef{
		Name: tableQuotaSnapshot,
		Columns: []migrate.ColumnDef{
			{Name: "id", Type: migrate.TypeText, PrimaryKey: true, NotNull: true},
			{Name: "taken_at", Type: migrate.TypeInteger, NotNull: true},
			{Name: "payload", Type: migrate.TypeText, NotNull: true},
		},
	}
}

// QuotaStore is the CRUD surface over sessions_quota_bucket and
// sessions_quota_snapshot. The zero value is not usable; construct with
// NewQuotaStore.
type QuotaStore struct {
	db execer
}

// NewQuotaStore returns a QuotaStore persisting through db (already
// migrated via ApplyMigrationSchema).
func NewQuotaStore(db *sql.DB) *QuotaStore { return &QuotaStore{db: db} }

const quotaBucketColumns = "domain_id, dimension, name, bucket_limit, remaining_fraction, reset_at, window, source, confidence, observed_at, limit_scope_id, capacity_observed, committed_since_observation, window_id, version"

// UpsertBucket reconciles one observation into sessions_quota_bucket,
// upserting by (domain_id, dimension) -- the ONLY writer this table has.
// b must pass NewBucket's validation.
func (s *QuotaStore) UpsertBucket(ctx context.Context, domainID DomainID, dimension string, b Bucket) error {
	if _, err := NewBucket(b); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO `+tableQuotaBucket+` (`+quotaBucketColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(domain_id, dimension) DO UPDATE SET
  name=excluded.name, bucket_limit=excluded.bucket_limit,
  remaining_fraction=excluded.remaining_fraction, reset_at=excluded.reset_at,
  window=excluded.window, source=excluded.source, confidence=excluded.confidence,
  observed_at=excluded.observed_at, limit_scope_id=excluded.limit_scope_id,
  capacity_observed=excluded.capacity_observed,
  committed_since_observation=excluded.committed_since_observation,
  window_id=excluded.window_id, version=excluded.version`,
		string(domainID), dimension, b.Name, b.Limit, b.RemainingFraction, unixOrNil(b.ResetAt),
		string(b.Window), string(b.Source), b.Confidence, unixOrNil(b.ObservedAt),
		string(b.LimitScopeID), b.CapacityObserved, b.CommittedSinceObservation, b.WindowID, b.Version)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "topology: upsert quota bucket")
	}
	return nil
}

// GetBucket reads one (domain_id, dimension) row.
func (s *QuotaStore) GetBucket(ctx context.Context, domainID DomainID, dimension string) (Bucket, error) {
	return getRow(ctx, s.db, `SELECT `+quotaBucketColumns+` FROM `+tableQuotaBucket+` WHERE domain_id=? AND dimension=?`,
		[]any{string(domainID), dimension}, scanQuotaBucket, newNotFoundErr("quota_bucket", string(domainID)+":"+dimension))
}

// ListBuckets reads every bucket row for one domain. listRows (schema.go)
// takes no query args, so this domain-scoped read queries directly rather
// than through that generic helper.
func (s *QuotaStore) ListBuckets(ctx context.Context, domainID DomainID) ([]Bucket, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+quotaBucketColumns+` FROM `+tableQuotaBucket+` WHERE domain_id=? ORDER BY dimension`, string(domainID))
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "topology: list quota buckets")
	}
	defer func() { _ = rows.Close() }()
	out := make([]Bucket, 0)
	for rows.Next() {
		b, serr := scanQuotaBucket(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanQuotaBucket(row rowScanner) (Bucket, error) {
	var (
		domainID, dimension, name, window, source, limitScope, windowID string
		limit, capacityObserved, committedSince, version                int64
		remaining, confidence                                           float64
		resetAt, observedAt                                             sql.NullInt64
	)
	err := row.Scan(&domainID, &dimension, &name, &limit, &remaining, &resetAt, &window, &source, &confidence,
		&observedAt, &limitScope, &capacityObserved, &committedSince, &windowID, &version)
	if err != nil {
		return Bucket{}, wrapScan(err, "quota_bucket")
	}
	return Bucket{
		Name: name, Limit: limit, RemainingFraction: remaining, ResetAt: nullUnixToTime(resetAt),
		Window: BucketWindow(window), Source: BucketSource(source), Confidence: confidence,
		ObservedAt: nullUnixToTime(observedAt), LimitScopeID: LimitScopeID(limitScope),
		CapacityObserved: capacityObserved, CommittedSinceObservation: committedSince,
		WindowID: windowID, Version: version,
	}, nil
}

// AppendSnapshot writes one append-only sessions_quota_snapshot row.
func (s *QuotaStore) AppendSnapshot(ctx context.Context, id string, snap QuotaSnapshot) error {
	payload, err := json.Marshal(snap)
	if err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "topology: marshal quota snapshot")
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO `+tableQuotaSnapshot+` (id, taken_at, payload) VALUES (?, ?, ?)`,
		id, snap.TakenAt.UnixMilli(), string(payload))
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "topology: append quota snapshot")
	}
	return nil
}
