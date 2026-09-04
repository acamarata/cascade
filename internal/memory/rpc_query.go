package memory

// Purpose: the read half of the memory.* namespace — memory.recall's
//   deterministic file-store scan and memory.list's address-cursor
//   pagination, plus the address enumeration both share.
// Inputs: raw JSON params from an untrusted peer; the T1 file store.
// Outputs: typed results, or a pkg/cascade taxonomy error.
// Constraints: this is a FILE scan, deliberately: the indexed projection
//   is derived state and its query path is a later ticket, so nothing
//   here reads an index. Ordering is canonical-address lexicographic in
//   every path, so two runs over the same tree return the same page.
//   Split from rpc.go for the 300-line file cap, not for a separate
//   concern.
// SPORT: internal.memory.rpc.Handler (ADD, P1-E07-W2-S13-T3).

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// defaultListLimit bounds a memory.list page when the caller names no
// limit. An unbounded default would make the first call on a large store
// the slowest one, and a paging API whose default is "everything" is not
// a paging API.
const defaultListLimit = 100

// RecallParams is memory.recall's input.
type RecallParams struct {
	// Query is the substring to match, case-insensitively, against a
	// record's name, description and body.
	Query string `json:"query"`
	// K caps the number of results. Zero returns no results — a caller
	// asking for zero is answered with zero rather than with a default
	// it did not choose.
	K int `json:"k"`
	// Type narrows the scan to one MemoryKind. Empty scans all four.
	Type string `json:"type,omitempty"`
}

// RecallResult is memory.recall's output.
type RecallResult struct {
	// Units are the matching records in canonical-address order.
	Units []MemoryEntry `json:"units"`
	// Unreadable names every address the scan could not read, with the
	// reason. A scan that quietly dropped a damaged record would return a
	// short answer that looks complete, which is the same defect as a
	// listing that fails whole because of one bad file, only quieter.
	Unreadable []ProjectionFailure `json:"unreadable,omitempty"`
}

// ListParams is memory.list's input.
type ListParams struct {
	// Type narrows the listing to one MemoryKind. Empty lists all four.
	Type string `json:"type,omitempty"`
	// Limit caps the page size. Zero or less uses defaultListLimit.
	Limit int `json:"limit,omitempty"`
	// Cursor is the last address of the previous page; the next page
	// starts strictly after it.
	Cursor string `json:"cursor,omitempty"`
}

// ListResult is memory.list's output.
type ListResult struct {
	// Units are this page's records in canonical-address order.
	Units []MemoryEntry `json:"units"`
	// NextCursor is the last address on this page when more pages
	// remain, and empty when this page was the last one.
	NextCursor string `json:"next_cursor,omitempty"`
	// Unreadable names every address on this page that could not be
	// read. See RecallResult.Unreadable for why it is reported.
	Unreadable []ProjectionFailure `json:"unreadable,omitempty"`
}

// Recall serves memory.recall: a case-insensitive substring scan of the
// files, in canonical-address order, truncated to K.
func (h *Handler) Recall(ctx context.Context, params json.RawMessage) (any, error) {
	var p RecallParams
	if err := decodeParams(MethodRecall, params, &p); err != nil {
		return nil, err
	}
	kinds, err := kindsFor(p.Type)
	if err != nil {
		return nil, err
	}
	result := RecallResult{Units: []MemoryEntry{}}
	if p.K <= 0 {
		return result, nil
	}
	addresses, err := h.liveAddresses(ctx, kinds)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(p.Query)
	for _, addr := range addresses {
		entry, failure, ok := h.readAddress(ctx, addr)
		if !ok {
			result.Unreadable = append(result.Unreadable, failure)
			continue
		}
		if !matches(entry, needle) {
			continue
		}
		result.Units = append(result.Units, entry)
		if len(result.Units) == p.K {
			break
		}
	}
	return result, nil
}

