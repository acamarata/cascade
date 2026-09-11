// Purpose: L/S-25.T3 unified spawned-agent registry. SpawnRecord is the
//   conductor's view of a spawned subprocess; Registry is the two-intake
//   seam (Register from the conductor, Reconcile from S-24.T1's census
//   poller) merging that view with census-discovered PIDs into one
//   UnifiedSession per process. DefaultRegistry is backed by the same
//   provider.Store the sessions domain (domain.go) persists through, under
//   its own "spawn:"/"unified:" prefixes - domain.go's SessionRecord schema
//   is untouched (no TaskID/ModelClass/Sensitivity columns exist there and
//   domain.go is outside this ticket's files_scope).
// Inputs: SpawnRecord (Register); []census.Snapshot (Reconcile).
// Outputs: UnifiedSession via List/Get, read fresh from store every call so
//   a second, independently-constructed DefaultRegistry over the same
//   store sees the same data (registry_test.go's fresh-handle proof).
// Constraints: PID dedup - SpawnRecord fields win over census defaults;
//   census-only PIDs pass through as attribution-unknown, never dropped;
//   a registered-but-never-confirmed entry evicts after timeout (removal
//   only, never promoted); all timing via the injected runtime.Clock.
// SPORT: fleet/sessions-registry/ADD (P1-E12-W3-S25-T3).

// Package sessions doc: see domain.go for the canonical package comment.
package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/acamarata/cascade/internal/fleet/census"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// registrySpawnPrefix and registryUnifiedPrefix are DefaultRegistry's own
// key prefixes within DefaultNamespace, distinct from domain.go's
// sessionKeyPrefix ("session:") and internal/fleet/journal's "e:"/"h:".
const (
	registrySpawnPrefix   = "spawn:"
	registryUnifiedPrefix = "unified:"
)

// SpawnRecord is the conductor's attribution view of one spawned
// subprocess, as of the moment internal/conductor's SpawnHook fired.
type SpawnRecord struct {
	TaskID      string    `json:"task_id"`
	PID         int       `json:"pid"`
	Binary      string    `json:"binary"`
	Account     string    `json:"account"`
	ModelClass  string    `json:"model_class"`
	Sensitivity string    `json:"sensitivity"`
	SpawnedAt   time.Time `json:"spawned_at"`
}

// UnifiedSession is one merged row of the unified sessions view: a
// SpawnRecord's attribution (when known) merged with census presence.
type UnifiedSession struct {
	SpawnRecord
	// AttributionKnown is false for a census-only session: TaskID,
	// ModelClass and Sensitivity are then all the empty string, and the
	// session is still present in List/Get - never dropped.
	AttributionKnown bool `json:"attribution_known"`
	// Flags carries census.Snapshot's allowlisted flag subset for this
	// PID, when census has observed it this cycle.
	Flags []string `json:"flags,omitempty"`
}

// ErrInvalidSpawnRecord is returned by Register for a SpawnRecord with a
// non-positive PID, an empty TaskID, or an empty Binary. Propagated to the
// caller, never swallowed, for direct Registry.Register callers.
var ErrInvalidSpawnRecord = cascade.New(cascade.KindInvalidInput, "sessions: invalid spawn record")

// Registry is the two-intake seam SpawnHook and the census poller call
// through. Register(SpawnRecord) error and Reconcile([]census.Snapshot)
// error carry no ctx parameter by contract (this ticket's frozen
// signature); DefaultRegistry uses context.Background() internally for
// its store calls.
type Registry interface {
	// Register records one conductor-dispatched subprocess. Rejected
	// input (PID <= 0, empty TaskID or Binary) returns a non-nil error.
	Register(rec SpawnRecord) error
	// Reconcile merges snapshots into the registry: PID matches take
	// SpawnRecord fields over census defaults; census-only PIDs pass
	// through as attribution-unknown; pending entries past the
	// registration timeout are evicted.
	Reconcile(snapshots []census.Snapshot) error
}

// DefaultRegistry is Registry's production implementation, backed by a
// provider.Store (the same store domain.go's Store wraps) and an injected
// runtime.Clock for deterministic pending-eviction timing. The zero value
// is not usable; construct with NewDefaultRegistry.
type DefaultRegistry struct {
	store   provider.Store
	timeout time.Duration
	clock   runtime.Clock
}

