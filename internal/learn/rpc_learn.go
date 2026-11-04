package learn

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ScoreRow exposes a posterior and the source of its observations.
type ScoreRow struct {
	Tier          string  `json:"tier"`
	TaskClass     string  `json:"task_class"`
	Score         float64 `json:"score"`
	Alpha         float64 `json:"alpha"`
	Beta          float64 `json:"beta"`
	Observations  int     `json:"observations"`
	FallbackLevel string  `json:"fallback_level"`
}

// ExplainEstimate carries capacity's expected-time inputs and result.
type ExplainEstimate struct {
	Tier            string        `json:"tier"`
	QueueWait       time.Duration `json:"queue_wait"`
	DurationEst     time.Duration `json:"duration_est"`
	PRework         float64       `json:"p_rework"`
	ReworkCyclesEst int           `json:"rework_cycles_est"`
	ExpectedTime    time.Duration `json:"expected_time"`
}

// ExplainResult explains capability scores and the expected-time jump.
type ExplainResult struct {
	Scores    []ScoreRow       `json:"scores"`
	Tier0     *ExplainEstimate `json:"tier0"`
	BestLower *ExplainEstimate `json:"best_lower"`
	Threshold float64          `json:"threshold"`
	JumpFires bool             `json:"jump_fires"`
}

// LearnReadDeps supplies the scorer, reset boundary and estimate inputs.
type LearnReadDeps struct { //nolint:revive // contract-mandated exported name (P1-LRN-04a outputs)
	Scorer    *SQLiteCapabilityScorer
	Resetter  ObservationResetter
	Estimates capacity.EstimateSource
	Clock     runtime.Clock
}

// RegisterLearnHandlers registers the owner-only status, explain and reset verbs.
func RegisterLearnHandlers(reg *rpc.Registry, d LearnReadDeps) error {
	if reg == nil || d.Scorer == nil || d.Scorer.db == nil || d.Scorer.clock == nil || d.Resetter == nil || d.Estimates == nil || d.Clock == nil {
		return cascade.New(cascade.KindInvalidInput, "learn: handlers require scorer, resetter, estimates and clock")
	}
	reg.Register("learn.status", func(ctx context.Context, _ json.RawMessage) (any, error) {
		scores, err := d.status(ctx)
		if err != nil {
			return nil, err
		}
		return struct {
			Scores []ScoreRow `json:"scores"`
		}{scores}, nil
	})
	reg.Register("learn.explain", d.handleExplain)
	reg.Register("learn.reset", d.handleReset)
	return nil
}

// handleExplain serves learn.explain: one scope and task class.
func (d LearnReadDeps) handleExplain(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ScopeKey  string `json:"scope_key"`
		TaskClass string `json:"task_class"`
	}
	if err := decodeLearnParams(raw, &p); err != nil {
		return nil, err
	}
	scope, err := ParseScopeKey(p.ScopeKey)
	if err != nil {
		return nil, err
	}
	if _, ok := capacity.Priors[capacity.TierZero][taskClassType(p.TaskClass)]; !ok {
		return nil, cascade.New(cascade.KindInvalidInput, "learn: task class has no capability prior")
	}
	return d.explain(ctx, scope, taskClassType(p.TaskClass))
}

// handleReset serves learn.reset: a scoped or all-rows observation reset.
func (d LearnReadDeps) handleReset(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		ScopeKey  string `json:"scope_key"`
		TaskClass string `json:"task_class"`
		Tier      string `json:"tier"`
		All       bool   `json:"all"`
	}
	if err := decodeLearnParams(raw, &p); err != nil {
		return nil, err
	}
	if err := validateReset(p.ScopeKey, p.TaskClass, p.Tier, p.All); err != nil {
		return nil, err
	}
	n, err := d.Resetter.Reset(ctx, p.ScopeKey, p.TaskClass, p.Tier, p.All)
	if err != nil {
		return nil, err
	}
	return struct {
		RowsDeleted int  `json:"rows_deleted"`
		Changed     bool `json:"changed"`
	}{n, n > 0}, nil
}

func taskClassType(s string) conductor.TaskClass { return conductor.TaskClass(s) }

func decodeLearnParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return cascade.Wrap(cascade.KindInvalidInput, err, "learn: invalid parameters")
	}
	return nil
}

func (d LearnReadDeps) score(ctx context.Context, scope ScopeKey, tc conductor.TaskClass, tier capacity.Tier) (ScoreRow, error) {
	p, err := d.Scorer.Posterior(ctx, scope, tc, tier)
	if err != nil {
		return ScoreRow{}, err
	}
	level := string(p.Level)
	if p.Observations == 0 {
		level = "priors"
	}
	return ScoreRow{Tier: string(tier), TaskClass: string(tc), Score: p.Alpha / (p.Alpha + p.Beta), Alpha: p.Alpha, Beta: p.Beta, Observations: p.Observations, FallbackLevel: level}, nil
}

func (d LearnReadDeps) status(ctx context.Context) ([]ScoreRow, error) {
	rows, err := d.Scorer.db.QueryContext(ctx, `SELECT scope_key, task_class, tier FROM `+tableCapabilityScore+` ORDER BY scope_key, task_class, tier`)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: list scores")
	}
	type key struct {
		scope ScopeKey
		tc    conductor.TaskClass
		tier  capacity.Tier
	}
	keys := []key{}
	for rows.Next() {
		var k key
		if err = rows.Scan(&k.scope, &k.tc, &k.tier); err != nil {
			break
		}
		keys = append(keys, k)
	}
	if err == nil {
		err = rows.Err()
	}
	_ = rows.Close()
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "learn: read scores")
	}
	if len(keys) == 0 {
		for tier, classes := range capacity.Priors {
			for tc := range classes {
				keys = append(keys, key{scopeGlobal, tc, tier})
			}
		}
	}
	result := make([]ScoreRow, 0, len(keys))
	for _, k := range keys {
		row, err := d.score(ctx, k.scope, k.tc, k.tier)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Tier != result[j].Tier {
			return result[i].Tier < result[j].Tier
		}
		return result[i].TaskClass < result[j].TaskClass
	})
	return result, nil
}

func (d LearnReadDeps) explain(ctx context.Context, scope ScopeKey, tc conductor.TaskClass) (ExplainResult, error) {
	result := ExplainResult{Scores: []ScoreRow{}, Threshold: capacity.JumpHysteresisFactor}
	var et0, best capacity.ExpectedTime
	for _, tier := range []capacity.Tier{capacity.TierZero, capacity.TierOne, capacity.TierTwo, capacity.TierLocal} {
		row, err := d.score(ctx, scope, tc, tier)
		if err != nil {
			return ExplainResult{}, err
		}
		result.Scores = append(result.Scores, row)
		qw, de := d.Estimates.Estimate(tier, tc)
		et := capacity.ComputeExpectedTime(qw, de, row.Score)
		estimate := &ExplainEstimate{string(tier), et.QueueWait, et.DurationEst, et.PRework, et.ReworkCyclesEst, et.ExpectedTime}
		if tier == capacity.TierZero {
			result.Tier0 = estimate
			et0 = et
		}
		if tier == capacity.TierOne || tier == capacity.TierTwo {
			if result.BestLower == nil || et.ExpectedTime < best.ExpectedTime {
				result.BestLower = estimate
				best = et
			}
		}
	}
	result.JumpFires = capacity.JumpFires(et0, best)
	return result, nil
}
