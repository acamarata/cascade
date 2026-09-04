package review

// Purpose: the memory.review.* JSON-RPC methods and the Register call the
//   daemon composition root makes, so the review queue is reachable from a
//   running daemon rather than merely built.
// Inputs: raw JSON params from an untrusted peer.
// Outputs: ListResult / ActResult, or a pkg/cascade taxonomy error the RPC
//   layer maps to a wire code.
// Constraints: params decode into concrete structs, never interface{} —
//   this is the package's external-input decoder, which is why
//   FuzzReviewRPCParams drives it; memory.review.list writes nothing, so a
//   peer cannot change anything by reading.
// SPORT: internal/memory/review (ADD, P1-E07-W2-S14-T3).

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The two method names of the memory.review.* namespace. They are
// constants because the daemon registers them and the CLI calls them by
// the same name; a literal typed twice is a namespace that half-exists.
const (
	// MethodReviewList returns the review queue. It is a pure read.
	MethodReviewList = "memory.review.list"
	// MethodReviewAct carries out one explicit action on one candidate.
	MethodReviewAct = "memory.review.act"
)

// Handler serves the memory.review.* namespace over one Queue.
type Handler struct {
	queue *Queue
}

// NewHandler returns a Handler serving q.
func NewHandler(q *Queue) *Handler { return &Handler{queue: q} }

// Register binds both methods on r. Without this call the queue is built,
// tested and unreachable from a running daemon, which is the failure mode
// this repository's test-only gate exists to make visible.
func (h *Handler) Register(r *rpc.Registry) {
	r.Register(MethodReviewList, h.List)
	r.Register(MethodReviewAct, h.Act)
}

// Compile-time proof that both methods still satisfy the router's handler
// signature, so a drifting signature fails the build here rather than at
// the composition root.
var (
	_ rpc.HandlerFunc = (*Handler)(nil).List
	_ rpc.HandlerFunc = (*Handler)(nil).Act
)

// List serves memory.review.list. It reads the queue and writes nothing:
// no peer can promote, retire or hide a candidate by listing.
func (h *Handler) List(ctx context.Context, params json.RawMessage) (any, error) {
	var p ListParams
	if err := decodeParams(MethodReviewList, params, &p); err != nil {
		return nil, err
	}
	return h.queue.List(ctx, p)
}

// Act serves memory.review.act.
//
// Errors: KindInvalidInput for malformed params, an unknown action, an
// unusable address or an out-of-range defer window; KindNotFound for an
// address with no candidate; KindConflict for an action the candidate's
// status does not admit (approving a promoted candidate, reverting one
// that was never promoted, deferring one that is not pending);
// KindUnavailable when the file system fails.
func (h *Handler) Act(ctx context.Context, params json.RawMessage) (any, error) {
	var p ActParams
	if err := decodeParams(MethodReviewAct, params, &p); err != nil {
		return nil, err
	}
	return h.queue.Act(ctx, p)
}

// decodeParams unmarshals raw params into dst, mirroring internal/memory's
// decoder: absent params decode as an empty object rather than a refusal,
// so a method whose fields are all optional stays callable with no params
// member, and every method still validates its own required fields
// afterwards.
//
// This is the external-input decoder for the whole namespace — every byte
// it sees comes from a peer — which is why FuzzReviewRPCParams drives it
// rather than driving json.Unmarshal directly.
func decodeParams(method string, params json.RawMessage, dst any) error {
	trimmed := strings.TrimSpace(string(params))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, err,
			"%s: malformed params", method)
	}
	return nil
}
