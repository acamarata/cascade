package supervision

// Purpose (this file): DirectiveStore, the durable queue of fixed
// daemon-authored directives the stall-supervision rungs (stall_rungs.go)
// leave for a stalled session. Enqueue is idempotent per (session, kind,
// stalled-since) while a directive is pending; Drain hands a session's
// pending directives to its consumer in enqueue order, removing each by
// compare-and-swap in the same transaction that returns it, so two
// concurrent Drains never return an entry twice.
//
// Inputs: a provider.Store and a runtime.Clock (stamps the stored row).
// Outputs: Directive values in enqueue order.
// Constraints: Directive.Text is fixed daemon text (stall_rungs.go builds
// it); this file never interpolates session, event or user content. Any
// store error is returned typed and removes nothing. Draining never
// touches the escalation ladder: the ladder state lives in the journal.
//
// SPORT: fleet.supervision.stall-rungs/ADDED (P1-SUP-03).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DirectiveKind names what a queued directive asks the session to do.
type DirectiveKind string

// The two directive kinds the retry and context rungs enqueue.
const (
	DirectiveRetry   DirectiveKind = "retry"
	DirectiveContext DirectiveKind = "context"
)

// Directive is one queued instruction for a stalled session.
type Directive struct {
	SessionID    string
	Kind         DirectiveKind
	Text         string
	StalledSince int64
}

const (
	directiveNamespace  = "supervision.directives"
	directiveMaxRetries = 8
	directiveSeqWidth   = 20
)

// directiveTombstone is the value an entry is swapped to inside the Drain
// transaction before it is deleted; the swap is the compare-and-swap that
// makes a second concurrent Drain of the same entry fail.
var directiveTombstone = []byte("drained")

// directiveRow is the stored shape of one pending directive.
type directiveRow struct {
	Directive
	EnqueuedAt int64 `json:"enqueued_at"`
}

// DirectiveStore is the pending-directive queue. The zero value is not
// usable; construct with NewDirectiveStore.
type DirectiveStore struct {
	kv    provider.Store
	clock runtime.Clock
}

// NewDirectiveStore builds a DirectiveStore over kv, in the
// "supervision.directives" namespace. A nil clock falls back to the system
// clock; a nil kv makes every method return KindInvalidInput.
func NewDirectiveStore(kv provider.Store, clock runtime.Clock) *DirectiveStore {
	if clock == nil {
		clock = runtime.NewSystemClock()
	}
	return &DirectiveStore{kv: kv, clock: clock}
}

func (s *DirectiveStore) ready() error {
	if s == nil || s.kv == nil {
		return cascade.New(cascade.KindInvalidInput, "supervision: directive store has no backing store")
	}
	return nil
}

func entryPrefix(sessionID string) string { return "e:" + url.QueryEscape(sessionID) + ":" }

func entryKey(sessionID string, seq uint64) string {
	return fmt.Sprintf("%s%0*d", entryPrefix(sessionID), directiveSeqWidth, seq)
}

func counterKey(sessionID string) string { return "n:" + url.QueryEscape(sessionID) }

func pendingKey(d Directive) string {
	return "p:" + url.QueryEscape(d.SessionID) + ":" + string(d.Kind) + ":" + strconv.FormatInt(d.StalledSince, 10)
}

func (d Directive) validate() error {
	if d.SessionID == "" || d.Text == "" {
		return cascade.New(cascade.KindInvalidInput, "supervision: directive requires a session id and text")
	}
	if d.Kind != DirectiveRetry && d.Kind != DirectiveContext {
		return cascade.Newf(cascade.KindInvalidInput, "supervision: unknown directive kind %q", string(d.Kind))
	}
	return nil
}

// Enqueue appends d to its session's queue. A directive with the same
// (SessionID, Kind, StalledSince) as one still pending is a no-op.
func (s *DirectiveStore) Enqueue(ctx context.Context, d Directive) error {
	if err := s.ready(); err != nil {
		return err
	}
	if err := d.validate(); err != nil {
		return err
	}
	row, err := json.Marshal(directiveRow{Directive: d, EnqueuedAt: s.clock.Now().UnixMilli()})
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "supervision: encoding directive")
	}
	return retryOnConflict(func() error {
		return s.kv.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
			return enqueueTx(ctx, tx, d, row)
		})
	})
}