var _ Registry = (*DefaultRegistry)(nil)

// NewDefaultRegistry returns a DefaultRegistry persisting through store,
// evicting pending (registered-but-not-yet-census-confirmed) entries after
// timeout has elapsed per clk. A nil store, a nil clk, or a non-positive
// timeout each independently make the returned registry unusable: callers
// construct exactly once at the daemon composition root with real
// collaborators, mirroring domain.go's New precondent for this package.
func NewDefaultRegistry(store provider.Store, timeout time.Duration, clk runtime.Clock) *DefaultRegistry {
	return &DefaultRegistry{store: store, timeout: timeout, clock: clk}
}

func spawnKey(pid int) string   { return fmt.Sprintf("%s%d", registrySpawnPrefix, pid) }
func unifiedKey(pid int) string { return fmt.Sprintf("%s%d", registryUnifiedPrefix, pid) }

// Register validates rec and persists it under its own PID-keyed entry.
// PID <= 0, an empty TaskID, or an empty Binary each return
// ErrInvalidSpawnRecord wrapped with cascade.KindInvalidInput.
func (r *DefaultRegistry) Register(rec SpawnRecord) error {
	if rec.PID <= 0 {
		return cascade.Wrap(cascade.KindInvalidInput, ErrInvalidSpawnRecord, "sessions: Register requires PID > 0")
	}
	if rec.TaskID == "" {
		return cascade.Wrap(cascade.KindInvalidInput, ErrInvalidSpawnRecord, "sessions: Register requires a non-empty TaskID")
	}
	if rec.Binary == "" {
		return cascade.Wrap(cascade.KindInvalidInput, ErrInvalidSpawnRecord, "sessions: Register requires a non-empty Binary")
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "sessions: encoding spawn record")
	}
	ctx := context.Background()
	if err := r.store.Put(ctx, DefaultNamespace, spawnKey(rec.PID), data); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "sessions: registering pid %d", rec.PID)
	}
	return nil
}

// Reconcile merges snapshots (this cycle's census discovery) with every
// registered SpawnRecord, writing one UnifiedSession per snapshot PID, then
// evicts pending SpawnRecord entries that have aged past r.timeout without
// ever being census-confirmed. An empty snapshot slice is a no-op on
// already-unified entries (nothing is cleared) but still runs eviction.
func (r *DefaultRegistry) Reconcile(snapshots []census.Snapshot) error {
	ctx := context.Background()
	seen := make(map[int]bool, len(snapshots))
	for _, snap := range snapshots {
		seen[snap.Pid] = true
		rec, hasSpawn, err := r.getSpawn(ctx, snap.Pid)
		if err != nil {
			return err
		}
		unified := UnifiedSession{Flags: snap.Flags}
		if hasSpawn {
			unified.SpawnRecord = rec
			unified.AttributionKnown = true
			// census-only fields supplement where the spawn record left
			// them empty; SpawnRecord fields otherwise win (PID dedup).
			if unified.Account == "" {
				unified.Account = snap.Account
			}
		} else {
			unified.SpawnRecord = SpawnRecord{PID: snap.Pid, Binary: snap.Binary, Account: snap.Account}
			unified.AttributionKnown = false
		}
		if err := r.putUnified(ctx, unified); err != nil {
			return err
		}
	}
	return r.evictStalePending(ctx, seen)
}

// getSpawn returns the registered SpawnRecord for pid, or (zero, false,
// nil) when none is registered.
func (r *DefaultRegistry) getSpawn(ctx context.Context, pid int) (SpawnRecord, bool, error) {
	data, err := r.store.Get(ctx, DefaultNamespace, spawnKey(pid))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return SpawnRecord{}, false, nil
		}
		return SpawnRecord{}, false, cascade.Wrapf(cascade.KindUnavailable, err, "sessions: reading spawn record for pid %d", pid)
	}
	var rec SpawnRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return SpawnRecord{}, false, cascade.Wrapf(cascade.KindIntegrity, err, "sessions: decoding spawn record for pid %d", pid)
	}
	return rec, true, nil
}

