package jobs

// Purpose: EvidenceLedger.Append's audit-effect wrapping (R-16.47, C16). An
//
//	intent effect record is committed BEFORE the evidence row, the row is
//	written in its own store transaction, and the terminal record follows
//	it, so a row never exists without an audit record. A crash between the
//	two leaves an open intent that RecoverEffects settles by probing the
//	row by (job_id, idempotency_key): row present = Confirmed, absent =
//	Failed. The probe is definitive, so no unknown-outcome state is used.
//
// Inputs: an EvidenceRecord already validated and authorized by Append.
// Outputs: the persisted (or already-present) record, or a typed error.
// Constraints: key derivation and parsing live in evidence_effect_key.go; a
//
//	begun key is begun for good, so a retry after a failed attempt uses
//	the next attempt key (at most evidenceEffectMaxTries per row).
//
// SPORT: jobs/completion-gate/ADD (P1-SEC-21).

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
)

const (
	evidenceEffectMaxTries = 16
	evidenceEffectActor    = "jobs.evidence"
)

// evidenceEffectRequest names the effect for rec's attempt-th try. The
// params hash binds the intent to the row's content, so the audit trail
// shows what was about to be recorded without storing it.
func evidenceEffectRequest(rec EvidenceRecord, attempt int) audit.EffectRequest {
	return audit.EffectRequest{
		Kind: audit.KindPolicyDecide, Actor: evidenceEffectActor,
		Action: "evidence.append:" + string(rec.Kind), ParamsHash: evidenceContentHash(rec),
		Key: evidenceEffectKey(rec.JobID, rec.IdempotencyKey, attempt),
	}
}

// evidenceContentHash digests the fields an intent promises to record, each
// escaped so no two distinct field tuples join to the same text.
func evidenceContentHash(rec EvidenceRecord) string {
	parts := []string{string(rec.Kind), string(rec.Outcome), rec.ResultHash, rec.AttemptID, rec.TreeHash}
	for i, p := range parts {
		parts[i] = escapeEffectPart(p)
	}
	return audit.HashParams([]byte(strings.Join(parts, ":")))
}

// isEffectExists reports whether err is audit.ErrEffectExists itself. The
// taxonomy's errors.Is compares Kind only, so identity is checked by hand.
func isEffectExists(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e == error(audit.ErrEffectExists) {
			return true
		}
	}
	return false
}

// lookup returns the row already carrying idem for jobID, if any.
func (l *EvidenceLedger) lookup(ctx context.Context, jobID, idem string) (EvidenceRecord, bool, error) {
	var out EvidenceRecord
	var ok bool
	err := l.store.withTx(ctx, func(tx *sql.Tx) error {
		var err error
		out, ok, err = existingByIdempotencyKeyTx(ctx, tx, jobID, idem)
		return err
	})
	return out, ok, err
}

// appendWithEffect begins an intent, writes the row, then settles the
// intent. A BeginEffect failure writes no row. A key already begun by an
// earlier attempt moves on to the next attempt key: the row transaction
// re-checks the idempotency key, so racing attempts still write one row.
func (l *EvidenceLedger) appendWithEffect(ctx context.Context, rec EvidenceRecord) (EvidenceRecord, error) {
	for attempt := 0; attempt < evidenceEffectMaxTries; attempt++ {
		h, err := l.effects.BeginEffect(ctx, evidenceEffectRequest(rec, attempt))
		if err == nil {
			return l.writeUnderEffect(ctx, rec, h)
		}
		if isEffectExists(err) {
			if existing, ok, lerr := l.lookup(ctx, rec.JobID, rec.IdempotencyKey); lerr != nil || ok {
				return existing, lerr
			}
			continue
		}
		if h.Key != "" {
			_, _ = l.effects.FailEffect(context.WithoutCancel(ctx), h, "intent logged but not acknowledged: "+err.Error())
		}
		return EvidenceRecord{}, err
	}
	return EvidenceRecord{}, cascade.Newf(cascade.KindConflict,
		"jobs: evidence %q already has %d audited attempts and no row", rec.IdempotencyKey, evidenceEffectMaxTries)
}

// writeUnderEffect inserts the row inside h's intent and settles h. The
// terminal calls ignore ctx cancellation: the row is already decided.
func (l *EvidenceLedger) writeUnderEffect(ctx context.Context, rec EvidenceRecord, h audit.EffectHandle) (EvidenceRecord, error) {
	done := context.WithoutCancel(ctx)
	out, existed, err := l.insertRow(ctx, rec)
	if err != nil {
		return l.settleFailedInsert(done, rec, h, err)
	}
	if existed {
		_, ferr := l.effects.FailEffect(done, h, "idempotency key already recorded; no new row")
		return out, ferr
	}
	return l.confirmRow(done, h, out)
}

// confirmRow writes h's confirmed record for the committed row out.
func (l *EvidenceLedger) confirmRow(done context.Context, h audit.EffectHandle, out EvidenceRecord) (EvidenceRecord, error) {
	if _, err := l.effects.ConfirmEffect(done, h, string(out.Outcome)); err != nil {
		return EvidenceRecord{}, cascade.Wrap(cascade.KindUnavailable, err,
			"jobs: evidence row written but its audit confirmation failed; RecoverEffects settles the intent")
	}
	return out, nil
}

