// Purpose: quota-window observations in the provider usage store
// (P1-GWY-04, contract:gateway-quota-bucket). This store owns the gateway
// quota bucket; the capacity compositor reads it through one seam
// (capacity.QuotaSource) and never keeps a second copy.
//
// Inputs: QuotaObservation values decoded from an account's usage source.
// Outputs: provider_quota_window rows; QuotaWindows reads with Stale derived.
// Constraints: a failed read keeps the last values and records a
//   content-free class only; nothing here turns an unknown into zero.
// SPORT: provider.usage quota windows (ADD, P1-GWY-04).

package usage

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tableProviderQuotaWindow holds one row per (provider, window).
const tableProviderQuotaWindow = "provider_quota_window"

// QuotaPollInterval is the endpoint source's poll interval; nothing polls
// faster.
const QuotaPollInterval = 60 * time.Second

// QuotaStaleAfter is twice the poll interval: an observation older than
// this reads Stale.
const QuotaStaleAfter = 2 * QuotaPollInterval

// QuotaUnknown marks Used, Limit or UtilizationPct as not reported.
const QuotaUnknown = -1

// QuotaObservation is one quota window's last reading for one provider.
type QuotaObservation struct {
	// Provider is the provider (gateway account) name.
	Provider string
	// Window is the accounting window, from the one topology vocabulary.
	Window topology.BucketWindow
	// Used is the consumed amount, or QuotaUnknown.
	Used int64
	// Limit is the window's limit, or QuotaUnknown.
	Limit int64
	// UtilizationPct is the source's figure, else 100*Used/Limit when
	// Limit > 0, else QuotaUnknown; clamped 0..100 when known.
	UtilizationPct float64
	// ResetsAt is when the window resets; zero when not reported.
	ResetsAt time.Time
	// ObservedAt is the instant of the last successful read.
	ObservedAt time.Time
	// LastError is the content-free class of the last failed read, if any.
	LastError string
	// Stale is derived on read: LastError set or ObservedAt too old.
	Stale bool
}

// Utilization returns the figure the observation carries per the contract:
// the reported value when known, else 100*Used/Limit when Limit > 0, else
// QuotaUnknown; known values are clamped to 0..100.
func (o QuotaObservation) Utilization() float64 {
	u := o.UtilizationPct
	if u < 0 {
		if o.Limit <= 0 || o.Used < 0 {
			return QuotaUnknown
		}
		u = 100 * float64(o.Used) / float64(o.Limit)
	}
	return min(max(u, 0), 100)
}

// RecordQuota upserts obs by (provider, window), clearing any last error.
func (m *Manager) RecordQuota(ctx context.Context, obs QuotaObservation) error {
	if obs.Provider == "" || !obs.Window.Valid() {
		return cascade.New(cascade.KindInvalidInput, "usage: RecordQuota requires a provider and a valid window")
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = m.clock.Now()
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO `+tableProviderQuotaWindow+`
			(provider_name, window, used, limit_value, utilization_pct, resets_at, observed_at, last_error)
		VALUES (?, ?, ?, ?, ?, ?, ?, '')
		ON CONFLICT(provider_name, window) DO UPDATE SET
			used=excluded.used, limit_value=excluded.limit_value, utilization_pct=excluded.utilization_pct,
			resets_at=excluded.resets_at, observed_at=excluded.observed_at, last_error=''`,
		obs.Provider, string(obs.Window), obs.Used, obs.Limit, obs.Utilization(),
		unixMilli(obs.ResetsAt), obs.ObservedAt.UnixMilli())
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "usage: record quota window")
	}
	return nil
}

// RecordQuotaFailure records a failed read of (provider, w): the last
// values are kept and only the content-free class is stored. A window
// never read successfully gets a row with every figure unknown.
func (m *Manager) RecordQuotaFailure(ctx context.Context, provider string, w topology.BucketWindow, class string) error {
	if provider == "" || !w.Valid() || class == "" {
		return cascade.New(cascade.KindInvalidInput, "usage: RecordQuotaFailure requires a provider, a valid window and a class")
	}
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO `+tableProviderQuotaWindow+`
			(provider_name, window, used, limit_value, utilization_pct, resets_at, observed_at, last_error)
		VALUES (?, ?, -1, -1, -1, 0, 0, ?)
		ON CONFLICT(provider_name, window) DO UPDATE SET last_error=excluded.last_error`,
		provider, string(w), class)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "usage: record quota failure")
	}
	return nil
}

// QuotaWindows returns provider's quota windows ordered by window, with
// Stale derived against the store's clock.
func (r *Reader) QuotaWindows(ctx context.Context, provider string) ([]QuotaObservation, error) {
	rows, err := r.mgr.db.QueryContext(ctx, `
		SELECT window, used, limit_value, utilization_pct, resets_at, observed_at, last_error
		FROM `+tableProviderQuotaWindow+` WHERE provider_name = ? ORDER BY window`, provider)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "usage: read quota windows")
	}
	defer func() { _ = rows.Close() }()
	now := r.mgr.clock.Now()
	var out []QuotaObservation
	for rows.Next() {
		obs, err := scanQuota(rows, provider)
		if err != nil {
			return nil, err
		}
		obs.Stale = obs.LastError != "" || obs.ObservedAt.IsZero() || now.Sub(obs.ObservedAt) > QuotaStaleAfter
		out = append(out, obs)
	}
	if err := rows.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "usage: read quota windows")
	}
	return out, nil
}

func scanQuota(rows *sql.Rows, provider string) (QuotaObservation, error) {
	var window, lastErr string
	var used, limit, resets, observed int64
	var util float64
	if err := rows.Scan(&window, &used, &limit, &util, &resets, &observed, &lastErr); err != nil {
		return QuotaObservation{}, cascade.Wrap(cascade.KindIntegrity, err, "usage: scan quota window")
	}
	return QuotaObservation{
		Provider: provider, Window: topology.BucketWindow(window), Used: used, Limit: limit,
		UtilizationPct: util, ResetsAt: fromMilli(resets), ObservedAt: fromMilli(observed), LastError: lastErr,
	}, nil
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMilli(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
