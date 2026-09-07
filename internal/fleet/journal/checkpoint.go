package journal

// Purpose: Checkpoint — atomically publishing an entity's replay cursor
//
//	together with the sequence it covers, so a reader can never observe a
//	cursor that claims coverage the log does not have (R-21.216).
//
// Inputs: a Cursor naming the entity and the sequence to publish.
// Outputs: nil on success (including the re-checkpoint no-op case), or a
//
//	pkg/cascade taxonomy error.
//
// SPORT: internal.fleet.journal.Store/CHANGED (Checkpoint) (P1-E13-W3-S27-T1).

import (
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// checkpointKeyPrefix namespaces checkpoint records apart from entry and
// head records within the same provider.Store namespace.
const checkpointKeyPrefix = "c:"

func checkpointKey(entityID string) string { return checkpointKeyPrefix + entityID + keyDelim }

// checkpointRecord is the durable form of a published cursor: the cursor's
// own Seq plus CoveredSeq, the entity's log head at the moment the
// checkpoint was published. Persisting both in one record, written in one
// transaction, is what makes "a reader never observes a cursor claiming
// coverage the log does not have" true by construction rather than by
// convention.
type checkpointRecord struct {
	Seq        uint64 `json:"seq"`
	CoveredSeq uint64 `json:"covered_seq"`
}

// Checkpoint atomically publishes cursor together with the sequence range
// it covers. Checkpointing a Seq beyond the entity's current log head is
// refused (ErrCheckpointBeyondLog): a checkpoint may only claim coverage
// the log has actually reached. Re-checkpointing the same cursor against
// an unchanged head is a no-op: no write occurs and the call still
// succeeds.
func (s *SQLiteStore) Checkpoint(ctx context.Context, cursor Cursor) error {
	if cursor.EntityID == "" {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEntry, "checkpoint requires an entity id")
	}
	lock := s.lockFor(cursor.EntityID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.recoverEntityLocked(ctx, cursor.EntityID); err != nil {
		return err
	}
	if err := s.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return s.checkpointTx(ctx, tx, cursor)
	}); err != nil {
		return wrapStore(err, "journal: checkpointing")
	}
	return nil
}

// checkpointTx is Checkpoint's body, run inside one write-executor
// transaction so the head read, the beyond-log refusal, and the write all
// see (and commit against) the same consistent snapshot.
func (s *SQLiteStore) checkpointTx(ctx context.Context, tx provider.Tx, cursor Cursor) error {
	head, err := s.loadHeadFrom(ctx, tx, cursor.EntityID)
	if err != nil {
		return err
	}
	if cursor.Seq > head {
		return cascade.Wrapf(cascade.KindConflict, ErrCheckpointBeyondLog,
			"entity %s: checkpoint seq %d exceeds log head %d", cursor.EntityID, cursor.Seq, head)
	}
	existing, ok, err := s.loadCheckpointFrom(ctx, tx, cursor.EntityID)
	if err != nil {
		return err
	}
	if ok && existing.Seq == cursor.Seq && existing.CoveredSeq == head {
		return nil // re-checkpointing the same cursor is a no-op
	}
	data, err := json.Marshal(checkpointRecord{Seq: cursor.Seq, CoveredSeq: head})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "journal: encoding checkpoint")
	}
	return tx.Put(ctx, s.namespace, checkpointKey(cursor.EntityID), data)
}

// loadCheckpointFrom reads entityID's last published checkpoint through g.
// ok is false when no checkpoint has ever been published for entityID.
func (s *SQLiteStore) loadCheckpointFrom(ctx context.Context, g getter, entityID string) (checkpointRecord, bool, error) {
	data, err := g.Get(ctx, s.namespace, checkpointKey(entityID))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return checkpointRecord{}, false, nil
		}
		return checkpointRecord{}, false, wrapStore(err, "journal: reading checkpoint")
	}
	var rec checkpointRecord
	if uerr := json.Unmarshal(data, &rec); uerr != nil {
		return checkpointRecord{}, false, cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
			"checkpoint record for entity %s is not decodable: %v", entityID, uerr)
	}
	return rec, true, nil
}
