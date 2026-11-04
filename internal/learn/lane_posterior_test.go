package learn

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	_ "modernc.org/sqlite"
)

func setupLanePosteriorDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "lane.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	err = applyRoutingSchema(db)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func seedLanePosterior(t *testing.T, db *sql.DB, lane topology.LaneID, tc conductor.TaskClass, at time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO `+tableLanePosterior+`
		(id, lane_id, task_class, alpha, beta, observation_count, last_updated)
		VALUES (?, ?, ?, 10, 5, 15, ?)`, string(lane)+"|"+string(tc), string(lane), string(tc), at.Unix())
	if err != nil {
		t.Fatal(err)
	}
}

// TestLanePosteriorHalfLife checks shared priors, decay, key isolation and read failures.
func TestLanePosteriorHalfLife(t *testing.T) {
	db := setupLanePosteriorDB(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := runtime.NewFixedClock(now)
	store := NewLaneStore(db, clock)
	ctx := context.Background()
	tc, lane := conductor.TaskClassCode, topology.LaneID("lane-observed")
	seedLanePosterior(t, db, lane, tc, now)
	for _, age := range []time.Duration{0, 30 * 24 * time.Hour} {
		if age != 0 {
			clock.Advance(age)
		}
		for tier := range capacity.Priors {
			a0, b0, has := PriorAlphaBeta(tier, tc)
			if !has {
				t.Fatal("missing code prior")
			}
			p, ok, err := store.LanePosterior(ctx, tc, lane, tier)
			weight := 1.0
			if age != 0 {
				weight = 0.5
			}
			if err != nil || !ok || p.Observations != 15 || math.Abs(p.Alpha-a0-10*weight) > 1e-12 || math.Abs(p.Beta-b0-5*weight) > 1e-12 {
				t.Fatalf("age %v tier %s: posterior %+v, ok %v, err %v", age, tier, p, ok, err)
			}
		}
	}
	for _, key := range []struct {
		tc   conductor.TaskClass
		lane topology.LaneID
	}{
		{tc, "lane-unobserved"}, {conductor.TaskClassReview, lane},
	} {
		if p, ok, err := store.LanePosterior(ctx, key.tc, key.lane, capacity.TierOne); err != nil || ok || p != (LaneScore{}) {
			t.Fatalf("unobserved key: %+v, %v, %v", p, ok, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	assertLaneReadUnavailable(ctx, t, store, tc, lane)
}

func assertLaneReadUnavailable(ctx context.Context, t *testing.T, store *LaneStore, tc conductor.TaskClass, lane topology.LaneID) {
	t.Helper()
	_, _, err := store.LanePosterior(ctx, tc, lane, capacity.TierOne)
	if !errors.Is(err, cascade.ErrUnavailable) || !strings.Contains(err.Error(), "learn: read lane posterior row") {
		t.Fatalf("read failure must be unavailable: %v", err)
	}
}

func TestLanePosteriorReadError(t *testing.T) {
	db := setupLanePosteriorDB(t)
	store := NewLaneStore(db, runtime.NewFixedClock(time.Unix(0, 0)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := store.LanePosterior(ctx, conductor.TaskClassCode, "lane", capacity.TierOne)
	if !errors.Is(err, cascade.ErrUnavailable) || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "learn: read lane posterior row") {
		t.Fatalf("canceled read must preserve its cause: %v", err)
	}
}

func TestLanePosteriorFold(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, success := range []bool{false, true} {
		row := foldLaneObservation(laneRow{alpha: 10, beta: 4, count: 14, lastUpdated: at}, success, at.Add(30*24*time.Hour))
		a, b := 5.0, 3.0
		if success {
			a, b = 6, 2
		}
		if row.alpha != a || row.beta != b || row.count != 15 || !row.lastUpdated.Equal(at.Add(30*24*time.Hour)) {
			t.Fatalf("fold: %+v", row)
		}
	}
}
