package audit

// Purpose: the effect recorder (C16, R3 F-11). BeginEffect commits a
//   create-only intent record keyed by a caller idempotency key before an
//   external effect; ConfirmEffect or FailEffect commits the terminal
//   record after it; recovery marks each intent left open unknown-outcome.
//   A begun key can never be begun again, so no effect re-runs blindly.
// Inputs: a *Log (its store, clock, redactor, bus and append lock).
// Outputs: EffectHandles, sealed effect Records, or pkg/cascade errors.
// Constraints: a record and its "eff:<key>" index row commit in ONE store
//   Tx (intent: create-only CAS; terminal: CAS from the exact intent row),
//   so no goroutine or process begins or ends a key twice. No new *Log
//   method; no crash hook (docs/security-posture/effect-records.md).
// SPORT: internal.audit.EffectLog/ADDED (P1-SEC-33).

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// EffectPhase is where one external effect stands in the log.
type EffectPhase string

// The four effect phases. Intent is the only non-terminal one.
const (
	EffectIntent    EffectPhase = "intent"
	EffectConfirmed EffectPhase = "confirmed"
	EffectFailed    EffectPhase = "failed"
	EffectUnknown   EffectPhase = "unknown-outcome"
)

// EffectRequest describes an effect about to run. Key: 1..128 bytes of
// [A-Za-z0-9:._-], caller-derived, deterministic ("approval.grant:<id>").
type EffectRequest struct {
	Kind                           Kind
	Actor, Action, Key, ParamsHash string
	Explain                        json.RawMessage
}

// EffectHandle names one committed intent, as the terminal calls take it.
type EffectHandle struct {
	Key       string
	Kind      Kind
	IntentSeq uint64
	IntentID  string
}

// EffectWriter is the seam every external-effect site injects. A key that
// exists in any phase makes BeginEffect return ErrEffectExists: the effect
// must not run. FailEffect means the effect definitively did not happen.
type EffectWriter interface {
	BeginEffect(ctx context.Context, req EffectRequest) (EffectHandle, error)
	ConfirmEffect(ctx context.Context, h EffectHandle, outcome string) (Record, error)
	FailEffect(ctx context.Context, h EffectHandle, reason string) (Record, error)
}

var _ EffectWriter = (*EffectLog)(nil)

var (
	// ErrEffectExists refuses a second BeginEffect for a key.
	ErrEffectExists = cascade.New(cascade.KindConflict, "audit: an effect with this key was already begun")
	// ErrEffectNotPending refuses a terminal write with no open intent
	// matching the handle (terminal, never begun, or another intent).
	ErrEffectNotPending = cascade.New(cascade.KindConflict, "audit: effect is not pending")
)

// EffectLog writes effect records through a *Log, as a separate type.
type EffectLog struct{ log *Log }

// NewEffectLog wraps l. A nil l is refused.
func NewEffectLog(l *Log) (*EffectLog, error) {
	if l == nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEvent, "NewEffectLog needs a *Log")
	}
	return &EffectLog{log: l}, nil
}

// BeginEffect commits the intent record and its index row together. If
// only the bus notification failed it returns the handle AND the error:
// the intent is logged, so do not run the effect; FailEffect the handle.
func (e *EffectLog) BeginEffect(ctx context.Context, req EffectRequest) (EffectHandle, error) {
	event := Event{Kind: req.Kind, Actor: req.Actor, Action: req.Action, ParamsHash: req.ParamsHash,
		Explain: req.Explain, EffectKey: req.Key, EffectPhase: string(EffectIntent)}
	rec, err := e.commit(ctx, event, nil, effectRow{Phase: EffectIntent}, ErrEffectExists)
	if rec.Seq == 0 {
		return EffectHandle{}, err
	}
	return EffectHandle{Key: req.Key, Kind: rec.Kind, IntentSeq: rec.Seq, IntentID: rec.ID}, err
}

// ConfirmEffect commits the confirmed record for h.
func (e *EffectLog) ConfirmEffect(ctx context.Context, h EffectHandle, outcome string) (Record, error) {
	return e.terminate(ctx, h, EffectConfirmed, outcome)
}

