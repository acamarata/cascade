package nodes

import "context"

// Purpose (this file): journal continuity across a re-queue — making a
//
//	replacement attempt continue the lost one's work rather than start it
//	over.
//
// THE RULE IT ENFORCES. A mid-run kill leaves records in the entity's
//
//	journal: the lost attempt streamed them back as it went (S-37.T2). Those
//	records are the resume substrate. A replacement that ignored them would
//	re-run everything the first attempt completed — which for idempotent
//	work is merely wasteful and for a journal is worse: the entity's history
//	then contains two accounts of the same operations with no way to tell
//	that they are one.
//
// WHAT "FROM THE CHECKPOINT" MEANS HERE. The reader returns the records
//
//	after the entity's last published checkpoint. That is the same fallback
//	the journal store's own replay uses, and it is deliberately generous: it
//	may hand back an operation the first attempt already finished, and it
//	will never skip one. Re-delivery is recoverable because the node's
//	durable dedup refuses a duplicate action id before it executes; a skip
//	is not recoverable at all.
//
// Inputs: the entity id whose journal holds the lost attempt's records.
// Outputs: the point a replacement attempt starts from.
// SPORT: internal/nodes:continuity (ADD) — P1-E17-W4-S37-T3.

// StreamedRecord is one record already in the entity's journal, in the
// narrow shape this decision needs.
type StreamedRecord struct {
	// Seq is the record's journal sequence.
	Seq uint64
	// Attempt is the fencing number it was produced under.
	Attempt uint64
	// OperationID correlates it with the operation that made it.
	OperationID string
}

// JournalContinuityReader reads what a lost attempt already recorded.
//
// A narrow interface, like this package's other journal seam: internal/**
// may import internal/**, but a package that takes the whole store takes
// its whole surface, and every test then has to build one.
type JournalContinuityReader interface {
	// RecordsSinceCheckpoint returns entityID's records after its last
	// published checkpoint, oldest first. An entity with no records is
	// an empty slice and a nil error, not an error.
	RecordsSinceCheckpoint(ctx context.Context, entityID string) ([]StreamedRecord, error)
}

// ResumePoint is where a replacement attempt picks up.
// It carries JSON TAGS because it travels: the controller decides it and
// the replacement NODE acts on it, over the claim response and the execute
// request (P1-E17-W4-S37-T7). Before that carriage existed the point was
// computed and discarded, and a replacement on a machine that had never
// seen the work re-ran everything the lost attempt had already done.
type ResumePoint struct {
	// EntityID is the journal entity.
	EntityID string `json:"entity_id,omitempty"`
	// Seq is the last sequence the lost attempt recorded, and the point
	// the replacement continues after. Zero means nothing was recorded.
	Seq uint64 `json:"seq,omitempty"`
	// CompletedOperations are the operation ids already accounted for,
	// oldest first and de-duplicated. The replacement carries these so a
	// re-delivered operation is recognised rather than re-run.
	CompletedOperations []string `json:"completed_operations,omitempty"`
	// FromScratch is true only when the entity's journal holds nothing
	// after its checkpoint — the genuine cold start. It is a separate
	// field rather than `Seq == 0` because a caller asserting on it is
	// asserting the thing that matters: whether work was lost. It is
	// carried on the wire for the same reason: a receiver that had to
	// infer it from a zero sequence would be inferring the one thing the
	// field exists to state.
	FromScratch bool `json:"from_scratch,omitempty"`
}

// ResumeFrom builds the resume point for entityID.
//
// A nil reader is an ERROR rather than a cold start. "I could not read the
// journal" and "there is nothing in the journal" produce the same resume
// point and mean opposite things, and confusing them re-runs completed
// work — which is the exact failure journal continuity exists to prevent.
func ResumeFrom(ctx context.Context, reader JournalContinuityReader, entityID string) (ResumePoint, error) {
	if entityID == "" {
		return ResumePoint{}, errUnidentifiedResume()
	}
	if reader == nil {
		return ResumePoint{}, errNoContinuityReader(entityID)
	}
	records, err := reader.RecordsSinceCheckpoint(ctx, entityID)
	if err != nil {
		return ResumePoint{}, err
	}
	point := ResumePoint{EntityID: entityID, FromScratch: len(records) == 0}
	seen := make(map[string]bool, len(records))
	for _, rec := range records {
		if rec.Seq > point.Seq {
			point.Seq = rec.Seq
		}
		if rec.OperationID == "" || seen[rec.OperationID] {
			continue
		}
		seen[rec.OperationID] = true
		point.CompletedOperations = append(point.CompletedOperations, rec.OperationID)
	}
	return point, nil
}

// Completed reports whether operationID is already accounted for at p.
func (p ResumePoint) Completed(operationID string) bool {
	for _, id := range p.CompletedOperations {
		if id == operationID {
			return true
		}
	}
	return false
}
