// Package forget implements the memory forget pipeline: the destructive
// half of the memory system, and the only path that removes a record a
// user asked to be rid of.
//
// It sits beside internal/memory rather than inside it on purpose. The
// store's job is to hold records and answer for them; retiring one reaches
// past the store into the derived index and the event bus, and keeping
// that orchestration in its own package is what makes the dependency point
// one way: this package knows about the store, and the store never knows
// about this one. The RPC surface reaches it through the memory.Forgetter
// seam.
//
// # What "forgotten" means here
//
// Precisely, and no more than that. A forget removes the record file, the
// projected row, its postings and its vector. It deliberately KEEPS the
// tombstone, its own account of the retirement, the consolidation account
// of any group the record belonged to, and the event that tells the backup
// lane not to restore it. It CANNOT reach the candidate ledger, and it
// does not shred bytes. Every one of those, kept and unreachable alike, is
// reported in the outcome of every call, so a caller never has to infer a
// guarantee from silence.
package forget

// Purpose: the forget pipeline — the backend behind `cascade memory
//   forget`. It writes an account of the retirement, scrubs the derived
//   index, retires the record in the store, and tells the backup lane, in
//   that order and idempotently.
// Inputs: a canonical "<kind>/<name>" address, the caller's reason, a
//   record store, an optional index scrubber, an event sink and a clock.
// Outputs: a memory.ForgetOutcome enumerating every mark this record
//   leaves and what became of it, or a pkg/cascade taxonomy error.
// Constraints: no bare clock; every refusal is a taxonomy error; the
//   pipeline never removes a record it was not asked for, and never claims
//   to have removed a mark it did not check.
// SPORT: internal/memory/forget (ADD, P1-E07-W2-S14-T4).

import (
	"context"

	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/pkg/cascade"
)

// RecordStore is the memory store as this pipeline needs it: it asks
// whether a record is live and it retires one. Declaring the two methods
// here rather than taking the whole MemoryStore is what proves, at the
// type level, that a forget cannot read, write or list anything.
type RecordStore interface {
	// Exists reports whether a live record is stored under kind and name.
	Exists(ctx context.Context, kind memory.MemoryKind, name string) (bool, error)
	// Delete tombstones a record.
	Delete(ctx context.Context, kind memory.MemoryKind, name string) error
}

// IndexScrubber is the derived index as this pipeline needs it: one call
// that removes every trace of one address and reports what it removed.
// *memory.ProjectionJob satisfies it.
type IndexScrubber interface {
	// ScrubRecord removes one address's row, postings and vector.
	ScrubRecord(ctx context.Context, id string) (memory.IndexTrace, error)
}

// Pipeline retires one memory record and everything derived from it.
//
// # The order of operations, and why it is this order
//
// A forget touches three things that can each survive a crash: an account
// file, the derived index, and the record itself. The order is account,
// then index, then record, then event.
//
//	The ACCOUNT IS FIRST because it is the only thing that can make an
//	interruption legible. Every state after it says, on disk, which
//	address was being retired, when, why, and how far the work got.
//
//	The INDEX IS SCRUBBED BEFORE THE FILE IS REMOVED, not after. The
//	crash window between the two therefore leaves a record that is still
//	on disk, still readable, still listed and still recallable by the
//	file-scanning verbs, and merely missing from a DERIVED index that the
//	next projection run rebuilds from the files anyway. The reverse order
//	would leave the opposite: a record the user has been told is gone,
//	still answering searches out of a stale row that carries its body.
//	Between "the index is briefly behind the files", which this package
//	already documents as the normal state, and "a forgotten record is
//	briefly still findable", only one is acceptable for this verb.
//
//	The EVENT IS LAST because it announces a completed fact. A backup
//	lane told to exclude a record that then failed to be removed would be
//	the one wrong claim this pipeline could make that nothing downstream
//	could check.
//
// # Idempotence and resumption
//
// Forget may be called any number of times on the same address. A call
// that finds a COMPLETED account does nothing and reports
// AlreadyForgotten. A call that finds an INCOMPLETE one resumes: every
// step is safe to repeat, the scrub because scrubbing an absent address
// removes nothing, the delete because it is skipped when the record is
// already gone, and the event because it is emitted only once the account
// says it has not been. So no interruption leaves work that no later call
// can finish.
//
// # What it does not do
//
// It does not shred bytes, it does not rewrite the consolidation account
// of any group this record belonged to, and it does not reach the
// candidate ledger. Those are not omissions to be inferred from silence:
// each is reported in the outcome's trace list on every call, with the
// reason, so a caller is never left to assume a guarantee that was not
// made.
type Pipeline struct {
	base  string
	store RecordStore
	index IndexScrubber
	sink  memory.ForgetEventSink
	clock memory.Clock
}

