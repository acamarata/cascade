package learn

// Purpose: the production capacity.EstimateSource. Estimate(tier, tc) returns
//   the observed median queue wait and median duration of the jobs the
//   scheduler routed to that tier for that task class, read from
//   jobs_scheduler_decisions joined to jobs_telemetry_outcomes on job_id.
// Inputs: a *sql.DB holding both tables.
// Outputs: (queueWait, durationEst) for capacity's R-21.175 expected-time
//   formula.
// Constraints: a cell with no joined rows (or an unreadable database, the
//   interface has no error return) returns the cold-start values: queue wait
//   0 and ONE identical duration for every tier, so a tier nobody has tried
//   is never favoured or punished by its estimate alone. Only the most
//   recent estimateWindow jobs count, one row per job even when a job was
//   dispatched more than once. Read-only: it writes no row and no config.
// SPORT: internal.learn.SQLiteEstimateSource/ADDED (P1-CAP-03).

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
)

const (
	// coldStartDuration is the duration estimate for a cell with no data,
	// identical across tiers.
	coldStartDuration = 5 * time.Minute
	// estimateWindow bounds how many recent jobs one Estimate reads.
	estimateWindow = 200
)

// SQLiteEstimateSource implements capacity.EstimateSource over observed data.
type SQLiteEstimateSource struct{ db *sql.DB }

// NewSQLiteEstimateSource returns an estimate source reading db.
func NewSQLiteEstimateSource(db *sql.DB) *SQLiteEstimateSource { return &SQLiteEstimateSource{db: db} }

var _ capacity.EstimateSource = (*SQLiteEstimateSource)(nil)

// Estimate returns the observed median queue wait and duration for (tier, tc),
// or the cold-start values when none are recorded.
func (e *SQLiteEstimateSource) Estimate(tier capacity.Tier, tc conductor.TaskClass) (queueWait, durationEst time.Duration) {
	if e == nil || e.db == nil {
		return 0, coldStartDuration
	}
	waits, durs, err := e.observed(context.Background(), tier, tc)
	if err != nil || len(waits) == 0 {
		return 0, coldStartDuration
	}
	return medianMillis(waits), medianMillis(durs)
}

// observedQuery joins the decisions of one (tier, task class) to their
// outcomes; the (selected_tier, task_class, decided_at) index serves it.
const observedQuery = `SELECT o.queue_time_ms, o.duration_ms, MAX(d.decided_at) AS t FROM ` +
	tableSchedulerDecision + ` d JOIN ` + tableTelemetryOutcomes + ` o ON o.job_id = d.job_id
		WHERE d.selected_tier = ? AND d.task_class = ? GROUP BY o.id ORDER BY t DESC LIMIT ?`

// observed loads queue_time_ms and duration_ms of the most recent jobs routed
// to (tier, tc): one row per job, newest decision first.
func (e *SQLiteEstimateSource) observed(ctx context.Context, tier capacity.Tier, tc conductor.TaskClass) (waits, durs []int64, err error) {
	rows, err := e.db.QueryContext(ctx, observedQuery, string(tier), string(tc), estimateWindow)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var w, d, t int64
		if err := rows.Scan(&w, &d, &t); err != nil {
			return nil, nil, err
		}
		waits, durs = append(waits, w), append(durs, d)
	}
	return waits, durs, rows.Err()
}

// medianMillis is the median of v (milliseconds) as a Duration; an even
// count averages the two middle values. v must be non-empty.
func medianMillis(v []int64) time.Duration {
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return time.Duration(s[mid]) * time.Millisecond
	}
	return time.Duration((s[mid-1]+s[mid])/2) * time.Millisecond
}
