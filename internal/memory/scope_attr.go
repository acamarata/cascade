package memory

// Purpose: caller-principal scoping for the memory.* verbs: a principal
//   reads, writes and forgets only records attributed to its own project
//   scope; the owner view is unchanged.
// Inputs: the transport-verified Caller on ctx (internal/principal); the
//   record's scope_ref frontmatter, written once at memory.remember.
// Outputs: filtered results, or a KindPermissionDenied refusal that is the
//   same for a foreign, unattributed, personal or absent record.
// Constraints: authority comes only from the Caller, never from params; a
//   declared scope is checked against the principal's and refused when it
//   differs. Kinds user and feedback (the personal domain) and records with
//   no project attribution never reach a principal. A principal's result
//   never names an unreadable record, since its scope cannot be checked.
// SPORT: internal.memory.rpc.Handler (scoped reads).

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// projectScopePrefix is the only scope kind a principal is ever minted
// with (internal/principal's Mint refuses every other kind).
const projectScopePrefix = "project:"

// errScopeDenied is the one refusal a principal receives for any record it
// may not touch, so the refusal never tells a foreign record from an absent
// one.
var errScopeDenied = cascade.New(cascade.KindPermissionDenied, "memory: record outside caller scope")

// callerPrincipal returns the principal bound to ctx. ok is false for the
// owner CLI and for an in-process caller (embedded CLI, tests); the RPC
// registry refuses a missing Caller before any memory handler runs.
func callerPrincipal(ctx context.Context) (principal.Principal, bool) {
	c, ok := principal.CallerFrom(ctx)
	if !ok || c.Principal == nil {
		return principal.Principal{}, false
	}
	return *c.Principal, true
}

// personalKind reports whether kind belongs to the personal domain, which
// no principal ever reads or writes.
func personalKind(kind MemoryKind) bool {
	return kind == KindUser || kind == KindFeedback
}

// visibleTo reports whether p may see e: a project-scoped principal, a
// non-personal kind, and a record attributed to exactly p's scope.
func visibleTo(p principal.Principal, e MemoryEntry) bool {
	return strings.HasPrefix(p.Scope, projectScopePrefix) && len(p.Scope) > len(projectScopePrefix) &&
		!personalKind(e.Kind) && e.ScopeRef == p.Scope
}

// checkDeclared refuses a declared scope that is not the principal's own.
// The declared value only ever narrows; it is never authority.
func checkDeclared(p principal.Principal, declared string) error {
	if declared != "" && declared != p.Scope {
		return errScopeDenied
	}
	return nil
}

// principalKinds resolves a principal's type filter, refusing an explicit
// personal kind. An unfiltered scan keeps every kind: visibleTo is the one
// place the personal kinds are dropped from a principal's result.
func principalKinds(typeParam string) ([]MemoryKind, error) {
	kinds, err := kindsFor(typeParam)
	if err != nil {
		return nil, err
	}
	if typeParam != "" && personalKind(kinds[0]) {
		return nil, errScopeDenied
	}
	return kinds, nil
}

// attribute sets the record's scope before a write. A principal creates
// records only in its own scope and may rewrite only a record already
// attributed to that scope; any other existing record is refused unchanged.
// The owner keeps an existing record's attribution, so a scope written at
// creation is never rewritten by a later memory.remember.
func (h *Handler) attribute(ctx context.Context, entry *MemoryEntry) error {
	stored, err := h.store.Read(ctx, entry.Kind, entry.Name)
	found := err == nil
	p, isPrincipal := callerPrincipal(ctx)
	if !isPrincipal {
		if found {
			entry.ScopeRef = stored.ScopeRef
		}
		return nil
	}
	if personalKind(entry.Kind) || !strings.HasPrefix(p.Scope, projectScopePrefix) {
		return errScopeDenied
	}
	switch {
	case found && visibleTo(p, stored):
		entry.ScopeRef = p.Scope
		return nil
	case err != nil && cascade.HasKind(err, cascade.KindNotFound):
		entry.ScopeRef = p.Scope
		return nil
	default:
		return errScopeDenied
	}
}

