// Purpose: QueryUsage/TotalCostMicroUSD (the read half of the usage
//   accounting domain) and Reader, the pkg/provider.UsageReader adapter.
//
// FILE-SCOPE DEVIATION (recorded, mirrors internal/providers/registry/
// atomic_health.go's identically-reasoned note). The ticket's files_scope
// names usage.go/migration.go as this package's only production files,
// but usage.go is already 193 of its 300-line cap -- adding the read
// surface there would exceed it. This file continues the read/write
// split internal/providers/registry already uses (registry.go vs.
// lanes.go/pool.go).
//
// Inputs: an open, migrated *sql.DB (via the Manager the caller already
//   holds) and a provider.UsageFilter.
// Outputs: []provider.UsageSummary / int64, or a pkg/cascade taxonomy
//   error.
// Constraints: QueryUsage orders provider_name -> lane_name -> model_name
//   -> bucket DESC (the ticket's stated stable order); TotalCostMicroUSD
//   is a plain SUM over the same WHERE clause QueryUsage builds, so the
//   two can never disagree on which rows are "in filter".
// SPORT: provider.usage/ADD (P1-E10-W3-S20-T4); pkg.provider.usage_reader/ADD.

package usage

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// buildFilterWhere turns a provider.UsageFilter into a SQL WHERE clause
// (possibly empty) and its bind args, shared by QueryUsage and
// TotalCostMicroUSD so the two can never diverge on row selection.
func buildFilterWhere(filter provider.UsageFilter) (string, []any) {
	var clauses []string
	var args []any
	if filter.ProviderName != "" {
		clauses = append(clauses, "provider_name = ?")
		args = append(args, filter.ProviderName)
	}
	if filter.LaneName != "" {
		clauses = append(clauses, "lane_name = ?")
		args = append(args, filter.LaneName)
	}
	if filter.ModelName != "" {
		clauses = append(clauses, "model_name = ?")
		args = append(args, filter.ModelName)
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, "bucket >= ?")
		args = append(args, bucketDate(filter.Since))
	}
	if !filter.Until.IsZero() {
		clauses = append(clauses, "bucket <= ?")
		args = append(args, bucketDate(filter.Until))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// QueryUsage returns every provider_usage row matching filter, ordered by
// provider_name, lane_name, model_name, then bucket descending.
func (m *Manager) QueryUsage(ctx context.Context, filter provider.UsageFilter) ([]provider.UsageSummary, error) {
	where, args := buildFilterWhere(filter)
	rows, err := m.db.QueryContext(ctx, `
		SELECT provider_name, lane_name, model_name, bucket, tokens_in, tokens_out, requests, errors,
			cost_micro_usd, last_used_at
		FROM `+tableProviderUsage+where+`
		ORDER BY provider_name ASC, lane_name ASC, model_name ASC, bucket DESC`, args...)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "usage: query")
	}
	defer func() { _ = rows.Close() }()

	out := make([]provider.UsageSummary, 0)
	for rows.Next() {
		var s provider.UsageSummary
		var lastUsedMs int64
		if err := rows.Scan(&s.ProviderName, &s.LaneName, &s.ModelName, &s.Bucket, &s.TokensIn, &s.TokensOut,
			&s.Requests, &s.Errors, &s.CostMicroUSD, &lastUsedMs); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "usage: scan row")
		}
		s.LastUsedAt = time.UnixMilli(lastUsedMs).UTC()
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "usage: iterate rows")
	}
	return out, nil
}

// TotalCostMicroUSD sums cost_micro_usd across every row matching filter.
// No matching rows returns (0, nil), never an error.
func (m *Manager) TotalCostMicroUSD(ctx context.Context, filter provider.UsageFilter) (int64, error) {
	where, args := buildFilterWhere(filter)
	var total sql.NullInt64
	row := m.db.QueryRowContext(ctx, `SELECT SUM(cost_micro_usd) FROM `+tableProviderUsage+where, args...)
	if err := row.Scan(&total); err != nil {
		return 0, cascade.Wrap(cascade.KindUnavailable, err, "usage: total cost")
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Int64, nil
}

// Reader adapts a *Manager to pkg/provider.UsageReader. The write-capable
// *Manager itself is never exported to pkg/.
type Reader struct {
	mgr *Manager
}

// NewReader wraps mgr as a pkg/provider.UsageReader.
func NewReader(mgr *Manager) *Reader { return &Reader{mgr: mgr} }

// QueryUsage implements provider.UsageReader.
func (r *Reader) QueryUsage(ctx context.Context, filter provider.UsageFilter) ([]provider.UsageSummary, error) {
	return r.mgr.QueryUsage(ctx, filter)
}

// TotalCostMicroUSD implements provider.UsageReader.
func (r *Reader) TotalCostMicroUSD(ctx context.Context, filter provider.UsageFilter) (int64, error) {
	return r.mgr.TotalCostMicroUSD(ctx, filter)
}

var _ provider.UsageReader = (*Reader)(nil)
