package forget

// Purpose: the trace list — the enumeration of every place a memory record
//   leaves a mark and what this pipeline did about each. It is the
//   verification half of the forget: a destructive verb that returns only
//   "ok" invites a user to believe more was destroyed than was.
// Inputs: one Forget call's finished state and the index trace it
//   produced.
// Outputs: a memory.ForgetOutcome whose Traces cover every mark, in a
//   fixed order.
// Constraints: the list is EXHAUSTIVE and its order is fixed, so two runs
//   of the same forget report the same thing and a reader can diff them.
//   No entry may claim a removal the call did not perform.
// SPORT: internal/memory/forget (ADD, P1-E07-W2-S14-T4).

import "github.com/acamarata/cascade/internal/memory"

// outcome assembles what the caller is told.
func (p *Pipeline) outcome(st state, trace memory.IndexTrace) memory.ForgetOutcome {
	return memory.ForgetOutcome{
		ID:               st.id,
		Forgotten:        st.acted,
		AlreadyForgotten: st.done,
		Traces:           p.traces(st, trace),
		Index:            trace,
		EventEmitted:     st.acct.EventEmitted,
		EventError:       st.eventErr,
	}
}

// traces enumerates every mark, in a fixed order.
//
// The list is deliberately longer than the set of things this pipeline
// removes. Three of its entries exist precisely to report what SURVIVES a
// forget, and one to report what the pipeline cannot reach at all. A user
// who asks for something to be forgotten is owed that list, not a return
// code, and an entry may only be dropped from it when the mark it names
// stops existing.
func (p *Pipeline) traces(st state, trace memory.IndexTrace) []memory.ForgetTrace {
	out := []memory.ForgetTrace{p.recordTrace(st), tombstoneTrace(), accountTrace()}
	out = append(out, p.indexTraces(trace)...)
	return append(out, p.eventTrace(st), ledgerTrace(), consolidationTrace(),
		stalenessTrace(), bytesTrace())
}

// recordTrace reports the record file itself.
func (p *Pipeline) recordTrace(st state) memory.ForgetTrace {
	t := memory.ForgetTrace{Place: "record file", Disposition: memory.ForgetRemoved}
	switch {
	case st.acted:
		t.Detail = "the markdown file was unlinked after its tombstone was written"
	default:
		t.Detail = "the markdown file was already gone when this call ran"
	}
	return t
}

// tombstoneTrace reports the marker that keeps the deletion durable.
func tombstoneTrace() memory.ForgetTrace {
	return memory.ForgetTrace{Place: "tombstone", Disposition: memory.ForgetRetained,
		Detail: "kept on purpose: removing it would bring the record back on the next scan"}
}

// accountTrace reports this pipeline's own account file, naming it so a
// user can go and read what was recorded about them.
func accountTrace() memory.ForgetTrace {
	return memory.ForgetTrace{Place: "forget account", Disposition: memory.ForgetRetained,
		Detail: "kept on purpose: it records the address, the time and the reason, " +
			"and never the record's text"}
}

// indexTraces report the projection's two legs separately, because they
// fail and are configured separately.
func (p *Pipeline) indexTraces(trace memory.IndexTrace) []memory.ForgetTrace {
	if p.index == nil {
		return []memory.ForgetTrace{
			{Place: "projection rows and postings", Disposition: memory.ForgetNotConfigured,
				Detail: "no index is wired to this pipeline, so none was scrubbed"},
			{Place: "vector index", Disposition: memory.ForgetNotConfigured,
				Detail: "no index is wired to this pipeline, so no vector was removed"},
		}
	}
	rows := memory.ForgetTrace{Place: "projection rows and postings",
		Disposition: memory.ForgetRemoved,
		Detail:      "the row and every posting it wrote were retracted"}
	if !trace.Row {
		rows.Detail = "the projection held no row for this address"
	} else if trace.RowUnreadable {
		rows.Detail = "the row was removed; it could not be decoded, so its postings are unknown"
	}
	return []memory.ForgetTrace{rows, vectorTrace(trace)}
}

// vectorTrace reports the vector leg.
func vectorTrace(trace memory.IndexTrace) memory.ForgetTrace {
	t := memory.ForgetTrace{Place: "vector index", Disposition: memory.ForgetRemoved,
		Detail: "the embedding for this address was deleted"}
	switch {
	case !trace.VectorProbed:
		t.Disposition = memory.ForgetNotConfigured
		t.Detail = "no vector index is configured, so nothing was checked or removed"
	case !trace.Vector:
		t.Detail = "the vector index held no embedding for this address"
	}
	return t
}

// eventTrace reports the backup-aware note.
func (p *Pipeline) eventTrace(st state) memory.ForgetTrace {
	t := memory.ForgetTrace{Place: "backup and sync note", Disposition: memory.ForgetRetained,
		Detail: "a MemoryForgotten event carrying the address, time and reason was " +
			"published so a restore does not bring the record back"}
	if !st.acct.EventEmitted {
		t.Disposition = memory.ForgetUnreachable
		t.Detail = "the event did not reach its sink, so a restore may return this record"
	}
	return t
}

// ledgerTrace reports the candidate ledger and the review queue over it.
//
// This is the pipeline's largest honest gap and it is stated plainly. A
// promoted candidate's record holds the DRAFT the promotion wrote, which
// is the same text as the record being forgotten, and the CandidateLedger
// contract has no delete: nothing this pipeline can call removes it.
func ledgerTrace() memory.ForgetTrace {
	return memory.ForgetTrace{Place: "candidate ledger and review queue",
		Disposition: memory.ForgetUnreachable,
		Detail: "a candidate recorded for this address keeps its draft, which repeats " +
			"the record's text; the ledger contract has no delete for this pipeline to call"}
}

// consolidationTrace reports the consolidation account.
//
// It is kept on purpose. A consolidation account is the only surviving
// explanation of the OTHER records that were retired into a survivor, and
// rewriting it to erase one member would destroy that account for members
// nobody asked to forget.
func consolidationTrace() memory.ForgetTrace {
	return memory.ForgetTrace{Place: "consolidation account",
		Disposition: memory.ForgetRetained,
		Detail: "kept on purpose: it explains what happened to other records, and " +
			"editing it would take their explanation away"}
}

// stalenessTrace reports the staleness queue, which is recomputed rather
// than edited.
func stalenessTrace() memory.ForgetTrace {
	return memory.ForgetTrace{Place: "staleness queue", Disposition: memory.ForgetRetained,
		Detail: "holds addresses only, never text, and the next scan recomputes it " +
			"from the files without this record"}
}

// bytesTrace is the claim this pipeline refuses to make.
func bytesTrace() memory.ForgetTrace {
	return memory.ForgetTrace{Place: "record bytes on disk", Disposition: memory.ForgetUnreachable,
		Detail: "the file was unlinked, not shredded; the bytes may remain recoverable " +
			"from the file system, from a backup, or from a snapshot"}
}
