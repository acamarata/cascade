// Purpose: the job.cancel JSON-RPC method (§D-21) - idempotent
//   cancellation of an in-flight model.execute job, keyed by the same
//   JobID Execute/ExecuteStreamJob mint and register in the cancel
//   registry (stream.go). RegisterHandlers is the production caller
//   wiring: it mounts "job.cancel" on an internal/rpc.Registry, mirroring
//   internal/nodes/enroll.go's RegisterHandlers precedent exactly (the
//   daemon's own composition root, cmd/cascade, is out of this ticket's
//   files_scope and is the one remaining call site - see the journal).
// Inputs: a job_id string, decoded from the JSON-RPC request params.
// Outputs: an empty success result for every call (known, completed, or
//   unknown job id alike) - job.cancel never returns a JSON-RPC error for
//   a bad job id, only for malformed params (R-40.X9's fail-closed rule
//   applies to the wire framing, not to the idempotency contract).
// Constraints: cancel() (stream.go) only calls the registered
//   context.CancelFunc; it never itself publishes the "cancelled"
//   terminal event or deregisters - that happens exclusively from the
//   streaming/dispatch goroutine's own sync.Once latch, so SSE ordering
//   (job.cancel's HTTP response and the "cancelled" SSE event) is never
//   reversed by this handler racing ahead of it.
// SPORT: conductor.cancel/ADD (P1-E11-W3-S23-T3).

package conductor

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// JobCancelMethod is the job.cancel JSON-RPC method name (§D-21).
const JobCancelMethod = "job.cancel"

// jobCancelParams is job.cancel's decoded request params.
type jobCancelParams struct {
	JobID string `json:"job_id"`
}

// jobCancelResult is job.cancel's result: always {"cancelled": true|false}
// - true when a known in-flight job's cancel func was actually invoked,
// false for the completed/unknown no-op case. Both are SUCCESS results
// (§D-21: "idempotent; success on completed or unknown job_id"); the
// boolean only distinguishes "had an effect" from "was already a no-op"
// for an interested caller, and is never itself an error signal.
type jobCancelResult struct {
	Cancelled bool `json:"cancelled"`
}

// RegisterHandlers mounts "job.cancel" on registry, wiring Executor.Cancel
// to the daemon's real dispatch path. This IS the production caller for
// Cancel: the daemon's composition root (cmd/cascade, out of this
// ticket's files_scope) calls RegisterHandlers once at startup;
// cancel_test.go drives the identical registry.Dispatch entry point a
// real client would use, and proves the wiring is load-bearing by showing
// Dispatch fails with method-not-found when RegisterHandlers is never
// called.
func RegisterHandlers(registry *rpc.Registry, exec *Executor) {
	registry.Register(JobCancelMethod, func(_ context.Context, params json.RawMessage) (any, error) {
		var p jobCancelParams
		if len(params) > 0 {
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "conductor: job.cancel params")
			}
		}
		if p.JobID == "" {
			return nil, cascade.New(cascade.KindInvalidInput, "conductor: job.cancel requires job_id")
		}
		return jobCancelResult{Cancelled: exec.Cancel(JobID(p.JobID))}, nil
	})
}

// Cancel requests cancellation of id. It reports whether id named a
// known, currently in-flight job (its cancel func was invoked) or was a
// no-op (the job already completed, or id was never registered at all -
// the two cases job.cancel's idempotency contract treats identically:
// success, no error, no side effect). Cancel never blocks on the job's
// own completion; it only signals its context and returns.
func (e *Executor) Cancel(id JobID) bool {
	return e.cancels().cancel(id)
}
