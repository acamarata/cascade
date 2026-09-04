// Purpose: Params and FuseWith — the point where the [retrieval] config
// surface meets the fusion (R-14.19 names this package, not a
// pipeline.go, as the hook). Fuse takes the two values it needs as
// arguments and knows nothing about config; FuseWith carries the
// configured k and the configured per-leg weights and applies them to the
// legs before handing them on.
//
// Inputs: the legs' ranked lists as they came back, plus the configured
// retrieval.fusion.k and retrieval.fusion.weights.
// Outputs: Fuse's results, or a taxonomy error.
//
// Constraints: applying a weight must not mutate the caller's lists — a
// leg is a value a caller may still hold — so the lists are copied before
// the weight is written. A weight naming a leg that did not run is
// refused rather than ignored: the operator who wrote it believes that
// leg is being weighted, and silently dropping it is how a tuning appears
// to be live while the ranking never changes.
//
// SPORT: internal.retrieval.rrf.FuseWith/ADDED (P1-E06-W2-S12-T4).

package rrf

import "github.com/acamarata/cascade/pkg/cascade"

// Params is the configured fusion tuning: the [retrieval.fusion] keys as
// the fusion consumes them.
type Params struct {
	// K is retrieval.fusion.k, the RRF smoothing constant. Zero means
	// "not configured" and resolves to DefaultK, so a zero Params is the
	// shipped behaviour rather than an error.
	K int64
	// Weights is retrieval.fusion.weights, by leg name. A leg absent
	// from the map keeps the weight its list already carries; nil leaves
	// every leg exactly as it came back.
	Weights map[StrategyName]float64
}

// EffectiveK returns the k Fuse will be called with: the configured value,
// or DefaultK when none was configured.
func (p Params) EffectiveK() int64 {
	if p.K <= 0 {
		return DefaultK
	}
	return p.K
}

// FuseWith applies p's weights to lists and fuses them under p's k.
//
// It is Fuse plus configuration and nothing else: the ranking rules, the
// tie-break and the normalization all still live in Fuse, so a result
// fused through here and the same result fused directly are the same
// value. A nil lists is refused by Fuse, as it is there.
func FuseWith(lists []RankedList, p Params) ([]FusedResult, error) {
	weighted, err := applyWeights(lists, p.Weights)
	if err != nil {
		return nil, err
	}
	return Fuse(weighted, p.EffectiveK())
}

// applyWeights returns a copy of lists with the configured weights
// written onto the legs that were named.
//
// A weight naming a leg not in lists is a hard error. The alternative —
// ignoring it — is indistinguishable to the operator from the weight
// having been applied, and the misspelling that most often produces this
// (a leg name typed wrong) is exactly the case where the ranking they see
// is not the ranking they configured.
func applyWeights(lists []RankedList, weights map[StrategyName]float64) ([]RankedList, error) {
	if len(weights) == 0 {
		return lists, nil
	}
	if lists == nil {
		return nil, cascade.New(cascade.KindInvalidInput, "rrf: nil ranked-list input")
	}
	present := make(map[StrategyName]bool, len(lists))
	for _, l := range lists {
		present[l.Strategy] = true
	}
	for leg := range weights {
		if !present[leg] {
			return nil, cascade.Newf(cascade.KindInvalidInput,
				"rrf: retrieval.fusion.weights names leg %q, which did not run in this query",
				string(leg))
		}
	}
	out := make([]RankedList, len(lists))
	copy(out, lists)
	for i := range out {
		if w, ok := weights[out[i].Strategy]; ok {
			out[i].Weight = w
		}
	}
	return out, nil
}
