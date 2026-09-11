// Package usage implements the provider usage accounting domain
// (P1-E10-W3-S20-T4): per (provider, lane, model, day-bucket) token/cost
// counters, an atomic IncrementUsage upsert, and the QueryUsage/
// TotalCostMicroUSD read surface pkg/provider.UsageReader exposes.
//
// Purpose (this file): Manager, the write-capable accounting surface, and
// IncrementUsage's atomic read-compute-write upsert.
// Inputs: an open *sql.DB already migrated via ApplyMigrationSchema, and
//
//	an injected Clock (never bare time.Now -- Art.7.3) for the bucket date
//	and last_used_at.
//
// Outputs: provider_usage rows; a pkg/cascade taxonomy error.
// Constraints: IncrementUsage computes the new absolute counters inside
//
//	one transaction (SELECT current row, checked-add the delta, UPDATE/
//	INSERT the absolute result) rather than a blind SQL `col = col +
//	excluded.col` upsert -- this is what lets it refuse a negative delta
//	or an overflowing sum with a typed error instead of silently wrapping
//	a signed 64-bit counter (the "money-shaped counters must not silently
//	overflow or go negative" requirement).
//
// SPORT: provider.usage/ADD (P1-E10-W3-S20-T4).
package usage

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Clock abstracts the wall clock (Art.7.3), structurally identical to
// internal/providers/registry.Clock so either concrete type (or
// internal/testkit's) satisfies both with zero adapter code.
type Clock interface {
	Now() time.Time
}

// IncrementRequest is IncrementUsage's input: one dispatch result's
// counters to add to the (ProviderName, LaneName, ModelName, bucket) row,
// bucket being derived from the injected clock at write time.
// LaneName/ModelName empty means the provider-level/lane-level aggregate
// row per the ticket's schema doc. CostMicroUSD is K/S-23.T4's
// pre-computed cost (a nil CostRecord upstream means CostMicroUSD=0,
// passed through here without error -- this domain never computes cost
// itself).
type IncrementRequest struct {
	ProviderName string
	LaneName     string
	ModelName    string
	TokensIn     int64
	TokensOut    int64
	Error        bool
	CostMicroUSD int64
}

// row is one provider_usage row's full column set, used internally by
// IncrementUsage's read-compute-write step and by QueryUsage's scan.
type row struct {
	providerName, laneName, modelName, bucket string
	tokensIn, tokensOut, requests, errors     int64
	costMicroUSD                              int64
	lastUsedAt                                time.Time
}

// Manager is the write-capable usage accounting surface. The zero value is
// not usable; construct with NewManager.
type Manager struct {
	db    *sql.DB
	clock Clock
}

// NewManager returns a Manager persisting through db (already migrated via
// ApplyMigrationSchema) and stamping every write's bucket/last_used_at
// from clk.
func NewManager(db *sql.DB, clk Clock) *Manager {
	return &Manager{db: db, clock: clk}
}

// bucketDate formats t as the YYYY-MM-DD day-bucket key, in UTC so the
// bucket boundary is process-timezone-independent.
func bucketDate(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

// IncrementUsage atomically adds req's counters to the row for
// (req.ProviderName, req.LaneName, req.ModelName, today's UTC bucket),
// creating the row if absent. Idempotent in the sense the ticket means:
// two calls with the same key accumulate (never duplicate rows); it is
// NOT idempotent in the retried-RPC sense (each call's counters are
// added, not deduplicated by a request id -- the ticket names no such id).
func (m *Manager) IncrementUsage(ctx context.Context, req IncrementRequest) error {
	if req.ProviderName == "" {
		return cascade.New(cascade.KindInvalidInput, "usage: provider_name is required")
	}
	if req.TokensIn < 0 || req.TokensOut < 0 || req.CostMicroUSD < 0 {
		return cascade.New(cascade.KindInvalidInput, "usage: token/cost counters must be non-negative")
	}

	bucket := bucketDate(m.clock.Now())
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "usage: increment: begin tx")
	}
	defer func() { _ = tx.Rollback() }()

	cur, ferr := selectRow(ctx, tx, req.ProviderName, req.LaneName, req.ModelName, bucket)
	if ferr != nil {
		return ferr
	}

	next := cur
	next.providerName, next.laneName, next.modelName, next.bucket = req.ProviderName, req.LaneName, req.ModelName, bucket
	if next.tokensIn, err = checkedAdd(cur.tokensIn, req.TokensIn); err != nil {
		return err
	}
	if next.tokensOut, err = checkedAdd(cur.tokensOut, req.TokensOut); err != nil {
		return err
	}
	if next.requests, err = checkedAdd(cur.requests, 1); err != nil {
		return err
	}
	errDelta := int64(0)
	if req.Error {
		errDelta = 1
	}
	if next.errors, err = checkedAdd(cur.errors, errDelta); err != nil {
		return err
	}
	if next.costMicroUSD, err = checkedAdd(cur.costMicroUSD, req.CostMicroUSD); err != nil {
		return err
	}
	next.lastUsedAt = m.clock.Now()

	if err := upsertRow(ctx, tx, next); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "usage: increment: commit")
	}
	return nil
}

// checkedAdd returns a+b, or a cascade.KindIntegrity error if the sum
// would overflow a signed 64-bit counter -- never a silently wrapped
// negative value.
func checkedAdd(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, cascade.New(cascade.KindIntegrity, "usage: counter overflow")
	}
	return a + b, nil
}

// selectRow reads the current row for the given key, or a zero row (all
// counters 0) if none exists yet -- IncrementUsage's "creating the row if
// absent" contract.
func selectRow(ctx context.Context, tx *sql.Tx, provider, lane, model, bucket string) (row, error) {
	r := tx.QueryRowContext(ctx, `
		SELECT tokens_in, tokens_out, requests, errors, cost_micro_usd, last_used_at
		FROM `+tableProviderUsage+`
		WHERE provider_name = ? AND lane_name = ? AND model_name = ? AND bucket = ?`,
		provider, lane, model, bucket)
	var out row
	var lastUsedMs int64
	err := r.Scan(&out.tokensIn, &out.tokensOut, &out.requests, &out.errors, &out.costMicroUSD, &lastUsedMs)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, nil
	}
	if err != nil {
		return row{}, cascade.Wrap(cascade.KindUnavailable, err, "usage: select current row")
	}
	out.lastUsedAt = time.UnixMilli(lastUsedMs).UTC()
	return out, nil
}

// upsertRow writes r's absolute counter values (never a `col = col + ?`
// SQL expression -- see this file's header doc comment for why).
func upsertRow(ctx context.Context, tx *sql.Tx, r row) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO `+tableProviderUsage+`
			(provider_name, lane_name, model_name, bucket, tokens_in, tokens_out, requests, errors, cost_micro_usd, last_used_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider_name, lane_name, model_name, bucket) DO UPDATE SET
			tokens_in=excluded.tokens_in, tokens_out=excluded.tokens_out, requests=excluded.requests,
			errors=excluded.errors, cost_micro_usd=excluded.cost_micro_usd, last_used_at=excluded.last_used_at`,
		r.providerName, r.laneName, r.modelName, r.bucket,
		r.tokensIn, r.tokensOut, r.requests, r.errors, r.costMicroUSD, r.lastUsedAt.UnixMilli())
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "usage: upsert row")
	}
	return nil
}
