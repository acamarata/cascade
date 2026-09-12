// Purpose: R-21.26's source-precedence ladder --
//
//	provider-status > cli-observation > user-estimate > unknown -- and
//	Merge, the deterministic reconciliation of two observations of the
//	same bucket. pickBetter is the shared comparison freshness.go's
//	Select folds the same way over a non-expired observation set, so the
//	two files never carry two competing tie-break implementations.
//
// Inputs: two Bucket values. Outputs: the winning Bucket, deterministically.
// Constraints: a merge never downgrades a bucket to a weaker source; ties
//
//	break on later ObservedAt, then higher Confidence.
//
// SPORT: fleet/topology/source_ladder/ADD (P1-E40-W9-S77-T3).

package topology

// Merge reconciles existing against incoming, keeping the higher-
// precedence source. On equal precedence the later ObservedAt wins; on
// equal precedence and equal ObservedAt the higher Confidence wins. The
// result is deterministic and never downgrades to a weaker source than
// either input carried.
func Merge(existing, incoming Bucket) Bucket {
	return pickBetter(existing, incoming)
}

// pickBetter returns whichever of a, b has the higher-precedence Source,
// breaking ties by later ObservedAt then higher Confidence. Ties that
// remain after both break (identical in every ranked field) return a, so
// the function is a stable total order.
func pickBetter(a, b Bucket) Bucket {
	pa, pb := a.Source.precedence(), b.Source.precedence()
	if pa != pb {
		if pa > pb {
			return a
		}
		return b
	}
	if !a.ObservedAt.Equal(b.ObservedAt) {
		if a.ObservedAt.After(b.ObservedAt) {
			return a
		}
		return b
	}
	if a.Confidence != b.Confidence {
		if a.Confidence > b.Confidence {
			return a
		}
		return b
	}
	return a
}
