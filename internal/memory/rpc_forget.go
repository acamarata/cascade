package memory

// Purpose: memory.forget — the one verb in this namespace that destroys a
//   user's own record, its params and result types, and its dry-run
//   rehearsal. Split out of rpc.go for the 300-line file cap; kept whole
//   in one file so the destructive semantics and the types that express
//   them are read together.
// Inputs: raw JSON params from an untrusted peer.
// Outputs: a ForgetResult saying what the call did, or a pkg/cascade
//   taxonomy error.
// Constraints: no prompt anywhere in the shipped path (§5.8), so the
//   rehearsal is a flag, not a question.
// SPORT: internal.memory.rpc.Handler (ADD, P1-E07-W2-S13-T3).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ForgetParams is memory.forget's input.
type ForgetParams struct {
	// ID is the canonical "<kind>/<name>" address to retire.
	ID string `json:"id"`
	// DryRun asks what forgetting this address would do without doing
	// it. It exists because this is the one verb in the namespace that
	// destroys a user's own data with no prompt anywhere in the shipped
	// path (§5.8 forbids one), so the only way to look before leaping is
	// a request that says so explicitly.
	DryRun bool `json:"dry_run,omitempty"`
}

// ForgetResult is memory.forget's output. It reports what the call did,
// not merely that it succeeded: a destructive verb whose output is
// indistinguishable from a no-op teaches a caller nothing.
type ForgetResult struct {
	// ID is the canonical address the call addressed.
	ID string `json:"id"`
	// Forgotten is true when the record was actually tombstoned. It is
	// false for a dry run.
	Forgotten bool `json:"forgotten"`
	// DryRun echoes the request's DryRun so a caller reading only the
	// result can tell a rehearsal from the real thing.
	DryRun bool `json:"dry_run"`
}

// Forget serves memory.forget.
//
// # What it removes and what it leaves
//
// It removes exactly one record: the file at the given canonical address.
// It writes a tombstone beside that file FIRST and removes the file
// second (the store's own order), so an interruption between the two
// leaves the deletion in force rather than silently undone — a partial
// forget is still a forget, never a half-deleted record.
//
// It leaves everything else untouched: every other record, including the
// address's lexical neighbours and other records of the same kind; the
// tombstone itself, which is what keeps the deletion durable and is the
// input the S-14.T4 scrub pipeline will consume; and the derived
// projection, whose rows retire on its next run because the files, not
// the rows, are the source of truth.
//
// It does NOT scrub the body from the disk irrecoverably. Removing the
// file is not shredding it, and the tombstone records that the address
// was retired. A caller who needs the bytes destroyed needs the forget
// pipeline, not this verb, and this comment says so rather than letting a
// user infer a guarantee that is not made.
func (h *Handler) Forget(ctx context.Context, params json.RawMessage) (any, error) {
	var p ForgetParams
	if err := decodeParams(MethodForget, params, &p); err != nil {
		return nil, err
	}
	kind, name, err := ParseAddress(p.ID)
	if err != nil {
		return nil, err
	}
	if p.DryRun {
		return h.forgetDryRun(ctx, kind, name)
	}
	if err := h.store.Delete(ctx, kind, name); err != nil {
		return nil, err
	}
	return ForgetResult{ID: recordID(kind, name), Forgotten: true}, nil
}

// forgetDryRun answers what a forget would do without doing it. It still
// refuses an address with no live record, so a rehearsal that succeeds
// means the real call will find something to remove.
func (h *Handler) forgetDryRun(ctx context.Context, kind MemoryKind, name string) (any, error) {
	present, err := h.store.Exists(ctx, kind, name)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, cascade.Wrapf(cascade.KindNotFound, ErrNoSuchEntry,
			"cannot forget absent memory record %s", recordID(kind, name))
	}
	return ForgetResult{ID: recordID(kind, name), DryRun: true}, nil
}
