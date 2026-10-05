// Purpose: actual-usage reconciliation. A real adapter usage report is
//
//	stored verbatim; a missing report keeps the estimate and says so,
//	never a silent zero. Scarce units are priced at the base price only.
//
// Inputs: a reservation id, an Actual and its ActualSource.
// Outputs: Reconcile.
// Constraints: ScarceUnits = BasePrice x actual requests, where BasePrice
//
//	is the base shadow price stamped on the row at creation: no pressure,
//	mode multiplier or reserve barrier enters it, so an envelope buys the
//	same work in every mode. Reconcile changes no state.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Reconcile records id's actual usage. src reported stores a verbatim;
// src estimated (no report arrived) keeps the estimate as the actual.
// ScarceUnits is recomputed from the stored base price.
func (rv *Reserver) Reconcile(ctx context.Context, id string, a Actual, src ActualSource) (Reservation, error) {
	r, err := rv.mustGet(ctx, id)
	if err != nil {
		return Reservation{}, err
	}
	switch src {
	case ActualSourceReported:
		r.Actual = a
	case ActualSourceEstimated:
		r.Actual = Actual(r.Estimate)
	default:
		return Reservation{}, cascade.Newf(cascade.KindInvalidInput, "economics: unknown actual source %q", string(src))
	}
	r.ActualSource = src
	r.ScarceUnits = r.BasePrice * float64(r.Actual.Requests)
	if err := rv.store.Replace(ctx, r); err != nil {
		return Reservation{}, err
	}
	return r, nil
}