func (r *DefaultRegistry) putUnified(ctx context.Context, u UnifiedSession) error {
	data, err := json.Marshal(u)
	if err != nil {
		return cascade.Wrap(cascade.KindInternal, err, "sessions: encoding unified session")
	}
	if err := r.store.Put(ctx, DefaultNamespace, unifiedKey(u.PID), data); err != nil {
		return cascade.Wrapf(cascade.KindUnavailable, err, "sessions: writing unified session pid %d", u.PID)
	}
	return nil
}

func (r *DefaultRegistry) hasUnified(ctx context.Context, pid int) (bool, error) {
	_, err := r.store.Get(ctx, DefaultNamespace, unifiedKey(pid))
	if err == nil {
		return true, nil
	}
	if cascade.HasKind(err, cascade.KindNotFound) {
		return false, nil
	}
	return false, cascade.Wrapf(cascade.KindUnavailable, err, "sessions: checking unified session pid %d", pid)
}

// evictStalePending deletes every registered SpawnRecord that (a) was not
// confirmed by census this cycle, (b) has never once been unified (a PID
// unified at least once is no longer "pending" - a later gap in one
// census cycle is not this ticket's concern), and (c) has aged past
// r.timeout measured from SpawnedAt via the injected clock. Eviction is
// removal only: an evicted entry is never surfaced as active.
func (r *DefaultRegistry) evictStalePending(ctx context.Context, seenThisCycle map[int]bool) error {
	it, err := r.store.Scan(ctx, DefaultNamespace, registrySpawnPrefix)
	if err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sessions: scanning pending registrations")
	}
	defer func() { _ = it.Close() }()

	now := r.clock.Now()
	var toEvict []int
	for it.Next(ctx) {
		var rec SpawnRecord
		if err := json.Unmarshal(it.Value(), &rec); err != nil {
			continue // corrupt entry: leave for a future cycle rather than delete on unparseable data.
		}
		if seenThisCycle[rec.PID] {
			continue
		}
		if now.Sub(rec.SpawnedAt) <= r.timeout {
			continue
		}
		confirmed, err := r.hasUnified(ctx, rec.PID)
		if err != nil {
			return err
		}
		if confirmed {
			continue
		}
		toEvict = append(toEvict, rec.PID)
	}
	if err := it.Err(); err != nil {
		return cascade.Wrap(cascade.KindUnavailable, err, "sessions: iterating pending registrations")
	}
	for _, pid := range toEvict {
		if err := r.store.Delete(ctx, DefaultNamespace, spawnKey(pid)); err != nil {
			return cascade.Wrapf(cascade.KindUnavailable, err, "sessions: evicting pending pid %d", pid)
		}
	}
	return nil
}

// List returns every unified session known to the registry, sorted by
// PID, read fresh from the store on every call (never a cached copy) -
// the property registry_test.go's fresh-handle test relies on.
func (r *DefaultRegistry) List() ([]UnifiedSession, error) {
	ctx := context.Background()
	it, err := r.store.Scan(ctx, DefaultNamespace, registryUnifiedPrefix)
	if err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "sessions: scanning unified sessions")
	}
	defer func() { _ = it.Close() }()

	var out []UnifiedSession
	for it.Next(ctx) {
		var u UnifiedSession
		if err := json.Unmarshal(it.Value(), &u); err != nil {
			return nil, cascade.Wrap(cascade.KindIntegrity, err, "sessions: decoding unified session")
		}
		out = append(out, u)
	}
	if err := it.Err(); err != nil {
		return nil, cascade.Wrap(cascade.KindUnavailable, err, "sessions: iterating unified sessions")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// Get returns the unified session for pid, or (zero, false, nil) when
// none exists.
func (r *DefaultRegistry) Get(pid int) (UnifiedSession, bool, error) {
	ctx := context.Background()
	data, err := r.store.Get(ctx, DefaultNamespace, unifiedKey(pid))
	if err != nil {
		if cascade.HasKind(err, cascade.KindNotFound) {
			return UnifiedSession{}, false, nil
		}
		return UnifiedSession{}, false, cascade.Wrapf(cascade.KindUnavailable, err, "sessions: reading unified session pid %d", pid)
	}
	var u UnifiedSession
	if err := json.Unmarshal(data, &u); err != nil {
		return UnifiedSession{}, false, cascade.Wrapf(cascade.KindIntegrity, err, "sessions: decoding unified session pid %d", pid)
	}
	return u, true, nil
}
