package learn

// Purpose: decayed Beta lane posteriors (P1-LRN-07d). One posterior per
// (task_class, topology lane), seeded from PriorAlphaBeta(selected tier,
// task class) and aged with the decay.go helper. Unobserved lanes report
// ok=false; read errors surface as KindUnavailable, never ok=false.
// SPORT: internal.learn.LaneStore/ADDED, internal.learn.LanePosterior/ADDED.

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// LaneScore is one decayed Beta posterior for a (task_class, lane).
type LaneScore struct {
	Alpha, Beta  float64
	Observations int
}

// LaneStore reads lane posteriors over SQLite.
type LaneStore struct {
	db    *sql.DB
	clock runtime.Clock
}

// NewLaneStore returns a LaneStore over db using clock for decay.
func NewLaneStore(db *sql.DB, clock runtime.Clock) *LaneStore {
	return &LaneStore{db: db, clock: clock}
}

// LanePosterior returns the decayed Beta posterior for (tc, lane, tier).
// ok is false with a nil error only when unobserved. A missing prior is
// KindInvalidInput; a read error is KindUnavailable.
func (s *LaneStore) LanePosterior(ctx context.Context, tc conductor.TaskClass, lane topology.LaneID, tier capacity.Tier) (LaneScore, bool, error) {
	if s == nil || s.db == nil || s.clock == nil {
		return LaneScore{}, false, cascade.New(cascade.KindInvalidInput, "learn: LanePosterior requires a constructed LaneStore")
	}
	if !lane.Valid() {
		return LaneScore{}, false, cascade.New(cascade.KindInvalidInput, "learn: LanePosterior requires a lane")
	}
	a0, b0, ok := PriorAlphaBeta(tier, tc)
	if !ok {
		return LaneScore{}, false, cascade.New(cascade.KindInvalidInput, "learn: no capability prior for this (tier, task class)")
	}
	row, found, err := s.readRow(ctx, lane, tc)
	if err != nil {
		return LaneScore{}, false, err
	}
	if !found {
		return LaneScore{}, false, nil
	}
	now := s.clock.Now()
	return LaneScore{
		Alpha:        a0 + decayed(row.alpha, row.lastUpdated, now),
		Beta:         b0 + decayed(row.beta, row.lastUpdated, now),
		Observations: row.count,
	}, true, nil
}

// foldLaneObservation decays observed mass before adding the next outcome.
// The prior is added only at read time, so it never decays or accumulates.
func foldLaneObservation(row laneRow, success bool, now time.Time) laneRow {
	return laneRow(foldObservation(scoreRow(row), success, now))
}

type laneRow struct {
	alpha, beta float64
	count       int
	lastUpdated time.Time
}

func (s *LaneStore) readRow(ctx context.Context, lane topology.LaneID, tc conductor.TaskClass) (laneRow, bool, error) {
	var r laneRow
	var last int64
	err := s.db.QueryRowContext(ctx,
		`SELECT alpha, beta, observation_count, last_updated FROM `+tableLanePosterior+
			` WHERE lane_id = ? AND task_class = ?`, string(lane), string(tc)).
		Scan(&r.alpha, &r.beta, &r.count, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return laneRow{}, false, nil
	}
	if err != nil {
		return laneRow{}, false, cascade.Wrap(cascade.KindUnavailable, err, "learn: read lane posterior row")
	}
	r.lastUpdated = time.Unix(last, 0).UTC()
	return r, true, nil
}
