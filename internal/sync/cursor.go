// Purpose: per-domain sync cursors, persisted via the B/S-02 Store
//   abstraction (namespace = the `config` domain, per storage's own
//   namespace-is-a-domain-string convention — internal/memory's
//   projectionNamespace is the precedent this file follows). A cursor is
//   the monotonic high-water mark of records DURABLY APPLIED OR
//   EXPLICITLY EXCLUDED for one domain+subkind; it never regresses and
//   never silently resyncs (R-21.223).
// Inputs: a provider.Store and an injected Clock (02 §v1.1 — no bare
//   time.Now in domain logic).
// Outputs: CursorStore's methods, or a *cascade.Error.
// Constraints: cursor regress is KindConflict, never a silent overwrite.
//   Advancing past an excluded record MUST go through AdvanceOverExclusion
//   so the exclusion is journaled by record id + policy version in the
//   same call the cursor moves past it — there is no path that advances
//   the cursor over a record without either applying or journaling it.
// SPORT: internal.sync.cursor/ADDED (P1-E17-W4-S38-T1).

package sync

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/storage"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// cursorNamespace is where cursor state lives: the `config` domain, since
// sync bookkeeping is engine-operational state, not synced content of its
// own (R-21.223 forbids a new SQLite domain; this is a namespace/key
// choice within the existing `config` domain, not a new domain).
const cursorNamespace = string(storage.DomainConfig)

func cursorKey(domain storage.DomainID, subkind string) string {
	return "sync/cursor/" + string(domain) + "/" + subkind
}

// Clock abstracts time.Now (Art.7.3 — no bare wall-clock read in domain
// logic). Declared locally, duck-typed against internal/runtime.Clock's
// exact shape, mirroring internal/nodes' identical precedent.
type Clock interface {
	Now() time.Time
}

// Cursor is one domain+subkind's persisted sync position.
type Cursor struct {
	Domain    storage.DomainID `json:"domain"`
	Subkind   string           `json:"subkind"`
	Position  uint64           `json:"position"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// CursorStore persists Cursor state through a provider.Store.
type CursorStore struct {
	store provider.Store
	clock Clock
}

// NewCursorStore builds a CursorStore over store, using clock for every
// UpdatedAt stamp.
func NewCursorStore(store provider.Store, clock Clock) *CursorStore {
	return &CursorStore{store: store, clock: clock}
}

// Get returns the persisted cursor for domain+subkind, or the zero Cursor
// (Position 0) if none has ever been written — a fresh sync starts at
// position zero, which Advance treats as the lowest valid position.
func (c *CursorStore) Get(ctx context.Context, domain storage.DomainID, subkind string) (Cursor, error) {
	raw, err := c.store.Get(ctx, cursorNamespace, cursorKey(domain, subkind))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return Cursor{Domain: domain, Subkind: subkind}, nil
		}
		return Cursor{}, err
	}
	var cur Cursor
	if err := json.Unmarshal(raw, &cur); err != nil {
		return Cursor{}, cascade.Wrap(cascade.KindIntegrity, err, "sync: corrupt persisted cursor")
	}
	return cur, nil
}

// Advance moves domain+subkind's cursor to newPosition. A newPosition at
// or below the current position is a regress: it is refused as
// KindConflict, never silently accepted as a resync-from-scratch signal.
func (c *CursorStore) Advance(ctx context.Context, domain storage.DomainID, subkind string, newPosition uint64) error {
	cur, err := c.Get(ctx, domain, subkind)
	if err != nil {
		return err
	}
	if newPosition <= cur.Position && (cur.Position != 0 || newPosition != 0) {
		return cascade.Newf(cascade.KindConflict, "sync: cursor regress refused for %s/%s: current=%d new=%d", domain, subkind, cur.Position, newPosition)
	}
	cur.Position = newPosition
	cur.UpdatedAt = c.clock.Now()
	raw, err := json.Marshal(cur)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "sync: encode cursor")
	}
	return c.store.Put(ctx, cursorNamespace, cursorKey(domain, subkind), raw)
}

// exclusionKey identifies one journaled exclusion.
func exclusionKey(domain storage.DomainID, subkind, recordID string) string {
	return "sync/exclusion/" + string(domain) + "/" + subkind + "/" + recordID
}

// Exclusion is one journaled pre-serialization exclusion (R-21.223): a
// record the cursor advanced past without ever serializing it.
type Exclusion struct {
	RecordID      string    `json:"record_id"`
	Reason        string    `json:"reason"`
	PolicyVersion int       `json:"policy_version"`
	ExcludedAt    time.Time `json:"excluded_at"`
}

// AdvanceOverExclusion journals excl and advances the cursor to
// newPosition in one call, so the cursor can never move past a record
// without either applying it (plain Advance, called by the transfer path
// after a successful send) or explicitly journaling why it was skipped.
func (c *CursorStore) AdvanceOverExclusion(ctx context.Context, domain storage.DomainID, subkind string, excl Exclusion, newPosition uint64) error {
	excl.ExcludedAt = c.clock.Now()
	raw, err := json.Marshal(excl)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "sync: encode exclusion")
	}
	if err := c.store.Put(ctx, cursorNamespace, exclusionKey(domain, subkind, excl.RecordID), raw); err != nil {
		return err
	}
	return c.Advance(ctx, domain, subkind, newPosition)
}

// Exclusion returns the journaled exclusion for recordID, or
// KindNotFound if it was never excluded.
func (c *CursorStore) Exclusion(ctx context.Context, domain storage.DomainID, subkind, recordID string) (Exclusion, error) {
	raw, err := c.store.Get(ctx, cursorNamespace, exclusionKey(domain, subkind, recordID))
	if err != nil {
		return Exclusion{}, err
	}
	var excl Exclusion
	if err := json.Unmarshal(raw, &excl); err != nil {
		return Exclusion{}, cascade.Wrap(cascade.KindIntegrity, err, "sync: corrupt persisted exclusion")
	}
	return excl, nil
}

// NeedsRescan reports whether excl was journaled under a policy version
// older than currentPolicyVersion: a reclassified record (policy or
// classification changed since it was excluded) must be re-admitted for
// evaluation rather than staying silently excluded forever.
func (c *CursorStore) NeedsRescan(excl Exclusion, currentPolicyVersion int) bool {
	return excl.PolicyVersion < currentPolicyVersion
}

// ListExclusions scans every exclusion journaled for domain+subkind.
// Used by the rescan path to find candidates whose policy version is
// stale. Iteration order is key order (provider.Scan's contract).
func (c *CursorStore) ListExclusions(ctx context.Context, domain storage.DomainID, subkind string) ([]Exclusion, error) {
	prefix := "sync/exclusion/" + string(domain) + "/" + subkind + "/"
	it, err := c.store.Scan(ctx, cursorNamespace, prefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()
	var out []Exclusion
	for it.Next(ctx) {
		var excl Exclusion
		if err := json.Unmarshal(it.Value(), &excl); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "sync: corrupt persisted exclusion during scan")
		}
		out = append(out, excl)
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
