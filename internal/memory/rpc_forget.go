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
	// Reason is the caller's stated reason for retiring the record. It is
	// recorded in the forget account and carried on the MemoryForgotten
	// event, and it is never required: a user forgetting their own record
	// owes nobody an explanation, and demanding one would put a prompt in
	// the path of the one verb §5.8 forbids prompting in.
	Reason string `json:"reason,omitempty"`
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
	// AlreadyForgotten is true when a previous, completed forget of this
	// address had already retired it. The call did nothing and that is not
	// a failure, but it is also not the same event as retiring a live
	// record, so it is reported rather than folded into Forgotten.
	AlreadyForgotten bool `json:"already_forgotten,omitempty"`
	// Traces enumerate every place this record leaves a mark and what the
	// call did about each. This is the field that keeps the verb honest:
	// a destructive command that returns only success invites a user to
	// believe more was destroyed than was, and this list says exactly
	// which marks were removed, which were kept on purpose, and which the
	// pipeline cannot reach at all.
	Traces []ForgetTrace `json:"traces,omitempty"`
	// Index counts what the projection actually held for this address and
	// gave up. It is zero-valued when no index was configured; the trace
	// list says which of the two it was.
	Index IndexTrace `json:"index"`
	// EventEmitted reports that the MemoryForgotten event reached its
	// sink, and EventError says why it did not. A failed emit does not
	// fail the call, because the record is already gone by then, but a
	// backup lane that was never told is worth saying out loud.
	EventEmitted bool   `json:"event_emitted,omitempty"`
	EventError   string `json:"event_error,omitempty"`
}

// ForgetDisposition says what a forget did about one place a record leaves
// a mark. The four values are exhaustive by construction: every trace the
// pipeline reports is removed, kept deliberately, out of the pipeline's
// reach, or not a thing this store has at all.
type ForgetDisposition string

// The four dispositions.
const (
	// ForgetRemoved means the mark is gone.
	ForgetRemoved ForgetDisposition = "removed"
	// ForgetRetained means the mark is deliberately kept, and the detail
	// says why. A tombstone is the clearest case: removing it would undo
	// the deletion.
	ForgetRetained ForgetDisposition = "retained"
	// ForgetUnreachable means the mark exists and this pipeline cannot
	// remove it. It is reported rather than omitted, because a user who
	// asked to be forgotten is owed the list of what is still there.
	ForgetUnreachable ForgetDisposition = "unreachable"
	// ForgetNotConfigured means the subsystem holding the mark was not
	// wired into this pipeline, so nothing was checked and nothing was
	// removed. It is not the same as "there was nothing there".
	ForgetNotConfigured ForgetDisposition = "not_configured"
)

// ForgetTrace is one place a memory record leaves a mark, and what the
// forget did about it.
type ForgetTrace struct {
	// Place names the store, index or log the mark lives in.
	Place string `json:"place"`
	// Disposition says what happened to it.
	Disposition ForgetDisposition `json:"disposition"`
	// Detail is one sentence of plain language: why it was kept, why it
	// could not be reached, or what exactly was removed.
	Detail string `json:"detail"`
}

// ForgetOutcome is what a forget pipeline reports back. It is declared
// here, in the package that serves memory.forget, so the pipeline can live
// in its own package without this one importing it: the RPC layer depends
// on the SHAPE of an outcome, never on a particular pipeline.
type ForgetOutcome struct {
	// ID is the canonical address that was addressed.
	ID string
	// Forgotten is true when THIS call retired a live record.
	Forgotten bool
	// AlreadyForgotten is true when a completed forget had already done
	// so.
	AlreadyForgotten bool
	// Traces enumerate every mark and its disposition.
	Traces []ForgetTrace
	// Index counts what the projection gave up.
	Index IndexTrace
	// EventEmitted and EventError report the backup-aware note.
	EventEmitted bool
	EventError   string
}

// Forgetter is the forget pipeline as this handler needs it.
//
// The pipeline is more than a delete: it writes an account of the
// retirement before anything is removed, scrubs the derived index, and
// tells the backup lane. Declaring the seam here rather than importing the
// pipeline keeps the dependency pointing one way, and it is why a Handler
// built with no pipeline still works and says so.
type Forgetter interface {
	// Forget retires one record by canonical address, reporting every
	// mark it removed, kept or could not reach.
	Forget(ctx context.Context, entityID, reason string) (ForgetOutcome, error)
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
	if h.forget != nil {
		out, ferr := h.forget.Forget(ctx, recordID(kind, name), p.Reason)
		if ferr != nil {
			return nil, ferr
		}
		return ForgetResult{
			ID: out.ID, Forgotten: out.Forgotten, AlreadyForgotten: out.AlreadyForgotten,
			Traces: out.Traces, Index: out.Index,
			EventEmitted: out.EventEmitted, EventError: out.EventError,
		}, nil
	}
	if err := h.store.Delete(ctx, kind, name); err != nil {
		return nil, err
	}
	return ForgetResult{ID: recordID(kind, name), Forgotten: true, Traces: unpipelinedTraces()}, nil
}

// unpipelinedTraces is the trace list of a Handler built with no forget
// pipeline. It exists so the honest answer is the DEFAULT answer: such a
// handler removed the file and wrote a tombstone, ran no index scrub and
// told no backup lane, and a caller reading the result learns that
// instead of inferring a completeness nobody promised.
func unpipelinedTraces() []ForgetTrace {
	return []ForgetTrace{
		{Place: "record file", Disposition: ForgetRemoved,
			Detail: "the markdown file was unlinked after its tombstone was written"},
		{Place: "tombstone", Disposition: ForgetRetained,
			Detail: "the tombstone is what keeps the deletion durable across an interruption"},
		{Place: "projection rows and postings", Disposition: ForgetNotConfigured,
			Detail: "no forget pipeline is wired to this handler, so no index was scrubbed"},
		{Place: "vector index", Disposition: ForgetNotConfigured,
			Detail: "no forget pipeline is wired to this handler, so no vector was removed"},
		{Place: "backup and sync note", Disposition: ForgetNotConfigured,
			Detail: "no MemoryForgotten event was emitted, so a restore may return this record"},
		{Place: "record bytes on disk", Disposition: ForgetUnreachable,
			Detail: "the file was unlinked, not shredded; the bytes may remain recoverable"},
	}
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
