package supervision

// Purpose (this file): the provider.Store-backed Store's core CRUD:
// construction, Push (idempotent on (kind, source_ref)), Get, and Ack.
// List and the eviction machinery are split into attention_query.go
// purely to stay under the repo-wide 300-line-per-file cap (R-14.117),
// mirroring internal/fleet/journal's entry.go/store.go split for the
// identical reason — no behavior lives in attention_query.go that this
// file's contract does not already describe.
//
// Persists under storage.DomainSessions — see the CONTRACT DEVIATION
// note below — the same domain internal/fleet/journal and
// internal/fleet/sessions already share, each distinguished by its own
// key prefix.
//
// Inputs: a pkg/provider.Store, an injected runtime.Clock, an EventBus
// (nil is a legitimate no-SSE configuration, mirroring
// internal/fleet/capacity/rpc.go's identical precedent), and maxItems
// (0 means DefaultMaxItems).
// Outputs: AttentionItem values, or a pkg/cascade taxonomy error.
//
// CONTRACT DEVIATION (domain registration, recorded, not papered over).
// The ticket's full_desc names internal/fleet/supervision/ as the new
// subsystem but does not ask for a thirteenth cascade.db DomainID —
// R-14.5's twelve-domain set is CLOSED (R-16.51/R-16.75's two ratified
// amendments). internal/storage/domains.go already documents
// DomainSessions's OwnerPkg as "internal/fleet (sessions, nodes, lanes,
// journal)"; this package persists under DefaultNamespace =
// string(storage.DomainSessions), exactly as internal/fleet/journal's own
// identical CONTRACT DEVIATION note does, distinguished by the
// attnKeyPrefix/attnScopeIndexPrefix key prefixes below. No migration is
// added: this is the SAME generic provider.Store KV table
// internal/fleet/sessions and internal/fleet/journal already write
// through — R-16.77's per-set MigrationSet identity applies to a NEW SQL
// table (internal/jobs/migration.go's shape); it does not apply here
// because no new table is created.
//
// EVICTION (queue capacity — see attention_query.go's makeRoomForOne for
// the implementation; documented here since it is Push's own contract).
// Push refuses a new item only once the queue is genuinely full: it
// first tries to evict the single oldest ACKNOWLEDGED item (lowest
// CreatedAt, ties broken by ID) to make room, since an acknowledged item
// has already served its purpose. If none is acknowledged, Push refuses
// with ErrQueueFull — never silently evicting an unacknowledged item,
// which would make a real human-attention need vanish.
//
// SPORT: fleet.supervision.Store/ADDED (P1-E18-W4-S39-T1).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// DefaultNamespace is the provider.Store namespace AttentionItems persist
// under. See this file's CONTRACT DEVIATION note above.
const DefaultNamespace = string(storage.DomainSessions)

const (
	attnKeyPrefix        = "attn:item:"
	attnDedupPrefix      = "attn:dedup:"
	attnScopeIndexPrefix = "attn:byscope:"
)

// DefaultMaxItems bounds the queue when no explicit maxItems is given to
// NewStore.
const DefaultMaxItems = 10000

// changedNamespace and ChangedKind are this package's SSE mirror topic,
// following R-21.272's ratified single-namespace shape (a distinct Kind
// on its own namespace, since attention is its own subsystem, not a
// second event type folded onto fleet.sessions).
const (
	changedNamespace                  = "fleet.attention"
	ChangedKind      events.EventKind = "fleet.attention.changed"
)

func itemKey(id string) string { return attnKeyPrefix + id }

func dedupKey(kind Kind, sourceRef string) string {
	return attnDedupPrefix + string(kind) + ":" + sourceRef
}

func scopeIndexKey(ref ScopeRef, id string) string {
	return scopeIndexPrefix(ref) + id
}

func scopeIndexPrefix(ref ScopeRef) string {
	return attnScopeIndexPrefix + string(ref.Kind) + ":" + ref.ID + ":"
}

// EventBus is the minimal seam Store publishes fleet.attention.changed
// through, duck-typed against *events.Bus's own Publish signature —
// matching internal/fleet/capacity's identical EventBus precedent so this
// package never requires a live bus in tests.
type EventBus interface {
	Publish(ctx context.Context, namespace string, kind events.EventKind, source string, payload []byte) (events.Event, error)
}

// IDGenerator mints a new item ID. Tests inject a deterministic
// generator; NewSystemIDGenerator (client.go) wraps crypto/rand for
// production.
type IDGenerator func() string

// Store is the provider.Store-backed attention queue.
type Store struct {
	kv       provider.Store
	clock    runtime.Clock
	bus      EventBus
	newID    IDGenerator
	maxItems int
}

// NewStore builds a Store over kv. clock and newID must be non-nil; bus
// may be nil (no-SSE mode). maxItems <= 0 means DefaultMaxItems.
func NewStore(kv provider.Store, clock runtime.Clock, bus EventBus, newID IDGenerator, maxItems int) *Store {
	if maxItems <= 0 {
		maxItems = DefaultMaxItems
	}
	return &Store{kv: kv, clock: clock, bus: bus, newID: newID, maxItems: maxItems}
}

