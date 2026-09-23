package memory

// Purpose: the projection's query surface -- Search, SearchIncludingExpired,
//   SearchInScope and SearchInScopeIncludingExpired -- plus searchIndex and
//   the scopeFilter it narrows by. Split out of db_projection.go and
//   db_projection_store.go (P1-E07-W5-S92-T1) purely to keep every file
//   under the 300-line cap: both files were already at 300 and 295 lines
//   (db_projection_store.go's own header names the same reason for its own
//   split from db_projection.go, one ticket earlier), and this ticket's
//   scope-before-k change (R-14.294 item 10, PCI s47t1-memory-scope-after-k)
//   could not land in either without moving something out first. Moved
//   code plus one new parameter, not a new concern.
// Inputs: a ProjectionJob's kv store and clock; a query string; an
//   optional scopeRef to narrow by.
// Outputs: matching IndexedRecord rows, narrowed by scope BEFORE the limit
//   truncates the result set, or a pkg/cascade taxonomy error.
// Constraints: the scope filter, when active, is applied to every matched
//   row before the limit cutoff -- a caller must never lose an in-scope
//   row to more out-of-scope rows ranked ahead of it in raw id order. An
//   empty scopeRef with the filter active matches NOTHING, never
//   everything: no live row can ever have an empty ScopeRef (types.go's
//   MemoryEntry validation refuses one at write time), so this is exactly
//   the fail-closed reading an unresolved caller session must get.
// SPORT: G/memory-db-projection (CHANGED, P1-E07-W5-S92-T1).

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

// scopeFilter narrows searchIndex to one ScopeRef, applied to each matched
// row BEFORE the limit truncates the result set. The zero value (Active
// false) is "no scope narrowing" -- Search and SearchIncludingExpired's
// existing, unscoped behaviour. Active true with an empty Ref matches
// NOTHING: no live row can ever carry an empty ScopeRef, so this is the
// fail-closed reading an unresolved caller session must get, never a
// widened one.
type scopeFilter struct {
	Ref    string
	Active bool
}

// Search returns the projected records matching query, most useful as the
// fast path a recall surface takes instead of re-reading every file. A
// hit is a pointer, not an authority: the row's body is what the file said
// when it was last projected, and the file wins on any disagreement.
// Retired and expired records are excluded, judged against the injected
// clock, so the index never returns a record the store itself would not.
func (j *ProjectionJob) Search(ctx context.Context, query string, limit int) ([]IndexedRecord, error) {
	return searchIndex(ctx, j.kv, query, scopeFilter{}, j.clock.Now().UTC(), limit, false)
}

// SearchIncludingExpired is Search, except an expired row is returned
// rather than dropped (D6/Q6): for a ranking pass that DEMOTES an expired
// row (R-16.7) rather than excluding it, which needs the row in hand. A
// retired (Deleted) row is still never returned either way.
func (j *ProjectionJob) SearchIncludingExpired(ctx context.Context, query string, limit int) ([]IndexedRecord, error) {
	return searchIndex(ctx, j.kv, query, scopeFilter{}, j.clock.Now().UTC(), limit, true)
}

// SearchInScope is Search, narrowed to scopeRef BEFORE limit is applied
// (P1-E07-W5-S92-T1): a caller that instead filters by scope AFTER
// fetching k rows (the way recall.what's memory leg used to) can lose an
// in-scope row ranked past the cap to more out-of-scope rows ranked ahead
// of it -- a recall-quality gap, not a leak, but a real one. This method
// is the fix: the projection itself narrows first, so limit always caps an
// already-scoped result set.
func (j *ProjectionJob) SearchInScope(ctx context.Context, query, scopeRef string, limit int) ([]IndexedRecord, error) {
	return searchIndex(ctx, j.kv, query, scopeFilter{Ref: scopeRef, Active: true}, j.clock.Now().UTC(), limit, false)
}

// SearchInScopeIncludingExpired is SearchInScope, except an expired row is
// returned rather than dropped (mirroring SearchIncludingExpired's own
// D6/Q6 reasoning) -- the method recall.what's memory leg calls, since its
// R-16.7 demotion pass needs an expired row in hand to demote against.
func (j *ProjectionJob) SearchInScopeIncludingExpired(ctx context.Context, query, scopeRef string, limit int) ([]IndexedRecord, error) {
	return searchIndex(ctx, j.kv, query, scopeFilter{Ref: scopeRef, Active: true}, j.clock.Now().UTC(), limit, true)
}

// searchIndex answers a full-text query from the postings.
//
// A record matches when it carries EVERY token of the query (conjunctive),
// which is the reading that cannot return more than the caller asked for.
// An empty query matches nothing rather than everything: a query that
// widened to "all records" when its terms tokenized away would disclose
// records the caller never asked to see. Results are ordered by record id,
// so the same query over the same projection returns the same order on any
// machine. at judges each row's TTL and comes from the caller's clock.
//
// sf narrows to one scope BEFORE limit truncates: the scope check runs
// ahead of the length check in the same loop iteration, so an out-of-scope
// row never occupies a result slot an in-scope row ranked behind it in raw
// id order could otherwise have filled.
//
// includeExpired (P1-E22-W5-S47-T1, D6/Q6) is the bounded escape hatch a
// caller that wants to DEMOTE rather than EXCLUDE an expired row (R-16.7's
// ranking rule) must ask for explicitly: a Deleted (retired/tombstoned)
// row is never returned either way -- expiry and retirement are different
// facts, and only the first is something a caller may choose to see past.
func searchIndex(
	ctx context.Context, kv provider.Store, query string, sf scopeFilter, at time.Time, limit int, includeExpired bool,
) ([]IndexedRecord, error) {
	tokens := tokenize(query)
	if len(tokens) == 0 {
		return nil, nil
	}
	ids, err := matchingIDs(ctx, kv, tokens)
	if err != nil {
		return nil, err
	}
	out := make([]IndexedRecord, 0, len(ids))
	for _, id := range ids {
		row, found, rerr := readRow(ctx, kv, id)
		if rerr != nil {
			return nil, rerr
		}
		if !found || row.Deleted {
			continue
		}
		if !includeExpired && row.Expired(at) {
			continue
		}
		if sf.Active && row.ScopeRef != sf.Ref {
			continue
		}
		out = append(out, row)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}