// enqueueTx runs inside one transaction: dedupe on the pending marker,
// allocate the next sequence number by CAS on the session counter, then
// write the entry and the marker.
func enqueueTx(ctx context.Context, tx provider.Tx, d Directive, row []byte) error {
	marker := pendingKey(d)
	if _, err := tx.Get(ctx, directiveNamespace, marker); err == nil {
		return nil
	} else if !cascade.HasKind(err, cascade.KindNotFound) {
		return err
	}
	old, next, err := nextSeq(ctx, tx, d.SessionID)
	if err != nil {
		return err
	}
	if err := tx.CompareAndSwap(ctx, directiveNamespace, counterKey(d.SessionID), old, []byte(strconv.FormatUint(next, 10))); err != nil {
		return err
	}
	key := entryKey(d.SessionID, next)
	if err := tx.Put(ctx, directiveNamespace, key, row); err != nil {
		return err
	}
	return tx.Put(ctx, directiveNamespace, marker, []byte(key))
}

// nextSeq reads the session counter and returns its raw old value (nil
// when absent, for the conditional create) and the next sequence number.
func nextSeq(ctx context.Context, tx provider.Tx, sessionID string) (old []byte, next uint64, err error) {
	raw, err := tx.Get(ctx, directiveNamespace, counterKey(sessionID))
	if cascade.HasKind(err, cascade.KindNotFound) {
		return nil, 1, nil
	}
	if err != nil {
		return nil, 0, err
	}
	cur, perr := strconv.ParseUint(string(raw), 10, 64)
	if perr != nil {
		return nil, 0, cascade.Wrap(cascade.KindIntegrity, perr, "supervision: directive counter is not a number")
	}
	return raw, cur + 1, nil
}

// retryOnConflict reruns op while it fails with KindConflict (a concurrent
// writer won the compare-and-swap), up to directiveMaxRetries attempts.
func retryOnConflict(op func() error) error {
	var err error
	for i := 0; i < directiveMaxRetries; i++ {
		if err = op(); !cascade.HasKind(err, cascade.KindConflict) {
			return err
		}
	}
	return cascade.Wrap(cascade.KindConflict, err, "supervision: directive store contention")
}

type pendingEntry struct {
	key string
	raw []byte
	row directiveRow
}

// Drain returns and removes sessionID's pending directives in enqueue
// order. Every entry is removed by compare-and-swap inside the transaction
// that returns it, so concurrent Drains return disjoint sets. A store
// error returns the error and removes nothing.
func (s *DirectiveStore) Drain(ctx context.Context, sessionID string) ([]Directive, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if sessionID == "" {
		return nil, cascade.New(cascade.KindInvalidInput, "supervision: drain requires a session id")
	}
	var out []Directive
	err := retryOnConflict(func() error {
		entries, err := s.pending(ctx, sessionID)
		if err != nil || len(entries) == 0 {
			out = nil
			return err
		}
		if err := s.kv.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
			return removeEntries(ctx, tx, entries)
		}); err != nil {
			return err
		}
		out = make([]Directive, len(entries))
		for i, e := range entries {
			out[i] = e.row.Directive
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// removeEntries swaps each entry to the tombstone by CAS and deletes it
// with its pending marker, inside the caller's transaction.
func removeEntries(ctx context.Context, tx provider.Tx, entries []pendingEntry) error {
	for _, e := range entries {
		if err := tx.CompareAndSwap(ctx, directiveNamespace, e.key, e.raw, directiveTombstone); err != nil {
			return err
		}
		if err := tx.Delete(ctx, directiveNamespace, e.key); err != nil {
			return err
		}
		if err := tx.Delete(ctx, directiveNamespace, pendingKey(e.row.Directive)); err != nil {
			return err
		}
	}
	return nil
}

// pending scans sessionID's entries in key (enqueue) order.
func (s *DirectiveStore) pending(ctx context.Context, sessionID string) ([]pendingEntry, error) {
	it, err := s.kv.Scan(ctx, directiveNamespace, entryPrefix(sessionID))
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()
	var entries []pendingEntry
	for it.Next(ctx) {
		var row directiveRow
		if err := json.Unmarshal(it.Value(), &row); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "supervision: decoding queued directive")
		}
		entries = append(entries, pendingEntry{key: it.Key(), raw: append([]byte(nil), it.Value()...), row: row})
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	return entries, nil
}
