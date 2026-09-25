// Purpose: the persisted-leg-result half of the fan-out primitive
//   (contract:fanout-leg-results): the LegResult record, the injected
//   LegResultStore and AuthorizeFn seams, the per-leg request digest, the
//   done-entry outcome vocabulary, and the replay path that releases a
//   stored Response only after the executor's own authorize step passes.
// Inputs: a fan-out id, a leg index, the leg request, and the injected
//   store/journal/authorize seams.
// Outputs: a stored Response (replay), or a typed refusal.
// Constraints: no stored byte leaves this file before AuthorizeFn returns
//   nil for the leg request; a record is never rewritten here; journal
//   payloads carry the result key and the digest, never Response content.
// SPORT: conductor.fanout/CHANGE (P1-CORE-18).

package conductor

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// LegResult is one successful fan-out leg's persisted outcome. It is
// written create-only before the leg's `fanout_leg_done` entry and read
// back only through the re-authorized replay path. Sensitivity is the
// leg request's own tier so a retention rule can govern the record.
type LegResult struct {
	FanOutID      string
	TaskID        string
	LegIndex      int
	Attempt       uint64
	RequestDigest string
	Sensitivity   provider.SensitivityTier
	Response      provider.ModelResponse
}

// LegResultStore persists LegResults keyed by (fanoutID, legIndex).
// PutLegResult is create-only: a second Put for the same key fails with
// KindConflict. GetLegResult reports found=false (and a nil error) for an
// absent key. DeleteTask removes every record of one fan-out and nothing
// of another.
type LegResultStore interface {
	PutLegResult(ctx context.Context, r LegResult) error
	GetLegResult(ctx context.Context, fanoutID string, legIndex int) (LegResult, bool, error)
	DeleteTask(ctx context.Context, fanoutID string) error
}

// AuthorizeFn is the executor's door check (validation, classifier,
// sensitivity, policy) for one request, with a refusal audited by the
// implementation. ExecuteFanOut passes (*Executor).Authorize.
type AuthorizeFn func(ctx context.Context, req provider.ModelRequest) error

// Done-entry outcomes. LegOutcomeOK is the only outcome that counts a leg
// as completed; the two failed outcomes store nothing.
const (
	LegOutcomeOK              = "ok"
	LegOutcomeFailedTerminal  = "failed_terminal"
	LegOutcomeFailedRetryable = "failed_retryable"
)

// Leg journal kinds, the literal strings JournalAppender receives.
const (
	legKindStarted = "fanout_leg_started"
	legKindDone    = "fanout_leg_done"
)

// ErrLegResultMissing is the replay refusal for a leg the journal records
// as completed whose LegResult does not exist: never an empty success.
var ErrLegResultMissing = cascade.New(cascade.KindNotFound, "conductor: fan-out leg is journaled as completed but its stored result is missing")

// ErrLegResultMismatch is the replay refusal for a stored LegResult that
// was produced by a different request (digest, fan-out id or leg index
// differ): the record is never released for this request.
var ErrLegResultMismatch = cascade.New(cascade.KindConflict, "conductor: stored fan-out leg result belongs to a different request")

// LegResultKey is the LegResultStore key of one leg: <fanoutID>#<legIndex>.
// The same string rides the done entry as result_key.
func LegResultKey(fanoutID string, legIndex int) string {
	return fanoutID + "#" + strconv.Itoa(legIndex)
}

// validateFanOutID refuses an id that cannot key a fan-out: empty is a
// construction error; '#' or NUL would make the <fanoutID>#<leg> keys and
// journal operation ids ambiguous across two fan-outs.
func validateFanOutID(fanoutID string) error {
	if fanoutID == "" {
		return ErrConstructionFailed
	}
	if strings.ContainsAny(fanoutID, "#\x00") {
		return cascade.Newf(cascade.KindInvalidInput, "conductor: fan-out id %q contains a reserved separator ('#' or NUL)", fanoutID)
	}
	return nil
}

// legRequestDigest binds one leg request: the fan-out id (salt), the
// client TaskID and leg index dispatchLeg always hashed, and the full leg
// request body, so a changed prompt, model or policy changes the digest.
func legRequestDigest(fanoutID string, idx int, legReq provider.ModelRequest) (string, error) {
	body, err := json.Marshal(legReq)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "conductor: encoding fan-out leg request for its digest")
	}
	prefix := fanoutID + "\x00" + legReq.TaskID + "|" + strconv.Itoa(idx) + "\x00"
	return audit.HashParams(append([]byte(prefix), body...)), nil
}

// legOutcome classifies a failed leg: a door refusal (policy, sensitivity,
// classifier or invalid input) is terminal; anything else may be retried.
func legOutcome(err error) string {
	if cascade.HasKind(err, cascade.KindPolicyDenied) || cascade.HasKind(err, cascade.KindInvalidInput) {
		return LegOutcomeFailedTerminal
	}
	return LegOutcomeFailedRetryable
}

// doneFields builds a done entry's payload fields. It carries the result
// key and the request digest, never Response content.
func doneFields(fanoutID string, idx int, attempt uint64, outcome, digest string, jobID JobID) map[string]string {
	f := map[string]string{
		"attempt":        strconv.FormatUint(attempt, 10),
		"outcome":        outcome,
		"request_digest": digest,
		"job_id":         string(jobID),
	}
	if outcome == LegOutcomeOK {
		f["result_key"] = LegResultKey(fanoutID, idx)
	}
	return f
}

// replayLeg releases a stored leg result: the record must exist, belong
// to this exact leg request, and the leg request must pass authorize NOW
// (a policy or sensitivity change since the record was written refuses
// it). Only then is the Response returned, and a missing done entry (the
// crash window between PutLegResult and the done append) is appended.
func (r *fanOutRun) replayLeg(ctx context.Context, idx int, legReq provider.ModelRequest, digest string, rec LegResult, found, doneJournaled bool) legResult {
	if !found {
		return legResult{index: idx, err: ErrLegResultMissing}
	}
	if rec.RequestDigest != digest || rec.FanOutID != r.fanoutID || rec.LegIndex != idx {
		return legResult{index: idx, err: ErrLegResultMismatch}
	}
	if err := r.authorize(ctx, legReq); err != nil {
		return legResult{index: idx, err: err}
	}
	if !doneJournaled {
		fields := doneFields(r.fanoutID, idx, rec.Attempt, LegOutcomeOK, digest, rec.Response.JobID)
		if _, err := r.journal.AppendLeg(ctx, legKindDone, r.fanoutID, idx, fields); err != nil {
			return legResult{index: idx, err: err}
		}
	}
	return legResult{index: idx, resp: rec.Response}
}
