// Purpose: the pure classifier that maps one call's evidence to a lane
//
//	write: (returned error, observed HTTP status, observed headers, now)
//	-> (state, reset estimate, write?). It reads the driver's own typed
//	cascade.Kind and never a raw status; the status only says whether a
//	response was observed at all.
//
// Inputs: the call's returned error, the observed status (0 = no response
//
//	reached the Transport) and Retry-After headers, and the injected now.
//
// Outputs: the lane state to record, its reset estimate, and whether to
//
//	write at all.
//
// Constraints: no observed response -> no write, whatever the Kind (a
//
//	local credential or grant refusal never reads as auth-required).
//	nil error -> available, reset cleared. KindPermissionDenied ->
//	auth-required. KindQuotaExhausted -> exhausted, reset = now +
//	Retry-After delta-seconds when that header is a plain non-negative
//	integer, clamped to 7 days (an integer past int64 or time.Duration
//	clamps too); absent, signed, fractional or otherwise unparsable ->
//	no estimate. KindCapabilityDenied and every other Kind -> no write
//	(R-14.88, R-21.29). Only Retry-After is read: no vendor reset header
//	is captured anywhere in the tree. A second copy of the delta-seconds
//	rule in providers/openai, which keeps its own unexported parser.
//
// SPORT: internal/providers/dispatch lane_outcome/ADD (P1-WID-11).

package dispatch

import (
	"strconv"
	"strings"
	"time"

	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/pkg/cascade"
)

// maxResetEstimate caps how far ahead a Retry-After may place a reset.
const maxResetEstimate = 7 * 24 * time.Hour

// classifyOutcome decides what, if anything, one call's evidence writes.
func classifyOutcome(err error, status int, headers map[string][]string, now time.Time) (registry.LaneState, time.Time, bool) {
	if status <= 0 {
		return "", time.Time{}, false
	}
	if err == nil {
		return registry.LaneStateAvailable, time.Time{}, true
	}
	kind, ok := cascade.KindOf(err)
	if !ok {
		return "", time.Time{}, false
	}
	if kind == cascade.KindPermissionDenied {
		return registry.LaneStateAuthRequired, time.Time{}, true
	}
	if kind == cascade.KindQuotaExhausted {
		return registry.LaneStateExhausted, resetFromRetryAfter(headers, now), true
	}
	return "", time.Time{}, false
}

// resetFromRetryAfter turns a Retry-After delta-seconds value into an
// instant, or the zero time when there is no valid estimate.
func resetFromRetryAfter(headers map[string][]string, now time.Time) time.Time {
	secs, ok := retryAfterSeconds(headers)
	if !ok {
		return time.Time{}
	}
	if secs >= uint64(maxResetEstimate/time.Second) {
		return now.Add(maxResetEstimate)
	}
	return now.Add(time.Duration(secs) * time.Second)
}

// retryAfterSeconds reads the first Retry-After value (header name matched
// case-insensitively) as RFC 9110 delta-seconds: ASCII digits only. A value
// too large for uint64 is reported as the largest uint64, so the caller's
// clamp applies. ok is false for a missing, empty, signed or non-digit value.
func retryAfterSeconds(headers map[string][]string) (uint64, bool) {
	for name, values := range headers {
		if !strings.EqualFold(name, retryAfterHeader) || len(values) == 0 {
			continue
		}
		v := strings.TrimSpace(values[0])
		if v == "" || strings.Trim(v, "0123456789") != "" {
			return 0, false
		}
		secs, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return ^uint64(0), true // all digits, so the only failure is range
		}
		return secs, true
	}
	return 0, false
}
