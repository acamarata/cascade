// Purpose: the R-21.35/R-21.50 VERBATIM token estimate: tokens_in as the
//
//	hydrated context size plus the 10% margin (ceil, fail-closed
//	rounding per 06 Sec5.15) and tokens_out as the frozen nine-task-class
//	default total.
//
// Inputs: a hydrated context size, a conductor.TaskClass (the sole
//
//	canonical enum, owned by K/S-22.T4) and a caller-supplied request
//	count.
//
// Outputs: TokensOutDefault, EstimateFor.
// Constraints: no second task-class enum, no triage/generate value
//
//	(R-21.50); the nine defaults are pinned by
//	testdata/goldens/tokens_out_defaults.json, authored from the ruling
//	text, never captured from an external counterpart.
//
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

package economics

import (
	"math"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// tokensOutDefaults is the R-21.35/R-21.50 NORMATIVE table, verbatim.
var tokensOutDefaults = map[conductor.TaskClass]int64{
	conductor.TaskClassClassify:  512,
	conductor.TaskClassSegment:   1024,
	conductor.TaskClassSummarize: 2048,
	conductor.TaskClassExtract:   2048,
	conductor.TaskClassChat:      4096,
	conductor.TaskClassCode:      8192,
	conductor.TaskClassReason:    4096,
	conductor.TaskClassReview:    4096,
	conductor.TaskClassArbitrate: 2048,
}

// TokensOutDefault returns tc's frozen tokens_out default, or
// ErrUnknownTaskClass for any value outside the nine declared classes
// (no permissive zero value, 06 Sec5.15).
func TokensOutDefault(tc conductor.TaskClass) (int64, error) {
	v, ok := tokensOutDefaults[tc]
	if !ok {
		return 0, cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownTaskClass, "%q", string(tc))
	}
	return v, nil
}

// tokensInMargin is the R-21.35 "+10%" multiplier applied to the
// hydrated context size.
const tokensInMargin = 1.10

// EstimateFor computes the R-21.35 Estimate for a hydrated context of
// hydratedTokens tokens, task class tc, and requests provider calls.
// tokens_in is ceil(hydratedTokens * 1.10) -- the conservative rounding
// 06 Sec5.15 requires. A negative hydratedTokens or a requests value
// below one is a typed error rather than a silently clamped zero.
func EstimateFor(hydratedTokens int64, tc conductor.TaskClass, requests int64) (Estimate, error) {
	if hydratedTokens < 0 {
		return Estimate{}, cascade.Newf(cascade.KindInvalidInput, "economics: hydrated context size must be >= 0, got %d", hydratedTokens)
	}
	if requests < 1 {
		return Estimate{}, cascade.Newf(cascade.KindInvalidInput, "economics: requests must be >= 1, got %d", requests)
	}
	tokensOut, err := TokensOutDefault(tc)
	if err != nil {
		return Estimate{}, err
	}
	tokensIn := int64(math.Ceil(float64(hydratedTokens) * tokensInMargin))
	return Estimate{
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Requests:  requests,
	}, nil
}