// FailEffect commits the failed record for h.
func (e *EffectLog) FailEffect(ctx context.Context, h EffectHandle, reason string) (Record, error) {
	return e.terminate(ctx, h, EffectFailed, reason)
}

// MarkUnknownOutcome commits the unknown-outcome record for h, for each
// pending intent no site probe settled. The key stays begun for good.
func (e *EffectLog) MarkUnknownOutcome(ctx context.Context, h EffectHandle) (Record, error) {
	return e.terminate(ctx, h, EffectUnknown, "no terminal record existed at recovery; the effect may have run")
}

// EffectState reports key's phase after checking its index row against
// the sealed records it names. found is false for a never-begun key.
func (e *EffectLog) EffectState(ctx context.Context, key string) (EffectPhase, bool, error) {
	row, found, err := e.readRow(ctx, key)
	if err == nil && found {
		_, err = e.checkRow(ctx, key, row)
	}
	if err != nil || !found {
		return "", false, err
	}
	return row.Phase, true, nil
}

// PendingEffects returns a handle for every intent with no terminal
// record, oldest first. It verifies the whole log and refuses unless the
// index rows are exactly what the effect records replay to, so a row lost
// or altered behind the API stops recovery instead of hiding an intent.
func (e *EffectLog) PendingEffects(ctx context.Context) ([]EffectHandle, error) {
	e.log.mu.Lock()
	defer e.log.mu.Unlock()
	var recs []Record
	if _, _, err := e.log.walk(ctx, func(r Record) bool { recs = append(recs, r); return true }); err != nil {
		return nil, err
	}
	want, intents, err := replayEffects(recs)
	if err != nil {
		return nil, err
	}
	rows, err := e.scanRows(ctx)
	if err == nil && !reflect.DeepEqual(rows, want) {
		err = cascade.Wrapf(cascade.KindIntegrity, ErrTampered, "effect index does not match the effect records")
	}
	var out []EffectHandle
	for key, row := range want {
		if in := intents[key]; err == nil && row.Phase == EffectIntent {
			out = append(out, EffectHandle{Key: key, Kind: in.Kind, IntentSeq: in.Seq, IntentID: in.ID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IntentSeq < out[j].IntentSeq })
	return out, err
}

// terminate commits one terminal record, moving the index row from the
// exact intent row h names to phase. Anything else is ErrEffectNotPending.
func (e *EffectLog) terminate(ctx context.Context, h EffectHandle, phase EffectPhase, outcome string) (Record, error) {
	intent, wantRow, err := e.pending(ctx, h)
	if err != nil {
		return Record{}, err
	}
	link := json.RawMessage(`{"intent_seq":` + strconv.FormatUint(intent.Seq, 10) +
		`,"intent_id":` + strconv.Quote(intent.ID) + `}`)
	event := Event{Kind: intent.Kind, Actor: intent.Actor, Action: intent.Action, ParamsHash: intent.ParamsHash,
		Outcome: outcome, Explain: link, EffectKey: h.Key, EffectPhase: string(phase)}
	return e.commit(ctx, event, wantRow, effectRow{Phase: phase, IntentSeq: intent.Seq}, ErrEffectNotPending)
}

// pending checks h against the stored row and intent record and returns
// the intent plus the exact row bytes the terminal CAS must replace.
func (e *EffectLog) pending(ctx context.Context, h EffectHandle) (Record, []byte, error) {
	row, found, err := e.readRow(ctx, h.Key)
	if err != nil {
		return Record{}, nil, err
	}
	if !found || row.Phase != EffectIntent || row.IntentSeq != h.IntentSeq {
		return Record{}, nil, cascade.Wrapf(cascade.KindConflict, ErrEffectNotPending, "key %q", h.Key)
	}
	intent, err := e.checkRow(ctx, h.Key, row)
	if err != nil {
		return Record{}, nil, err
	}
	if intent.ID != h.IntentID || intent.Kind != h.Kind {
		return Record{}, nil, cascade.Wrapf(cascade.KindConflict, ErrEffectNotPending,
			"key %q: the handle does not name the stored intent", h.Key)
	}
	want, err := encodeRow(row)
	return intent, want, err
}

// commit appends like Log.Append and, in the same Tx, CASes the key's row
// from old to row stamped with the new seq (refusal: lost). The row write
// runs first, so a contender loses on the key, not on the sequence number.
func (e *EffectLog) commit(ctx context.Context, event Event, old []byte, row effectRow,
	lost *cascade.Error) (Record, error) {
	l := e.log
	if err := l.redactEvent(ctx, &event); err != nil {
		return Record{}, err
	}
	if err := validateEvent(event); err != nil {
		return Record{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.loadHeadLocked(ctx); err != nil {
		return Record{}, err
	}
	rec, err := l.buildLocked(event)
	if err != nil {
		return Record{}, err
	}
	rowData, data, headData, err := encodeCommit(row.stamped(rec.Seq), rec)
	if err != nil {
		return Record{}, err
	}
	key := event.EffectKey
	txErr := l.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		if err := conflictAs(tx.CompareAndSwap(ctx, namespace, effectKey(key), old, rowData), lost, key); err != nil {
			return err
		}
		if err := tx.CompareAndSwap(ctx, namespace, recordKey(rec.Seq), nil, data); err != nil {
			return cascade.Wrapf(cascade.KindConflict, ErrAlreadyRecorded, "sequence %d: %v", rec.Seq, err)
		}
		if err := tx.Put(ctx, namespace, indexKey(rec.ID), []byte(strconv.FormatUint(rec.Seq, 10))); err != nil {
			return err
		}
		return tx.Put(ctx, namespace, headKey, headData)
	})
	if txErr != nil {
		return Record{}, wrapStore(txErr, "appending effect record")
	}
	l.head = head{Seq: rec.Seq, Hash: rec.Hash}
	return rec, l.notify(ctx, rec)
}

// scanRows reads every index row; the scan closes before any record read.
func (e *EffectLog) scanRows(ctx context.Context) (map[string]effectRow, error) {
	it, err := e.log.store.Scan(ctx, namespace, effectPrefix)
	if err != nil {
		return nil, wrapStore(err, "scanning effect index")
	}
	defer func() { _ = it.Close() }()
	rows := map[string]effectRow{}
	for it.Next(ctx) {
		row, derr := decodeRow(it.Value())
		if derr != nil {
			return nil, derr
		}
		rows[strings.TrimPrefix(it.Key(), effectPrefix)] = row
	}
	return rows, iterErr(it)
}

// readRow validates key and loads its row; found is false when none exists.
func (e *EffectLog) readRow(ctx context.Context, key string) (effectRow, bool, error) {
	if err := validateEffectKey(key); err != nil {
		return effectRow{}, false, err
	}
	data, err := e.log.store.Get(ctx, namespace, effectKey(key))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return effectRow{}, false, nil
		}
		return effectRow{}, false, wrapStore(err, "reading effect index")
	}
	row, err := decodeRow(data)
	return row, err == nil, err
}

// checkRow verifies the records a row names and returns the intent.
func (e *EffectLog) checkRow(ctx context.Context, key string, row effectRow) (Record, error) {
	intent, err := e.loadRecord(ctx, key, EffectIntent, row.IntentSeq)
	if err != nil || row.Phase == EffectIntent {
		return intent, err
	}
	_, err = e.loadRecord(ctx, key, row.Phase, row.TerminalSeq)
	return intent, err
}

// loadRecord reads, hash-verifies and matches the record at seq.
func (e *EffectLog) loadRecord(ctx context.Context, key string, phase EffectPhase, seq uint64) (Record, error) {
	data, err := e.log.store.Get(ctx, namespace, recordKey(seq))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return Record{}, cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
				"effect index for %q names record %d, which is not stored", key, seq)
		}
		return Record{}, wrapStore(err, "reading effect record")
	}
	rec, err := decodeRecord(data)
	if err != nil {
		return Record{}, err
	}
	return rec, checkEffectRecord(rec, key, phase, seq)
}
