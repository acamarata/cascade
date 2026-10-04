// Purpose: the durable records of one conductor.execute fan-out
//   (contract:fanout-producer): the resume cursor (journal seq 1 of
//   FanOutEntity(fanoutID)), the request record, the final marker and the
//   record deletion every finalize ends with. FanOutStore also carries the
//   leg journal appender and leg-result store, so the producer builds one
//   value over the daemon store.
// Inputs: a journal.Store and the daemon's provider.Store.
// Outputs: FanOutStore, which satisfies conductor.JournalAppender and
//   conductor.LegResultStore and adds the cursor/request/final verbs.
// Constraints: the cursor carries ids, counts and a digest only, never
//   request or response content; the prompt lives only in the request
//   record (create-only), which every finalize deletes.
// SPORT: internal.fleet.resume.FanOutStore/ADDED (P1-CORE-19).

package resume

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/internal/fleet/journal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// requestsNamespace holds one request record per fan-out, keyed by the
// bare fan-out id.
const requestsNamespace = "conductor.fanout.requests"

// Final-marker outcomes (the EPIC outcome table).
const (
	OutcomeDelivered = "delivered"
	OutcomeTerminal  = "terminal"
	OutcomeFailed    = "failed"
	OutcomeCancelled = "cancelled"
	OutcomeUnknown   = "unknown_outcome"
	OutcomeExpired   = "expired"
)

// MaxLegStarts is the per-leg start cap: a leg with this many raw starts
// and no ok done is never dispatched again (unknown outcome).
const MaxLegStarts = maxLegStarts

// cursorOpID and finalOpID are the fixed operation ids of a fan-out's
// cursor and final marker: one of each per entity.
const (
	cursorOpID = "fanout-cursor"
	finalOpID  = "fanout-final"
)

// FanOutCursor is the resume cursor's content: who the fan-out is, how
// many legs it has, and the digest and key of its request record.
type FanOutCursor struct {
	FanOutID      string
	TaskID        string
	Legs          int
	RequestDigest string
	RequestKey    string
}

// finalMarker is the KindAck payload that closes a fan-out.
type finalMarker struct {
	T       string `json:"t"` // "fanout_final"
	Outcome string `json:"outcome"`
}

// requestRecord is the stored parent request of one fan-out.
type requestRecord struct {
	Version     int                      `json:"version"`
	FanOutID    string                   `json:"fanout_id"`
	Sensitivity provider.SensitivityTier `json:"sensitivity"`
	Request     provider.ModelRequest    `json:"request"`
}

// FanOutStore is the producer's single durable seam over the daemon store.
type FanOutStore struct {
	*journalAppenderAdapter
}

// NewFanOutStore builds a FanOutStore. A nil journal or store is
// ErrLegStoreUnset.
func NewFanOutStore(js journal.Store, store provider.Store) (*FanOutStore, error) {
	a, err := newLegAdapter(js, store)
	if err != nil {
		return nil, err
	}
	return &FanOutStore{journalAppenderAdapter: a}, nil
}

// RequestDigest binds a parent request: prompt, model, sensitivity, task
// class and fan_out all change it. The reservation id is excluded.
func RequestDigest(req provider.ModelRequest) (string, error) {
	req.ReservationID = ""
	body, err := json.Marshal(req)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInternal, err, "resume: encoding fan-out request for its digest")
	}
	return audit.HashParams(body), nil
}

// WriteCursor appends c as the cursor of FanOutEntity(c.FanOutID) and
// returns the sequence it landed at (1 for a new fan-out).
func (s *FanOutStore) WriteCursor(ctx context.Context, c FanOutCursor) (uint64, error) {
	payload, err := encodeCursor(c)
	if err != nil {
		return 0, err
	}
	e, err := s.journal.Append(ctx, FanOutEntity(c.FanOutID), journal.KindResumeCursor, cursorOpID, payload)
	if err != nil {
		return 0, rewrap(err, "resume: appending fan-out cursor")
	}
	return e.Seq, nil
}

