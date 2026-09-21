// Purpose: the first-use window ObserveLogger gates on - the persisted
//   first_use_at row, its atomic once-only initialization, and IsObserving,
//   the gate-state report doctor and status surfaces call. The gate's
//   behavior (observe vs apply, the audit event) is in observe_log.go.
// Inputs: the ObserveLogger's own provider.Store handle and Clock; a
//   context.Context per read or write.
// Outputs: the resolved first_use_at instant, or a typed error - the
//   store's own (KindUnavailable for a store that cannot answer), or
//   KindIntegrity for a row that is present but unreadable.
// Constraints:
//
//   ONE ROW, NOT ONE PER TOPIC. The contract's
//   "topic_observe_state{topic_type, first_use_at}" names two FIELDS of a
//   single row (R-16.63 addendum, 18-T0-RULINGS-R16.md): observeStateScope
//   is its constant topic_type, because IsObserving is a property of the
//   engine, not of one topic. The row lives in the retrieval domain under
//   the key topic_observe_state, and observe_state_test.go asserts those two
//   coordinates as literal strings so the constants below cannot be
//   redefined without a red test.
//
//   NEVER OVERWRITTEN, PROVABLY. initFirstUseAt does not write
//   unconditionally: it claims the row inside the store's own transaction
//   with Tx.CompareAndSwap and a nil old value, which provider/store.go
//   defines as a conditional create. Two instances racing on a fresh store
//   therefore cannot both write - the loser gets KindConflict, reads the
//   winner's value and adopts it, so first_use_at is monotonically set once.
//
//   IsObserving TAKES NO ctx (the literal call shape the contract and
//   S-46.T5's status surface name), so its fallback read - the one that
//   happens when nothing has called Observe on this instance yet - uses
//   context.Background(). It DOES return an error: an absent row is the
//   AC's "no Observe call has occurred" and reports (false, nil), while a
//   store that cannot answer and a row that will not decode are real
//   failures and report (false, err) with KindUnavailable / KindIntegrity
//   respectively, never the safe-looking "absent".
// SPORT: internal/conversation/topics observe-logger (ADD) (P1-E21-W5-S45-T4).

package topics

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// observeWindow is the fixed first-week gate (elapsed >= 168h is apply
// mode). observeState* locate the single first_use_at row (header, ONE ROW).
const (
	observeWindow         = 7 * 24 * time.Hour
	observeStateNamespace = string(storage.DomainRetrieval)
	observeStateKey       = "topic_observe_state"
	observeStateScope     = "engine"
)

// observeStateRecord is first_use_at's persisted shape (contract schema).
type observeStateRecord struct {
	TopicType  string `json:"topic_type"`
	FirstUseAt string `json:"first_use_at"` // RFC3339
}

// IsObserving reports whether first_use_at is set and less than 7*24h has
// elapsed. (false, nil) means apply mode: either the window has closed or
// the engine has never run. A non-nil error means the gate state is
// UNKNOWN - the caller must not read the false as "apply mode is fine"
// (header, IsObserving TAKES NO ctx).
func (o *ObserveLogger) IsObserving() (bool, error) {
	cached := o.cachedFirstUseAt()
	if cached == nil {
		t, err := o.readFirstUseAt(context.Background())
		if err != nil {
			if cascade.HasKind(err, cascade.KindNotFound) {
				return false, nil
			}
			return false, err
		}
		o.cacheFirstUseAt(t)
		cached = &t
	}
	return o.clock.Now().Sub(*cached) < observeWindow, nil
}

// resolveFirstUseAt returns the cached value, the stored one, or - when the
// row is absent - the value initFirstUseAt claims for it.
func (o *ObserveLogger) resolveFirstUseAt(ctx context.Context) (time.Time, error) {
	if cached := o.cachedFirstUseAt(); cached != nil {
		return *cached, nil
	}
	t, err := o.readFirstUseAt(ctx)
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return o.initFirstUseAt(ctx)
		}
		return time.Time{}, err
	}
	o.cacheFirstUseAt(t)
	return t, nil
}

// readFirstUseAt reads and decodes the row. A missing row surfaces as the
// store's own KindNotFound error; each caller decides what that means for
// it (IsObserving: "never used"; resolveFirstUseAt: "claim it now").
func (o *ObserveLogger) readFirstUseAt(ctx context.Context) (time.Time, error) {
	raw, err := o.store.Get(ctx, observeStateNamespace, observeStateKey)
	if err != nil {
		return time.Time{}, err
	}
	return decodeObserveState(raw)
}

// decodeObserveState JSON-decodes and RFC3339-parses raw. Both failures are
// KindIntegrity: the row exists and is unreadable, which is not the same
// thing as the row being absent.
func decodeObserveState(raw []byte) (time.Time, error) {
	var rec observeStateRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return time.Time{}, cascade.Wrap(cascade.KindIntegrity, err, "topics: observe log: decoding first_use_at record")
	}
	t, err := time.Parse(time.RFC3339, rec.FirstUseAt)
	if err != nil {
		return time.Time{}, cascade.Wrap(cascade.KindIntegrity, err, "topics: observe log: parsing stored first_use_at")
	}
	return t, nil
}

// initFirstUseAt claims first_use_at for the current clock time with a
// conditional create inside the store's transaction (header, NEVER
// OVERWRITTEN): on KindConflict another instance won the race, so this call
// adopts that instance's stored value instead of moving the window forward.
func (o *ObserveLogger) initFirstUseAt(ctx context.Context) (time.Time, error) {
	now := o.clock.Now().UTC()
	fresh, err := json.Marshal(observeStateRecord{TopicType: observeStateScope, FirstUseAt: now.Format(time.RFC3339)})
	if err != nil {
		return time.Time{}, cascade.Wrap(cascade.KindInternal, err, "topics: observe log: encoding first_use_at record")
	}
	txErr := o.store.Tx(ctx, func(ctx context.Context, tx provider.Tx) error {
		return tx.CompareAndSwap(ctx, observeStateNamespace, observeStateKey, nil, fresh)
	})
	if txErr != nil {
		if cascade.HasKind(txErr, cascade.KindConflict) {
			return o.adoptStoredFirstUseAt(ctx)
		}
		return time.Time{}, txErr
	}
	o.cacheFirstUseAt(now)
	return now, nil
}

// adoptStoredFirstUseAt re-reads the row the conditional create lost to and
// caches the winner's value.
func (o *ObserveLogger) adoptStoredFirstUseAt(ctx context.Context) (time.Time, error) {
	t, err := o.readFirstUseAt(ctx)
	if err != nil {
		return time.Time{}, err
	}
	o.cacheFirstUseAt(t)
	return t, nil
}

// cachedFirstUseAt reads the cache under the lock.
func (o *ObserveLogger) cachedFirstUseAt() *time.Time {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.firstUseAt
}

// cacheFirstUseAt sets the cache only the first time (never replaced).
func (o *ObserveLogger) cacheFirstUseAt(t time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.firstUseAt == nil {
		cached := t
		o.firstUseAt = &cached
	}
}
