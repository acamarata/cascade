// Purpose: contract:lane-promotion-rule, the single PromotionEligible rule
//
//	that moves an unranked lane into a ranked LaneClass. Posteriors arrive
//	only through the injected PosteriorSource; ApplyPromotions is the one
//	write path, and it also closes expired promotion reversals
//	(promotion_reversal.go) before it promotes anything.
//
// Inputs: a PosteriorSource, a Clock and a PromotionAuditor.
// Outputs: lane_class/base_shadow_price updates on config_lane, one audit
//
//	event per change, and a PromotionReport. No topology row is deleted.
//
// Constraints: no vendor or model name; LaneClass is the R-21.30 set as it
//
//	exists; NaN, negative or out-of-range input is never eligible.
//
// SPORT: fleet/topology/promotion/ADD (P1-TOP-06).

package topology

import (
	"context"
	"math"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// TaskClass names one kind of task a lane posterior is measured over.
type TaskClass string

// The task classes an unranked lane may serve.
const (
	TaskClassClassify  TaskClass = "classify"
	TaskClassExtract   TaskClass = "extract"
	TaskClassSummarize TaskClass = "summarize"
	TaskClassSegment   TaskClass = "segment"
)

// Promotion thresholds: a task-class posterior promotes an unranked lane
// only with at least PromotionMinSamples samples and a mean of at least
// PromotionMinPosterior. Both bounds are inclusive.
const (
	PromotionMinSamples   = 50
	PromotionMinPosterior = 0.80
)

// PromotionDefaultClass is the class a promoted lane takes when no passing
// posterior carries a family hint.
const PromotionDefaultClass = LaneClassWorkerDedicated

// ErrPromotionInput reports a promotion or reversal request this package
// refuses: a nil dependency, an unknown class or an out-of-bounds window.
var ErrPromotionInput = cascade.New(cascade.KindInvalidInput, "topology: invalid promotion input")

// UnrankedTaskClasses returns, in a fixed order, the only task classes an
// unranked lane is eligible for. The slice is a fresh copy.
func UnrankedTaskClasses() []TaskClass {
	return []TaskClass{TaskClassClassify, TaskClassExtract, TaskClassSummarize, TaskClassSegment}
}

// EligibleTaskClasses returns the task classes the promotion rule lets a
// lane of class serve while it is gated by that rule. Only an unranked lane
// is gated; every other class, and any unknown class, returns nil.
func EligibleTaskClasses(class LaneClass) []TaskClass {
	if class != LaneClassUnranked {
		return nil
	}
	return UnrankedTaskClasses()
}

func unrankedEligible(tc TaskClass) bool {
	for _, c := range UnrankedTaskClasses() {
		if c == tc {
			return true
		}
	}
	return false
}

// PromotionEligible is the single promotion rule. It reports whether one
// task-class posterior (samples observations, mean success) clears both
// thresholds; a false result always carries a non-empty reason.
func PromotionEligible(samples int, mean float64) (bool, string) {
	switch {
	case samples < 0:
		return false, "negative sample count"
	case math.IsNaN(mean) || math.IsInf(mean, 0):
		return false, "posterior mean is not a finite number"
	case mean < 0 || mean > 1:
		return false, "posterior mean outside [0, 1]"
	case samples < PromotionMinSamples:
		return false, "fewer than 50 samples"
	case mean < PromotionMinPosterior:
		return false, "posterior mean below 0.80"
	}
	return true, ""
}

// Posterior is one lane's learned success estimate for one task class.
// FamilyHint optionally names the ranked LaneClass the evidence resembles.
type Posterior struct {
	TaskClass  TaskClass
	Samples    int
	Mean       float64
	FamilyHint LaneClass
}

// PosteriorSource supplies lane posteriors. It is the only input of
// learned evidence to ApplyPromotions (P1-LRN-06 binds the real one).
type PosteriorSource interface {
	LanePosteriors(ctx context.Context, lane LaneID) ([]Posterior, error)
}

// PromotionFamily picks the class a promoted lane takes from the family
// hints of its passing posteriors: the most-shared valid ranked hint wins,
// a tie goes to the lexicographically smaller class, and no usable hint
// yields PromotionDefaultClass.
func PromotionFamily(hints []LaneClass) LaneClass {
	counts := map[LaneClass]int{}
	for _, h := range hints {
		if h.Valid() && h != LaneClassUnranked {
			counts[h]++
		}
	}
	best, bestN := PromotionDefaultClass, 0
	for c, n := range counts {
		if n > bestN || (n == bestN && c < best) {
			best, bestN = c, n
		}
	}
	return best
}

// PromotionEventKind names one audited promotion change.
type PromotionEventKind string

// The audited promotion changes.
const (
	PromotionEventPromoted       PromotionEventKind = "promoted"
	PromotionEventReversed       PromotionEventKind = "reversed"
	PromotionEventReversalExpiry PromotionEventKind = "reversal-expired"
)

// PromotionEvent is one audit record: which lane moved, between which
// class/price pairs, when, and (for a reversal) until when.
type PromotionEvent struct {
	Kind      PromotionEventKind
	Lane      LaneID
	FromClass LaneClass
	ToClass   LaneClass
	FromPrice float64
	ToPrice   float64
	At        time.Time
	ExpiresAt time.Time
	Reason    string
}

// PromotionAuditor records promotion events. A Record error aborts the
// change that produced it, so no unaudited change is ever committed.
type PromotionAuditor interface {
	RecordPromotion(ctx context.Context, ev PromotionEvent) error
}

// PromotionReport summarizes one ApplyPromotions pass.
type PromotionReport struct {
	Promoted []LaneID
	Restored []LaneID
	// RowsChanged counts lane and reversal rows written by the pass.
	RowsChanged int
}

func promotionDepsErr(src PosteriorSource, clock Clock, audit PromotionAuditor) error {
	switch {
	case src == nil:
		return cascade.Wrap(cascade.KindInvalidInput, ErrPromotionInput, "topology: nil posterior source")
	case clock == nil:
		return cascade.Wrap(cascade.KindInvalidInput, ErrPromotionInput, "topology: nil clock")
	case audit == nil:
		return cascade.Wrap(cascade.KindInvalidInput, ErrPromotionInput, "topology: nil promotion auditor")
	}
	return nil
}

// ApplyPromotions is the only promotion write path. In one transaction it
// restores every expired reversal, then promotes each live unranked lane
// without an active reversal whose eligible posteriors pass
// PromotionEligible. Any source, audit or store error rolls back the whole
// pass and is returned; a nil dependency writes nothing.
func (s *Store) ApplyPromotions(ctx context.Context, src PosteriorSource, clock Clock, audit PromotionAuditor) (PromotionReport, error) {
	if err := promotionDepsErr(src, clock, audit); err != nil {
		return PromotionReport{}, err
	}
	var rep PromotionReport
	err := s.withTx(ctx, func(tx *Store) error {
		now := clock.Now()
		if err := tx.expireReversals(ctx, now, audit, &rep); err != nil {
			return err
		}
		held, err := tx.activeReversalLanes(ctx)
		if err != nil {
			return err
		}
		lanes, err := tx.ListLanes(ctx, false)
		if err != nil {
			return err
		}
		for _, lane := range lanes {
			if lane.LaneClass != LaneClassUnranked || held[lane.ID] {
				continue
			}
			if err := tx.promoteOne(ctx, src, audit, lane, now, &rep); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return PromotionReport{}, err
	}
	return rep, nil
}

// promoteOne promotes lane when its posteriors qualify.
func (s *Store) promoteOne(ctx context.Context, src PosteriorSource, audit PromotionAuditor, lane Lane, now time.Time, rep *PromotionReport) error {
	posts, err := src.LanePosteriors(ctx, lane.ID)
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: posteriors for lane %q", lane.ID)
	}
	var hints []LaneClass
	passed := false
	for _, p := range posts {
		if !unrankedEligible(p.TaskClass) {
			continue
		}
		if ok, _ := PromotionEligible(p.Samples, p.Mean); ok {
			passed = true
			hints = append(hints, p.FamilyHint)
		}
	}
	if !passed {
		return nil
	}
	target := PromotionFamily(hints)
	price, err := BaseShadowPrice(target)
	if err != nil {
		return err
	}
	if err := s.setLaneClass(ctx, lane.ID, lane.LaneClass, target, price); err != nil {
		return err
	}
	ev := PromotionEvent{Kind: PromotionEventPromoted, Lane: lane.ID, FromClass: lane.LaneClass, ToClass: target,
		FromPrice: lane.BaseShadowPrice, ToPrice: price, At: now, Reason: "posteriors cleared the promotion rule"}
	if err := audit.RecordPromotion(ctx, ev); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: audit promotion of lane %q", lane.ID)
	}
	rep.Promoted = append(rep.Promoted, lane.ID)
	rep.RowsChanged++
	return nil
}

// setLaneClass compare-and-sets lane_class and base_shadow_price; a lane
// no longer at from is ErrTopologyConflict, never a blind overwrite.
func (s *Store) setLaneClass(ctx context.Context, id LaneID, from, to LaneClass, price float64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE `+tableLane+` SET lane_class = ?, base_shadow_price = ? WHERE id = ? AND lane_class = ?`,
		string(to), price, string(id), string(from))
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: set class of lane %q", id)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "topology: set class of lane %q", id)
	}
	if n != 1 {
		return cascade.Wrapf(cascade.KindConflict, ErrTopologyConflict, "lane %q is no longer %s", id, from)
	}
	return nil
}