// NewPipeline returns a pipeline over the store tree rooted at base,
// taking its timestamps from clk and announcing retirements to sink. A nil
// sink discards events, which is the documented no-bus configuration; the
// outcome still reports that the event went nowhere.
func NewPipeline(base string, store RecordStore, clk memory.Clock, sink memory.ForgetEventSink) *Pipeline {
	if sink == nil {
		sink = memory.DiscardForgetEvents()
	}
	return &Pipeline{base: base, store: store, sink: sink, clock: clk}
}

// WithIndex attaches the derived index this pipeline scrubs. Without it
// the pipeline runs no scrub and says so in every outcome, rather than
// reporting a clean index it never looked at.
func (p *Pipeline) WithIndex(ix IndexScrubber) *Pipeline {
	p.index = ix
	return p
}

// state is one Forget call's working set: the account, where it lives, and
// what the store said about the record when the call began.
type state struct {
	id    string
	path  string
	acct  account
	live  bool
	done  bool
	acted bool
	// eventErr is why the backup-aware note did not reach its sink, empty
	// when it did or when nothing needed emitting.
	eventErr string
}

// Forget retires one record by canonical address.
//
// It returns ErrNoSuchEntry only when there is nothing to retire and no
// record of a previous attempt: an address whose forget was interrupted
// still resumes, even though the record itself is already gone.
func (p *Pipeline) Forget(ctx context.Context, entityID, reason string) (memory.ForgetOutcome, error) {
	if err := ctx.Err(); err != nil {
		return memory.ForgetOutcome{}, cascade.Wrap(cascade.KindCanceled, err, "memory forget canceled")
	}
	kind, name, err := memory.ParseAddress(entityID)
	if err != nil {
		return memory.ForgetOutcome{}, err
	}
	st, err := p.begin(ctx, kind, name, reason)
	if err != nil {
		return memory.ForgetOutcome{}, err
	}
	if st.done {
		return p.outcome(st, memory.IndexTrace{ID: st.id}), nil
	}
	trace, err := p.retire(ctx, kind, name, &st)
	if err != nil {
		return memory.ForgetOutcome{}, err
	}
	p.announce(ctx, &st)
	return p.outcome(st, trace), nil
}

// begin loads or opens the account and decides whether there is anything
// left to do. It writes the account BEFORE the caller removes anything, so
// the intent is durable before the first destructive step.
func (p *Pipeline) begin(
	ctx context.Context, kind memory.MemoryKind, name, reason string,
) (state, error) {
	st := state{id: memory.Address(kind, name), path: accountPath(p.base, kind, name)}
	acct, found, err := loadAccount(st.path)
	if err != nil {
		return st, err
	}
	st.acct = acct
	if found && acct.Completed {
		st.done = true
		return st, nil
	}
	live, err := p.store.Exists(ctx, kind, name)
	if err != nil {
		return st, err
	}
	st.live = live
	if !live && !found {
		return st, cascade.Wrapf(cascade.KindNotFound, memory.ErrNoSuchEntry,
			"cannot forget absent memory record %s", st.id)
	}
	if !found {
		st.acct = newAccount(st.id, reason, p.clock.Now())
		if serr := saveAccount(st.path, st.acct); serr != nil {
			return st, serr
		}
	}
	return st, nil
}

// retire scrubs the index and then removes the record, recording each step
// in the account as it completes.
func (p *Pipeline) retire(
	ctx context.Context, kind memory.MemoryKind, name string, st *state,
) (memory.IndexTrace, error) {
	trace := memory.IndexTrace{ID: st.id}
	if p.index != nil {
		scrubbed, err := p.index.ScrubRecord(ctx, st.id)
		if err != nil {
			return trace, err
		}
		trace = scrubbed
		st.acct.IndexScrubbed = true
		if serr := saveAccount(st.path, st.acct); serr != nil {
			return trace, serr
		}
	}
	if !st.live {
		return trace, nil
	}
	if err := p.store.Delete(ctx, kind, name); err != nil {
		return trace, err
	}
	now := p.clock.Now().UTC()
	st.acct.Tombstoned, st.acct.DeletedAt, st.acted = true, &now, true
	return trace, saveAccount(st.path, st.acct)
}

// announce emits the MemoryForgotten event and completes the account.
//
// An emit failure is recorded, not returned: by this point the record is
// gone, and reporting a failure to the caller would say the forget did not
// happen when it did. The account stays incomplete so a later call retries
// the emit rather than silently deciding the backup lane was told.
func (p *Pipeline) announce(ctx context.Context, st *state) {
	if !st.acct.EventEmitted {
		at := p.clock.Now().UTC()
		if st.acct.DeletedAt != nil {
			at = st.acct.DeletedAt.UTC()
		}
		if err := p.sink.MemoryForgotten(ctx, memory.ForgottenEvent{
			EntityID: st.acct.EntityID, Timestamp: at, Reason: st.acct.Reason,
		}); err != nil {
			st.eventErr = err.Error()
			return
		}
		st.acct.EventEmitted = true
	}
	st.acct.Completed = true
	if serr := saveAccount(st.path, st.acct); serr != nil {
		st.eventErr = serr.Error()
	}
}