// List serves memory.list: one page of live records in canonical-address
// order, starting strictly after Cursor.
func (h *Handler) List(ctx context.Context, params json.RawMessage) (any, error) {
	var p ListParams
	if err := decodeParams(MethodList, params, &p); err != nil {
		return nil, err
	}
	kinds, err := kindsFor(p.Type)
	if err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	addresses, err := h.liveAddresses(ctx, kinds)
	if err != nil {
		return nil, err
	}
	return h.page(ctx, after(addresses, p.Cursor), limit), nil
}

// page reads up to limit addresses into a ListResult, setting NextCursor
// only when addresses remain beyond the page. An unreadable record still
// consumes its slot in the page: dropping it silently and pulling the next
// address forward would make the page size depend on the store's health.
func (h *Handler) page(ctx context.Context, addresses []string, limit int) ListResult {
	result := ListResult{Units: []MemoryEntry{}}
	taken := 0
	for _, addr := range addresses {
		if taken == limit {
			result.NextCursor = addresses[taken-1]
			break
		}
		taken++
		entry, failure, ok := h.readAddress(ctx, addr)
		if !ok {
			result.Unreadable = append(result.Unreadable, failure)
			continue
		}
		result.Units = append(result.Units, entry)
	}
	return result
}

// after returns the suffix of addresses strictly after cursor. An empty
// cursor returns everything, and a cursor that no longer exists (the
// record was forgotten between pages) still resolves to "everything after
// where it would have been", so paging survives a concurrent delete
// instead of restarting or skipping a page.
func after(addresses []string, cursor string) []string {
	if cursor == "" {
		return addresses
	}
	i := sort.SearchStrings(addresses, cursor)
	for i < len(addresses) && addresses[i] <= cursor {
		i++
	}
	return addresses[i:]
}

// readAddress reads one record, converting any refusal into a reported
// failure rather than an error that fails the whole call.
func (h *Handler) readAddress(ctx context.Context, addr string) (MemoryEntry, ProjectionFailure, bool) {
	kind, name, err := ParseAddress(addr)
	if err != nil {
		return MemoryEntry{}, ProjectionFailure{ID: addr, Reason: err.Error()}, false
	}
	entry, err := h.store.Read(ctx, kind, name)
	if err != nil {
		return MemoryEntry{}, ProjectionFailure{ID: addr, Reason: err.Error()}, false
	}
	return entry, ProjectionFailure{}, true
}

// liveAddresses enumerates every live record address across kinds, sorted
// lexicographically. The sort is over ADDRESSES, not over kinds then
// names: AllKinds' own order is the taxonomy's declaration order, not
// alphabetical, and a caller paging by address needs the two orders to be
// the same one.
func (h *Handler) liveAddresses(ctx context.Context, kinds []MemoryKind) ([]string, error) {
	var out []string
	for _, kind := range kinds {
		names, err := h.store.List(ctx, kind)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			out = append(out, recordID(kind, name))
		}
	}
	sort.Strings(out)
	return out, nil
}

// kindsFor resolves the Type filter to the kinds a scan must walk. An
// unknown kind is refused rather than treated as "no filter": scanning
// everything for a caller who asked for one thing would answer a question
// nobody asked.
func kindsFor(kindParam string) ([]MemoryKind, error) {
	if kindParam == "" {
		return AllKinds(), nil
	}
	kind, err := ParseKind(kindParam)
	if err != nil {
		return nil, cascade.Wrapf(cascade.KindInvalidInput, err,
			"memory: unknown --type %q", kindParam)
	}
	return []MemoryKind{kind}, nil
}

// matches reports whether needle (already lower-cased) occurs in the
// record's name, description or body. An empty needle matches every
// record, which is what makes `recall ""` a whole-store listing rather
// than an error.
func matches(e MemoryEntry, needle string) bool {
	if needle == "" {
		return true
	}
	return strings.Contains(strings.ToLower(e.Name), needle) ||
		strings.Contains(strings.ToLower(e.Description), needle) ||
		strings.Contains(strings.ToLower(e.Body), needle)
}