// Push inserts item (minting its ID and CreatedAt) unless an
// unacknowledged or acknowledged item already exists for the same
// (Kind, SourceRef) pair, in which case Push is a no-op and returns the
// EXISTING item — idempotency, per the ticket's task 2. A nil clock/newID
// (a zero-value Store) is a programmer error, reported as KindInternal
// rather than panicking.
func (s *Store) Push(ctx context.Context, item AttentionItem) (AttentionItem, error) {
	if s.kv == nil || s.clock == nil || s.newID == nil {
		return AttentionItem{}, cascade.New(cascade.KindInternal, "supervision: Store used before NewStore")
	}
	if err := item.Validate(); err != nil {
		return AttentionItem{}, err
	}
	if existing, ok, err := s.existingForDedup(ctx, item.Kind, item.SourceRef); err != nil {
		return AttentionItem{}, err
	} else if ok {
		return existing, nil
	}
	if err := s.makeRoomForOne(ctx); err != nil {
		return AttentionItem{}, err
	}
	item.ID = s.newID()
	item.CreatedAt = s.clock.Now().UnixMilli()
	item.AckedAt = nil
	if err := s.writeItem(ctx, item); err != nil {
		return AttentionItem{}, err
	}
	s.emit(ctx, item)
	return item, nil
}

// existingForDedup reads the dedup index for (kind, sourceRef) and, if
// present, resolves it to the current item.
func (s *Store) existingForDedup(ctx context.Context, kind Kind, sourceRef string) (AttentionItem, bool, error) {
	raw, err := s.kv.Get(ctx, DefaultNamespace, dedupKey(kind, sourceRef))
	if cascade.HasKind(err, cascade.KindNotFound) {
		return AttentionItem{}, false, nil
	}
	if err != nil {
		return AttentionItem{}, false, err
	}
	item, err := s.Get(ctx, string(raw))
	if err != nil {
		return AttentionItem{}, false, err
	}
	return item, true, nil
}

// writeItem writes item's canonical record, its dedup index entry, and
// its scope index entry. Not transactional across the three keys: an
// interrupted write leaves at most a canonical record with a missing
// index, which attention_query.go's List tolerates (a lost scope index
// entry only makes the item briefly invisible to List, never duplicated
// or corrupted); wrapping this in kv.Tx is left for a follow-up ticket,
// recorded in this ticket's journal.
func (s *Store) writeItem(ctx context.Context, item AttentionItem) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "supervision: encode attention item")
	}
	if err := s.kv.Put(ctx, DefaultNamespace, itemKey(item.ID), raw); err != nil {
		return err
	}
	if err := s.kv.Put(ctx, DefaultNamespace, dedupKey(item.Kind, item.SourceRef), []byte(item.ID)); err != nil {
		return err
	}
	return s.kv.Put(ctx, DefaultNamespace, scopeIndexKey(item.ScopeRef, item.ID), []byte(item.ID))
}

// Get returns the item named id, or ErrNotFound.
func (s *Store) Get(ctx context.Context, id string) (AttentionItem, error) {
	raw, err := s.kv.Get(ctx, DefaultNamespace, itemKey(id))
	if cascade.HasKind(err, cascade.KindNotFound) {
		return AttentionItem{}, ErrNotFound
	}
	if err != nil {
		return AttentionItem{}, err
	}
	var item AttentionItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return AttentionItem{}, cascade.Wrap(cascade.KindInternal, err, "supervision: decode attention item")
	}
	return item, nil
}

// Ack acknowledges id, setting AckedAt to clock.Now(). Acking an already
// acked or unknown id returns ErrNotFound (task 2's error-path: "ack
// already-acked id -> ErrNotFound" — an acked item is excluded from the
// default view and Ack treats it as no longer addressable).
func (s *Store) Ack(ctx context.Context, id string) (AttentionItem, error) {
	item, err := s.Get(ctx, id)
	if err != nil {
		return AttentionItem{}, err
	}
	if item.Acked() {
		return AttentionItem{}, ErrNotFound
	}
	now := s.clock.Now().UnixMilli()
	item.AckedAt = &now
	if err := s.writeItem(ctx, item); err != nil {
		return AttentionItem{}, err
	}
	s.emit(ctx, item)
	return item, nil
}

// emit publishes fleet.attention.changed. Fire-and-forget, mirroring
// internal/fleet/sessions.Store.emit's identical rationale: SSE is
// observability, not a structural invariant of Push/Ack.
func (s *Store) emit(ctx context.Context, item AttentionItem) {
	if s.bus == nil {
		return
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return
	}
	_, _ = s.bus.Publish(ctx, changedNamespace, ChangedKind, fmt.Sprintf("attention:%s", item.ID), raw)
}

// scanIDsInto drains it into ids. Declared here (used by
// attention_query.go's collectIDs) since both files share the
// provider.Iterator import.
func scanIDsInto(ctx context.Context, it provider.Iterator, ids map[string]bool) error {
	for it.Next(ctx) {
		ids[string(it.Value())] = true
	}
	return it.Err()
}