// encodeCursor validates c and renders its KindResumeCursor payload.
func encodeCursor(c FanOutCursor) (json.RawMessage, error) {
	if !validFanOutID(c.FanOutID) || c.Legs < 1 {
		return nil, cascade.Newf(cascade.KindInvalidInput, "resume: invalid fan-out cursor %q/%d", c.FanOutID, c.Legs)
	}
	payload, err := json.Marshal(resumeCursorPayload{T: "cursor", FanOutID: c.FanOutID, TaskID: c.TaskID,
		Legs: c.Legs, RequestDigest: c.RequestDigest, RequestKey: c.RequestKey})
	if err != nil {
		return nil, cascade.Wrap(cascade.KindInternal, err, "resume: encoding fan-out cursor")
	}
	return payload, nil
}

// PutRequest stores req as fanoutID's request record, create-only.
func (s *FanOutStore) PutRequest(ctx context.Context, fanoutID string, req provider.ModelRequest) error {
	data, err := json.Marshal(requestRecord{Version: 1, FanOutID: fanoutID, Sensitivity: req.Sensitivity, Request: req})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "resume: encoding fan-out request record")
	}
	err = s.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, requestsNamespace, fanoutID, nil, data)
	})
	return rewrap(err, "resume: storing fan-out request record (create-only)")
}

// GetRequest loads fanoutID's request record; found is false (with a nil
// error) when it does not exist.
func (s *FanOutStore) GetRequest(ctx context.Context, fanoutID string) (provider.ModelRequest, bool, error) {
	return getRequest(ctx, s.store, fanoutID)
}

// getRequest is GetRequest over a bare store, shared with Scan.
func getRequest(ctx context.Context, store provider.Store, fanoutID string) (provider.ModelRequest, bool, error) {
	data, err := store.Get(ctx, requestsNamespace, fanoutID)
	if cascade.HasKind(err, cascade.KindNotFound) {
		return provider.ModelRequest{}, false, nil
	}
	if err != nil {
		return provider.ModelRequest{}, false, rewrap(err, "resume: reading fan-out request record")
	}
	var rec requestRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.Version != 1 || rec.FanOutID != fanoutID {
		return provider.ModelRequest{}, false, cascade.Newf(cascade.KindIntegrity, "resume: fan-out request record %s is undecodable", fanoutID)
	}
	return rec.Request, true, nil
}

// Finalize closes fanoutID: it appends the final marker with outcome
// (unless one exists) and then deletes the fan-out's records. A marker
// error never skips the deletion; both errors are joined.
func (s *FanOutStore) Finalize(ctx context.Context, fanoutID, outcome string) error {
	return finalize(ctx, s.journal, s.store, fanoutID, outcome)
}

// finalize is Finalize over bare seams, shared with Scan and Sweep.
func finalize(ctx context.Context, js journal.Store, store provider.Store, fanoutID, outcome string) error {
	markErr := appendFinal(ctx, js, fanoutID, outcome)
	return errors.Join(markErr, deleteRecords(ctx, js, store, fanoutID))
}

// appendFinal appends the final marker. It is a no-op when the entity is
// already final-marked, so a marker is never rewritten.
func appendFinal(ctx context.Context, js journal.Store, fanoutID, outcome string) error {
	st, err := loadState(ctx, js, fanoutID)
	if err != nil {
		return err
	}
	if st.Final != "" {
		return nil
	}
	payload, err := json.Marshal(finalMarker{T: "fanout_final", Outcome: outcome})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "resume: encoding fan-out final marker")
	}
	_, err = js.Append(ctx, FanOutEntity(fanoutID), journal.KindAck, finalOpID, payload)
	return rewrap(err, "resume: appending fan-out final marker")
}

// deleteRecords removes fanoutID's leg results and request record.
func deleteRecords(ctx context.Context, js journal.Store, store provider.Store, fanoutID string) error {
	legs := &journalAppenderAdapter{journal: js, store: store}
	legErr := legs.DeleteTask(ctx, fanoutID)
	reqErr := store.Delete(ctx, requestsNamespace, fanoutID)
	if cascade.HasKind(reqErr, cascade.KindNotFound) {
		reqErr = nil
	}
	return errors.Join(legErr, rewrap(reqErr, "resume: deleting fan-out request record"))
}

// itoa avoids importing strconv solely for one call site in this file's
// doc-facing Reason string (kept trivial and allocation-light on
// purpose).
func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
