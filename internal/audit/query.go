package audit

// Purpose: the read surface, cursor-paginated, oldest-first query;
//   Verify, the whole-log integrity walk; and explain-why for one record
//   id.
// Inputs: a validated Filter, or a record id.
// Outputs: a Page of verified records, an Explanation, or a pkg/cascade
//   taxonomy error.
// Constraints: every record a read passes over is verified and
//   chain-checked before it can shape an answer, so an alteration or a
//   removal made behind this API is reported rather than absorbed.
// SPORT: internal.audit.Log/ADDED (P1-E09-W2-S18-T2).

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// Page is one page of results, oldest first.
type Page struct {
	// Records are the matching records in ascending sequence order.
	Records []Record
	// NextCursor is non-empty when the walk stopped on the page limit
	// with records still unread. Pass it back as Filter.Cursor.
	NextCursor string
}

// Explanation is explain-why for one record: the record itself plus the
// rationale and the policy as it stood when the decision was made, so the
// decision can be reconstructed without depending on today's policy.
type Explanation struct {
	// Record is the verified record.
	Record Record
	// Explain is the rationale body exactly as recorded.
	Explain json.RawMessage
	// PolicySnapshot is the resolved policy as recorded.
	PolicySnapshot json.RawMessage
}

// Query returns the records matching f, oldest first.
//
// Every record the walk passes over is verified and chain-checked, whether
// or not it matches the filter, so a record altered or removed behind this
// API is reported as tampering instead of quietly shaping the answer. A
// walk that reaches the end of the log also checks the tail pointer, which
// is what catches records truncated off the newest end.
func (l *Log) Query(ctx context.Context, f Filter) (Page, error) {
	if err := f.validate(); err != nil {
		return Page{}, err
	}
	after, err := decodeCursor(f.Cursor)
	if err != nil {
		return Page{}, err
	}
	limit := f.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	page := Page{}
	last, complete, err := l.walk(ctx, func(rec Record) bool {
		if rec.Seq > after && f.matches(rec) {
			page.Records = append(page.Records, rec)
		}
		return len(page.Records) < limit
	})
	if err != nil {
		return Page{}, err
	}
	if !complete {
		page.NextCursor = encodeCursor(last)
	}
	return page, nil
}

// Verify walks the entire log and reports the first integrity failure it
// finds, or nil when every record verifies, the chain is unbroken, no
// sequence number is missing, the tail matches the head pointer, and the
// effect index holds exactly the rows the chained effect records replay to.
//
// The walk and the index scan run under the append lock, so this Log's own
// writers cannot move the log mid-check. A writer in another process or on
// another Log value can: when the stored head differs from the tail the
// walk reached, the check runs again, and a mismatch under a head that did
// not move between two runs is tampering.
func (l *Log) Verify(ctx context.Context) error {
	if l.store == nil {
		return cascade.Wrapf(cascade.KindUnavailable, ErrStoreUnavailable, "this log has no store")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var seen head
	for attempt := 1; attempt <= verifyAttempts; attempt++ {
		var recs []Record
		tail, _, err := l.walkRecords(ctx, func(r Record) bool { recs = append(recs, r); return true })
		if err != nil {
			return err
		}
		mismatch := l.checkEffectIndex(ctx, recs)
		stored, herr := l.storedHead(ctx)
		if herr != nil {
			return herr
		}
		if stored.Seq == tail.Seq && stored.Hash == tail.Hash {
			return mismatch
		}
		if attempt > 1 && stored == seen {
			return cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
				"log ends at record %d but the tail pointer names record %d: records are missing", tail.Seq, stored.Seq)
		}
		seen = stored
	}
	return cascade.Wrapf(cascade.KindConflict, ErrAlreadyRecorded,
		"the log kept changing while it was verified; run the check again")
}

// verifyAttempts bounds Verify's re-runs against a moving head.
const verifyAttempts = 3

// checkEffectIndex compares the eff: rows in the store with the rows the
// chained effect records replay to, and names the first key that differs.
func (l *Log) checkEffectIndex(ctx context.Context, recs []Record) error {
	want, _, err := replayEffects(recs)
	if err != nil {
		return err
	}
	got, err := (&EffectLog{log: l}).scanRows(ctx)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(want)+len(got))
	for k := range want {
		keys = append(keys, k)
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		w, inWant := want[k]
		g, inGot := got[k]
		if inWant != inGot || w != g {
			return cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
				"effect %q does not match its records (index row present: %t, replayed: %t)", k, inGot, inWant)
		}
	}
	return nil
}

// storedHead reads the tail pointer; a log with none has the zero head.
func (l *Log) storedHead(ctx context.Context) (head, error) {
	data, err := l.store.Get(ctx, namespace, headKey)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return head{}, nil
		}
		return head{}, wrapStore(err, "reading head pointer")
	}
	var h head
	if uerr := json.Unmarshal(data, &h); uerr != nil {
		return head{}, cascade.Wrapf(cascade.KindIntegrity, ErrTampered, "head pointer is not decodable: %v", uerr)
	}
	return h, nil
}

