package learn

// Purpose: the value refusals of SQLiteSchedulerDecisionWriter.Record: a zero
//   DecidedAt, a non-finite or out-of-range score, and a JumpRuleFired flag
//   that disagrees with JumpReasonCode are KindInvalidInput and store nothing;
//   the inclusive score bounds are accepted.
// SPORT: internal.learn.SQLiteSchedulerDecisionWriter/TESTED (P1-CAP-03).

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestSchedulerDecisionValueRefusals(t *testing.T) {
	db, ctx := openMigratedDB(t, tmplOutcome), context.Background()
	w := SQLiteSchedulerDecisionWriter{}
	refused := map[string]func(*SchedulerDecision){
		"zero decided_at":         func(d *SchedulerDecision) { d.DecidedAt = time.Time{} },
		"NaN score":               func(d *SchedulerDecision) { d.ScoreAtSelection = math.NaN() },
		"+Inf score":              func(d *SchedulerDecision) { d.ScoreAtSelection = math.Inf(1) },
		"-Inf score":              func(d *SchedulerDecision) { d.ScoreAtSelection = math.Inf(-1) },
		"score above 1":           func(d *SchedulerDecision) { d.ScoreAtSelection = 1.0000001 },
		"negative score":          func(d *SchedulerDecision) { d.ScoreAtSelection = -0.0000001 },
		"fired with code none":    func(d *SchedulerDecision) { d.JumpReasonCode = capacity.JumpReasonNone },
		"not fired with a reason": func(d *SchedulerDecision) { d.JumpRuleFired, d.JumpReasonCode = false, capacity.JumpReasonRiskScore },
	}
	for name, mut := range refused {
		d := testDecision("dec-bad")
		mut(&d)
		if err := w.Record(ctx, db, d); !cascade.HasKind(err, cascade.KindInvalidInput) {
			t.Errorf("%s: Record = %v, want KindInvalidInput", name, err)
		}
	}
	if rows := readDecisions(t, db); len(rows) != 0 {
		t.Fatalf("rows after the refusals = %+v, want none", rows)
	}
	for i, score := range []float64{0, 1} {
		d := testDecision(map[int]string{0: "dec-lo", 1: "dec-hi"}[i])
		d.ScoreAtSelection = score
		if err := w.Record(ctx, db, d); err != nil {
			t.Errorf("score %v: Record = %v, want accepted", score, err)
		}
	}
	if rows := readDecisions(t, db); len(rows) != 2 {
		t.Errorf("rows after the bound writes = %d, want 2", len(rows))
	}
}