// scopedForget serves memory.forget: a principal forgets only a record it
// can see; the owner path is Forget unchanged. The check and the forget hold
// the remember lock, so the record checked is the record forgotten.
func (h *Handler) scopedForget(ctx context.Context, params json.RawMessage) (any, error) {
	p, isPrincipal := callerPrincipal(ctx)
	if !isPrincipal {
		return h.Forget(ctx, params)
	}
	var fp ForgetParams
	if err := decodeParams(MethodForget, params, &fp); err != nil {
		return nil, err
	}
	kind, name, err := ParseAddress(fp.ID)
	if err != nil {
		return nil, err
	}
	h.writeMu.Lock()
	defer h.writeMu.Unlock()
	stored, err := h.store.Read(ctx, kind, name)
	if err != nil || !visibleTo(p, stored) {
		return nil, errScopeDenied
	}
	return h.Forget(ctx, params)
}

// scopedQuery returns the kinds and record filter for a recall or list
// call. ok is false for the owner with no declared scope: that call takes
// the unchanged owner path. A principal sees only records visible to it
// and is refused a declared scope that is not its own; an owner's declared
// scope narrows the scan to records attributed to it.
func scopedQuery(ctx context.Context, typeParam, declared string) ([]MemoryKind, func(MemoryEntry) bool, bool, error) {
	p, isPrincipal := callerPrincipal(ctx)
	if !isPrincipal {
		if declared == "" {
			return nil, nil, false, nil
		}
		kinds, err := kindsFor(typeParam)
		return kinds, func(e MemoryEntry) bool { return e.ScopeRef == declared }, true, err
	}
	if err := checkDeclared(p, declared); err != nil {
		return nil, nil, true, err
	}
	kinds, err := principalKinds(typeParam)
	return kinds, func(e MemoryEntry) bool { return visibleTo(p, e) }, true, err
}

// recallFiltered is memory.recall over the records keep admits.
func (h *Handler) recallFiltered(ctx context.Context, rp RecallParams, kinds []MemoryKind, keep func(MemoryEntry) bool) (any, error) {
	result := RecallResult{Units: []MemoryEntry{}}
	if rp.K <= 0 {
		return result, nil
	}
	visible, err := h.keptEntries(ctx, kinds, keep)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(rp.Query)
	for _, entry := range visible {
		if matches(entry, needle) {
			result.Units = append(result.Units, entry)
			if len(result.Units) == rp.K {
				break
			}
		}
	}
	return result, nil
}

// listFiltered is memory.list paging over the records keep admits only,
// so page size and cursors never depend on records the caller cannot see.
func (h *Handler) listFiltered(ctx context.Context, lp ListParams, kinds []MemoryKind, keep func(MemoryEntry) bool) (any, error) {
	limit := lp.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	visible, err := h.keptEntries(ctx, kinds, keep)
	if err != nil {
		return nil, err
	}
	result := ListResult{Units: []MemoryEntry{}}
	for _, entry := range visible {
		if lp.Cursor != "" && Address(entry.Kind, entry.Name) <= lp.Cursor {
			continue
		}
		if len(result.Units) == limit {
			result.NextCursor = Address(result.Units[limit-1].Kind, result.Units[limit-1].Name)
			break
		}
		result.Units = append(result.Units, entry)
	}
	return result, nil
}

// keptEntries reads every live record of kinds in canonical-address order
// and keeps those keep admits. An unreadable record is skipped: its scope
// cannot be proven, so it is treated as foreign and never named.
func (h *Handler) keptEntries(ctx context.Context, kinds []MemoryKind, keep func(MemoryEntry) bool) ([]MemoryEntry, error) {
	addresses, err := h.liveAddresses(ctx, kinds)
	if err != nil {
		return nil, err
	}
	var out []MemoryEntry
	for _, addr := range addresses {
		entry, _, ok := h.readAddress(ctx, addr)
		if ok && keep(entry) {
			out = append(out, entry)
		}
	}
	return out, nil
}