// settleFailedInsert handles an insertRow error. The error may have come
// after the commit persisted (a lost acknowledgement), and a Failed record
// means "definitively did not happen", so the row is probed first. A row
// with the intent's own content = Confirmed; a row with other content is a
// racing attempt's, so this intent is Failed and that row is returned as
// the no-op result; no row = Failed. If the probe itself fails the intent
// stays open for RecoverEffects.
func (l *EvidenceLedger) settleFailedInsert(done context.Context, rec EvidenceRecord, h audit.EffectHandle, insertErr error) (EvidenceRecord, error) {
	row, found, perr := l.lookup(done, rec.JobID, rec.IdempotencyKey)
	if perr != nil {
		return EvidenceRecord{}, errors.Join(insertErr, perr)
	}
	if found && evidenceContentHash(row) == evidenceContentHash(rec) {
		return l.confirmRow(done, h, row)
	}
	reason := "evidence row not written: " + insertErr.Error()
	if found {
		reason = "idempotency key recorded by another attempt; no new row"
	}
	if _, ferr := l.effects.FailEffect(done, h, reason); ferr != nil {
		return EvidenceRecord{}, errors.Join(insertErr, ferr)
	}
	if found {
		return row, nil
	}
	return EvidenceRecord{}, insertErr
}

// insertRow is the row transaction: the idempotency re-check, the chain
// link, the sequence number and the insert.
func (l *EvidenceLedger) insertRow(ctx context.Context, rec EvidenceRecord) (out EvidenceRecord, existed bool, err error) {
	err = l.store.withTx(ctx, func(tx *sql.Tx) error {
		if prior, ok, err := existingByIdempotencyKeyTx(ctx, tx, rec.JobID, rec.IdempotencyKey); err != nil || ok {
			out, existed = prior, ok
			return err
		}
		prev, err := prevHashTx(ctx, tx, rec.JobID)
		if err != nil {
			return err
		}
		seq, err := nextSeqTx(ctx, tx, rec.JobID)
		if err != nil {
			return err
		}
		rec.Seq, rec.CreatedAt, rec.PrevHash = seq, l.clock.Now().UTC(), prev
		if err := insertEvidenceRowTx(ctx, tx, rec); err != nil {
			return err
		}
		out = rec
		return nil
	})
	return out, existed, err
}

// hashedEvidenceTargets maps the sha256 form of every stored row's
// escaped (job, idempotency key) pair to that pair, for keys too long to spell out.
func (l *EvidenceLedger) hashedEvidenceTargets(ctx context.Context) (map[string]effectTarget, error) {
	rows, err := l.store.db.QueryContext(ctx, `SELECT job_id, idempotency_key FROM `+tableEvidence)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan evidence keys")
	}
	defer func() { _ = rows.Close() }()
	out := map[string]effectTarget{}
	for rows.Next() {
		var t effectTarget
		if err := rows.Scan(&t.job, &t.idem); err != nil {
			return nil, cascade.Wrap(cascade.KindUnavailable, err, "jobs: scan evidence keys")
		}
		out[evidencePairHash(t.job, t.idem)] = t
	}
	return out, rows.Err()
}

// RecoverEffects settles every pending evidence intent after a crash: the
// row is present, so the intent is Confirmed, or it is absent, so the
// intent is Failed. Handles for other effect kinds are left alone. Every
// handle is attempted; the errors are joined.
func (l *EvidenceLedger) RecoverEffects(ctx context.Context, pending []audit.EffectHandle) error {
	if l == nil {
		return cascade.New(cascade.KindInvalidInput, "jobs: nil EvidenceLedger")
	}
	var hashed map[string]effectTarget
	var errs []error
	for _, h := range pending {
		if !strings.HasPrefix(h.Key, evidenceEffectPrefix) && !strings.HasPrefix(h.Key, evidenceEffectHashPrefix) {
			continue
		}
		t, ok := parseEvidenceEffectKey(h.Key)
		if !ok {
			errs = append(errs, cascade.Newf(cascade.KindIntegrity, "jobs: malformed evidence effect key %q", h.Key))
			continue
		}
		if t.hash != "" && hashed == nil {
			var err error
			if hashed, err = l.hashedEvidenceTargets(ctx); err != nil {
				return errors.Join(append(errs, err)...)
			}
		}
		if t.hash != "" {
			t = hashed[t.hash]
		}
		if err := l.settleEffect(ctx, h, t); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// settleEffect probes the row t names and writes h's terminal record.
func (l *EvidenceLedger) settleEffect(ctx context.Context, h audit.EffectHandle, t effectTarget) error {
	var row EvidenceRecord
	var found bool
	if t.job != "" {
		var err error
		if row, found, err = l.lookup(ctx, t.job, t.idem); err != nil {
			return err
		}
	}
	if found {
		_, err := l.effects.ConfirmEffect(ctx, h, string(row.Outcome))
		return err
	}
	_, err := l.effects.FailEffect(ctx, h, "crash before the evidence row committed: no row exists")
	return err
}
