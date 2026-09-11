package supervision

// Purpose (this file): ListInScopes and the queue-capacity eviction
// machinery — split out of attention_store.go purely to stay under the
// repo-wide 300-line-per-file cap (R-14.117); see that file's header for
// the split rationale.
//
// SPORT: fleet.supervision.Store.ListInScopes/ADDED (P1-E18-W4-S39-T1).

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ListInScopes returns every item filed under one of scopes, matching f,
// sorted by Priority ASC then CreatedAt ASC then ID ASC (task 2's tie-
// order: ID is the final, always-distinct tiebreak, so List's order is
// fully deterministic for any input, including two items pushed in the
// same clock millisecond). scopes is the CALLER's already-resolved
// candidate set (routing.go's R-21.157 traversal-table resolution) — this
// method queries exactly those scope-index prefixes and nothing wider; it
// never scans the whole namespace and then filters by scope after the
// fact.
func (s *Store) ListInScopes(ctx context.Context, scopes []ScopeRef, f Filter) ([]AttentionItem, error) {
	ids, err := s.collectIDs(ctx, scopes)
	if err != nil {
		return nil, err
	}
	items := make([]AttentionItem, 0, len(ids))
	for id := range ids {
		item, err := s.Get(ctx, id)
		if cascade.HasKind(err, cascade.KindNotFound) {
			continue // race with a concurrent ack/evict; skip, don't fail the list
		}
		if err != nil {
			return nil, err
		}
		if f.matches(item) {
			items = append(items, item)
		}
	}
	sortItems(items)
	return items, nil
}

// collectIDs scans each scope's index prefix and dedups the resulting
// item IDs (a scope appearing twice in scopes must not double-count).
func (s *Store) collectIDs(ctx context.Context, scopes []ScopeRef) (map[string]bool, error) {
	ids := make(map[string]bool)
	for _, ref := range scopes {
		it, err := s.kv.Scan(ctx, DefaultNamespace, scopeIndexPrefix(ref))
		if err != nil {
			return nil, err
		}
		scanErr := scanIDsInto(ctx, it, ids)
		_ = it.Close()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	return ids, nil
}

// sortItems sorts in place by (Priority, CreatedAt, ID) ascending.
func sortItems(items []AttentionItem) {
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return a.ID < b.ID
	})
}

// makeRoomForOne evicts the oldest acknowledged item when the queue is at
// capacity. Returns ErrQueueFull if the queue is full and nothing is
// evictable.
func (s *Store) makeRoomForOne(ctx context.Context) error {
	count, err := s.countAll(ctx)
	if err != nil {
		return err
	}
	if count < s.maxItems {
		return nil
	}
	victim, ok, err := s.oldestAcked(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrQueueFull
	}
	return s.evict(ctx, victim)
}

// countAll counts every canonical item record.
func (s *Store) countAll(ctx context.Context) (int, error) {
	it, err := s.kv.Scan(ctx, DefaultNamespace, attnKeyPrefix)
	if err != nil {
		return 0, err
	}
	defer func() { _ = it.Close() }()
	n := 0
	for it.Next(ctx) {
		n++
	}
	return n, it.Err()
}

// oldestAcked scans every item and returns the acknowledged one with the
// lowest (CreatedAt, ID) — the eviction victim.
func (s *Store) oldestAcked(ctx context.Context) (AttentionItem, bool, error) {
	it, err := s.kv.Scan(ctx, DefaultNamespace, attnKeyPrefix)
	if err != nil {
		return AttentionItem{}, false, err
	}
	defer func() { _ = it.Close() }()
	var victim AttentionItem
	found := false
	for it.Next(ctx) {
		var item AttentionItem
		if err := json.Unmarshal(it.Value(), &item); err != nil {
			continue
		}
		if !item.Acked() {
			continue
		}
		if !found || item.CreatedAt < victim.CreatedAt || (item.CreatedAt == victim.CreatedAt && item.ID < victim.ID) {
			victim, found = item, true
		}
	}
	if err := it.Err(); err != nil {
		return AttentionItem{}, false, err
	}
	return victim, found, nil
}

// evict removes item's canonical record, dedup index, and scope index.
func (s *Store) evict(ctx context.Context, item AttentionItem) error {
	if err := s.kv.Delete(ctx, DefaultNamespace, itemKey(item.ID)); err != nil {
		return err
	}
	if err := s.kv.Delete(ctx, DefaultNamespace, dedupKey(item.Kind, item.SourceRef)); err != nil {
		return err
	}
	return s.kv.Delete(ctx, DefaultNamespace, scopeIndexKey(item.ScopeRef, item.ID))
}
