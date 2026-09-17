package nodes

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the journal-streaming leg — admitting the records a
//
//	node streams back mid-dispatch and appending them to the controller's
//	entity journal.
//
// Inputs: a record from the node, the fencing register, and a sink.
// Outputs: one appended journal entry, or a typed refusal.
// Constraints: no new wire format (06 §5.7) — these records ride the
//
//	existing S-36 node RPC channel, so this file adds no parser and no fuzz
//	target. The SINK is an interface rather than the journal store itself:
//	this package owns the admission DECISIONS (fencing, the static-key
//	refusal, the required fields) and the composition root supplies the
//	store, which is what keeps those decisions reachable without one.
//
//	Fencing extends to journals, not only to results (R-21.221). A
//	partitioned-but-alive node keeps streaming; if its records could still
//	append, the entity's journal would interleave two attempts' accounts of
//	the same work and read as one confused history — which is worse than a
//	gap, because a gap is visible.
//
// SPORT: internal/nodes:journal-stream (ADD) — P1-E17-W4-S37-T2.

// JournalRecord is one record a node streams back during a dispatch.
type JournalRecord struct {
	// DispatchID names the dispatch this record belongs to.
	DispatchID string `json:"dispatch_id"`
	// Attempt is the fencing number the record was produced under.
	Attempt uint64 `json:"attempt"`
	// EntityID is the journal entity the record appends to.
	EntityID string `json:"entity_id"`
	// OperationID correlates the record with the operation that made it.
	OperationID string `json:"operation_id"`
	// Payload is the node's record, in the M/S-27.T1 format, verbatim.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// NodeStreamAppender appends one admitted record to the controller's
// journal as a node-streamed entry.
//
// It is this package's narrow view of the M/S-27.T1 store: one method,
// taking only what a node record can supply. The store itself is wired in
// at the composition root.
type NodeStreamAppender interface {
	AppendNodeStream(ctx context.Context, entityID, operationID string, payload json.RawMessage) error
}

// JournalStreamDeps are the journal leg's collaborators.
type JournalStreamDeps struct {
	// Attempts is the controller's fencing register, consulted so a
	// superseded attempt's records are refused.
	Attempts *AttemptRegister
	// Sink appends an admitted record.
	Sink NodeStreamAppender
}

// StreamJournalRecord admits one streamed record and appends it.
//
// The order is the contract: admit, then stamp, then append. A record that
// cannot be admitted never reaches the store, so a superseded attempt
// cannot leave a partial account of itself behind in the entity's history.
func StreamJournalRecord(ctx context.Context, deps JournalStreamDeps, rec JournalRecord) error {
	if err := admitJournalRecord(deps, rec); err != nil {
		return err
	}
	stamped, err := stampJournalRecord(rec)
	if err != nil {
		return err
	}
	return deps.Sink.AppendNodeStream(ctx, rec.EntityID, rec.OperationID, stamped)
}

// admitJournalRecord applies every rule that can refuse a record.
func admitJournalRecord(deps JournalStreamDeps, rec JournalRecord) error {
	if deps.Sink == nil {
		return cascade.New(cascade.KindInternal,
			"nodes: no journal sink was wired for the dispatch stream")
	}
	if err := requireJournalFields(rec); err != nil {
		return err
	}
	if err := AssertNoStaticKey(rec.DispatchID, rec.Payload); err != nil {
		return err
	}
	if len(rec.Payload) > 0 && !json.Valid(rec.Payload) {
		return cascade.Newf(cascade.KindInvalidInput,
			"nodes: dispatch %s streamed a journal record whose payload is not JSON", rec.DispatchID)
	}
	return fenceJournalRecord(deps.Attempts, rec)
}

// requireJournalFields refuses a record missing an identifier the journal
// cannot be appended without.
func requireJournalFields(rec JournalRecord) error {
	for field, value := range map[string]string{
		"dispatch id":  rec.DispatchID,
		"entity id":    rec.EntityID,
		"operation id": rec.OperationID,
	} {
		if strings.TrimSpace(value) == "" {
			return cascade.Newf(cascade.KindInvalidInput,
				"nodes: a streamed journal record needs a %s", field)
		}
	}
	return nil
}

// fenceJournalRecord refuses a record from any attempt but the current one.
//
// Attempt 0 is refused rather than treated as "unfenced": an unnumbered
// record is exactly what a node built before fencing existed would send,
// and admitting it would defeat the guarantee for every record.
func fenceJournalRecord(reg *AttemptRegister, rec JournalRecord) error {
	if reg == nil {
		return cascade.New(cascade.KindInternal,
			"nodes: no attempt register was wired for the dispatch stream")
	}
	if rec.Attempt == 0 {
		return cascade.Newf(cascade.KindInvalidInput,
			"nodes: dispatch %s streamed a journal record carrying no attempt number", rec.DispatchID)
	}
	if current := reg.Current(rec.DispatchID); rec.Attempt != current {
		return ErrStaleAttemptf(rec.DispatchID, rec.Attempt, current)
	}
	return nil
}

// StreamedEntry is what one admitted record becomes in the journal.
//
// The node's own record is carried VERBATIM in Record rather than merged
// into this object: re-encoding it here would reorder its keys and re-escape
// its strings, so the entry a reader replays would not be the bytes the node
// actually produced.
type StreamedEntry struct {
	DispatchID string          `json:"dispatch_id"`
	Attempt    uint64          `json:"attempt"`
	Record     json.RawMessage `json:"record,omitempty"`
}

// DecodeStreamedEntry reads back what stampJournalRecord wrote.
//
// Exported, with its type, so the composition root's continuity reader
// decodes the SHAPE THIS PACKAGE WROTE rather than a hand-copied struct
// with the same field tags. Two declarations of one wire format drift,
// and the drift here would be silent: a continuity reader that could not
// find the attempt number would report every record as attempt zero, and
// a replacement would resume from a fence nobody minted.
func DecodeStreamedEntry(payload json.RawMessage) (StreamedEntry, error) {
	var entry StreamedEntry
	if err := json.Unmarshal(payload, &entry); err != nil {
		return StreamedEntry{}, cascade.Wrap(cascade.KindIntegrity, err,
			"nodes: decoding a streamed journal record")
	}
	return entry, nil
}

// stampJournalRecord wraps the node's record with its fencing identity.
func stampJournalRecord(rec JournalRecord) (json.RawMessage, error) {
	encoded, err := json.Marshal(StreamedEntry{
		DispatchID: rec.DispatchID,
		Attempt:    rec.Attempt,
		Record:     rec.Payload,
	})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err,
			"nodes: encoding a streamed journal record")
	}
	return encoded, nil
}