// walk scans the log in sequence order, verifying as it goes, and calls
// visit for each record. visit returns false to stop early. It reports the
// last sequence number visited and whether the walk reached the end of the
// log.
func (l *Log) walk(ctx context.Context, visit func(Record) bool) (uint64, bool, error) {
	last, complete, err := l.walkRecords(ctx, visit)
	if err != nil || !complete {
		return last.Seq, complete, err
	}
	return last.Seq, true, l.checkHead(ctx, last)
}

// walkRecords is the scan under walk; Verify calls it directly because it
// makes the closing head comparison itself.
func (l *Log) walkRecords(ctx context.Context, visit func(Record) bool) (Record, bool, error) {
	it, err := l.store.Scan(ctx, namespace, recordPrefix)
	if err != nil {
		return Record{}, false, wrapStore(err, "scanning audit records")
	}
	defer func() { _ = it.Close() }()

	var prev Record
	for it.Next(ctx) {
		rec, derr := decodeRecord(it.Value())
		if derr != nil {
			return prev, false, derr
		}
		if cerr := checkChain(prev, rec); cerr != nil {
			return prev, false, cerr
		}
		prev = rec
		if !visit(rec) {
			return rec, false, iterErr(it)
		}
	}
	return prev, true, iterErr(it)
}

// iterErr converts an iteration failure into this package's taxonomy.
func iterErr(it provider.Iterator) error {
	if err := it.Err(); err != nil {
		return wrapStore(err, "scanning audit records")
	}
	return nil
}

// checkChain enforces gapless numbering and hash linkage between two
// consecutive records. A gap means records were removed; a broken link
// means a record was replaced.
func checkChain(prev, rec Record) error {
	if rec.Seq != prev.Seq+1 {
		return cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
			"expected sequence %d, found %d: records are missing", prev.Seq+1, rec.Seq)
	}
	if rec.PrevHash != prev.Hash {
		return cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
			"record %d does not link to record %d", rec.Seq, prev.Seq)
	}
	return nil
}

// checkHead compares the last record a full walk saw against the stored
// tail pointer. They disagree exactly when records have been truncated off
// the end of the log.
func (l *Log) checkHead(ctx context.Context, last Record) error {
	data, err := l.store.Get(ctx, namespace, headKey)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) && last.Seq == 0 {
			return nil
		}
		return wrapStore(err, "reading head pointer")
	}
	var h head
	if uerr := json.Unmarshal(data, &h); uerr != nil {
		return cascade.Wrapf(cascade.KindIntegrity, ErrTampered, "head pointer is not decodable: %v", uerr)
	}
	if h.Seq != last.Seq || h.Hash != last.Hash {
		return cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
			"log ends at record %d but the tail pointer names record %d: records are missing", last.Seq, h.Seq)
	}
	return nil
}

// Explain returns the rationale and the policy snapshot recorded for one
// record id. An id no record carries returns ErrNoSuchRecord, never an
// empty explanation, which a caller could mistake for "the decision had no
// rationale".
func (l *Log) Explain(ctx context.Context, id string) (Explanation, error) {
	if id == "" {
		return Explanation{}, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidEvent, "record id is required")
	}
	raw, err := l.store.Get(ctx, namespace, indexKey(id))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return Explanation{}, cascade.Wrapf(cascade.KindNotFound, ErrNoSuchRecord, "id %q", id)
		}
		return Explanation{}, wrapStore(err, "reading record index")
	}
	seq, perr := strconv.ParseUint(string(raw), 10, 64)
	if perr != nil {
		return Explanation{}, cascade.Wrapf(cascade.KindIntegrity, ErrTampered, "index entry for %q is malformed", id)
	}
	return l.explainSeq(ctx, id, seq)
}

// explainSeq loads and verifies the record the index pointed at.
func (l *Log) explainSeq(ctx context.Context, id string, seq uint64) (Explanation, error) {
	data, err := l.store.Get(ctx, namespace, recordKey(seq))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return Explanation{}, cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
				"index names record %d for id %q but no such record is stored", seq, id)
		}
		return Explanation{}, wrapStore(err, "reading record")
	}
	rec, derr := decodeRecord(data)
	if derr != nil {
		return Explanation{}, derr
	}
	if rec.ID != id {
		return Explanation{}, cascade.Wrapf(cascade.KindIntegrity, ErrTampered,
			"record %d carries id %q, not %q", seq, rec.ID, id)
	}
	return Explanation{Record: rec, Explain: rec.Explain, PolicySnapshot: rec.PolicySnapshot}, nil
}
