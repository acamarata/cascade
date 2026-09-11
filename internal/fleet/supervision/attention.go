package supervision

// Purpose (this file): the AttentionItem domain type, the closed Kind
// vocabulary, and the sentinel errors/filters the rest of this package's
// API is built on.
//
// Inputs: none (types only).
// Outputs: none.
// Constraints: Kind is the CLOSED four-member set the ticket names —
// stall, elevation-refused, policy-ask, error — with no permissive zero
// value (Valid rejects ""), mirroring registry.LaneState's precedent
// (internal/fleet/capacity/snapshot.go). ScopeRef reuses
// internal/context/scope.Ref directly rather than redeclaring a second
// scope-reference shape (DRY; see routing.go's R-21.157 visibility use of
// the same type).
//
// SPORT: fleet.supervision.AttentionItem/ADDED (P1-E18-W4-S39-T1).

import (
	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Kind is the closed attention-item kind vocabulary.
type Kind string

// The four closed Kind members. The zero value ("") is deliberately not
// a member — Valid rejects it, so a zero-value Kind fails closed rather
// than silently matching every filter.
const (
	KindStall            Kind = "stall"
	KindElevationRefused Kind = "elevation-refused"
	KindPolicyAsk        Kind = "policy-ask"
	KindError            Kind = "error"
)

// Valid reports whether k is one of the four closed Kind members.
func (k Kind) Valid() bool {
	switch k {
	case KindStall, KindElevationRefused, KindPolicyAsk, KindError:
		return true
	}
	return false
}

// globalKindSet is the closed subset of Kind a GLOBAL-scope item may
// carry (R-21.157(b)): every member of Kind, but stated as its own set so
// a future widening of Kind does not silently widen what may reach the
// global scope without a deliberate edit here.
var globalKindSet = map[Kind]bool{
	KindStall:            true,
	KindElevationRefused: true,
	KindPolicyAsk:        true,
	KindError:            true,
}

// ScopeRef is the R-16.5 scope a queued item is filed under. It is a type
// alias for scope.Ref (internal/context/scope), not a second declaration
// of the same shape — routing.go's visibility resolution reads scope.Ref
// values from the SAME closed traversal table this type must resolve
// against, so aliasing keeps there being exactly one scope-reference type
// in the tree.
type ScopeRef = scope.Ref

// AttentionItem is one queued item: a job or session requiring human
// intervention.
//
// Art.1: every field below is populated by a real caller (push.go/
// routing.go); there is no placeholder/zero item ever persisted.
type AttentionItem struct {
	// ID is the item's persisted identifier (a UUID, minted by Push —
	// never caller-supplied, so a caller cannot forge idempotency by
	// reusing another item's ID).
	ID string `json:"id"`
	// Kind is one of the four closed members above.
	Kind Kind `json:"kind"`
	// SourceRef names the job/session this item is about (opaque to this
	// package; interpreted by whichever subsystem pushed it).
	SourceRef string `json:"source_ref"`
	// ScopeRef is the R-16.5 scope this item is filed under — the scope
	// visibility resolution (routing.go) and delivery routing (an
	// addressed push, routing.go) both key off.
	ScopeRef ScopeRef `json:"scope_ref"`
	// Priority orders the default list view: smaller values sort first
	// (attention_store.go's List; task-2 tie-order).
	Priority int `json:"priority"`
	// CreatedAt is the push instant (unix millis), read from the
	// injected Clock — never a bare time.Now.
	CreatedAt int64 `json:"created_at"`
	// AckedAt is nil while unacknowledged, and the ack instant (unix
	// millis) once Ack succeeds.
	AckedAt *int64 `json:"acked_at,omitempty"`
}

// Acked reports whether the item has been acknowledged.
func (a AttentionItem) Acked() bool { return a.AckedAt != nil }

// Filter selects a subset of List's results. A nil field matches every
// value for that column.
type Filter struct {
	// KindFilter, when non-nil, restricts to items of exactly this Kind.
	KindFilter *Kind
	// Priority, when non-nil, restricts to items with exactly this
	// priority.
	Priority *int
	// IncludeAcked controls whether already-acknowledged items are
	// included. The default (unack'd) list view sets this false.
	IncludeAcked bool
}

// matches reports whether item satisfies f, independent of scope
// visibility (routing.go applies the scope candidate set separately, at
// the query layer — never as a post-fetch filter on top of this one).
func (f Filter) matches(item AttentionItem) bool {
	if !f.IncludeAcked && item.Acked() {
		return false
	}
	if f.KindFilter != nil && item.Kind != *f.KindFilter {
		return false
	}
	if f.Priority != nil && item.Priority != *f.Priority {
		return false
	}
	return true
}

// Sentinel errors. Each wraps exactly one frozen pkg/cascade.Kind
// (R-14.2): domain-specific sentinels live in their owning package.
var (
	// ErrNotFound is returned when an item id names no record.
	ErrNotFound = cascade.New(cascade.KindNotFound, "supervision: attention item not found")
	// ErrInvalidItem is returned for an item that fails Validate.
	ErrInvalidItem = cascade.New(cascade.KindInvalidInput, "supervision: invalid attention item")
	// ErrQueueFull is returned by Push when the queue is at capacity and
	// no acknowledged item is available to evict (attention_store.go's
	// eviction policy).
	ErrQueueFull = cascade.New(cascade.KindQuotaExhausted, "supervision: attention queue is full")
)

// Validate reports whether item may be pushed: Kind must be one of the
// four closed members, SourceRef must be non-empty, and ScopeRef.Kind
// must be a valid scope.Kind. ID/CreatedAt/AckedAt are Push's own
// responsibility and are not checked here.
func (a AttentionItem) Validate() error {
	if !a.Kind.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidItem, "supervision: %q is not a valid attention Kind", a.Kind)
	}
	if a.SourceRef == "" {
		return cascade.Wrap(cascade.KindInvalidInput, ErrInvalidItem, "supervision: source_ref is required")
	}
	if !a.ScopeRef.Kind.Valid() {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidItem, "supervision: %q is not a valid scope kind", a.ScopeRef.Kind)
	}
	return nil
}
