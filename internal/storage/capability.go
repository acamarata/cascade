package storage

import (
	"context"
	"sync"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose: the ACCESS-ISOLATION layer over the R-14.5 closed domain set
//
//	(02-TARGET-STRUCTURE.md "cross-domain access requires an explicit
//	capability"): a capability grant model (Grant, Op, CapabilityRegistry)
//	that providers/sqlite's executor and driver consult before letting one
//	domain's Store handle touch another domain's data. B/S-02.T2 already
//	shipped domains.go's GrantRegistry interface — a duck-typed self-grant
//	seam Bootstrap calls once per domain ("domain may always write its own
//	tables"). This file EXTENDS that seam rather than duplicating it:
//	CapabilityRegistry.Register implements domains.GrantRegistry exactly
//	(same method signature, verified by the var _ GrantRegistry assertion
//	below) as a same-domain self-grant, and the SAME registry instance also
//	answers the cross-domain Grant/Revoke/Check calls this ticket adds.
//	Bootstrap and the executor/driver enforcement therefore share one
//	registry object end to end, not two parallel mechanisms.
//
// Inputs: Grant{SrcDomain, DstDomain, Ops, Exp} describes one explicit,
//
//	revocable cross-domain permission. Check(ctx, src, dst, op) is called
//	synchronously, on the hot path, before any cross-domain read or write
//	is allowed to proceed.
//
// Outputs: Check returns nil only when access is actually permitted;
//
//	every denied path returns a typed, non-nil *cascade.Error
//	(ErrDomainForbidden or ErrCapabilityWidening) — never (nil, nil) and
//	never a panic. See capability_test.go's fail-closed escape tests.
//
// Constraints: same-domain access (src == dst) never requires a grant —
//
//	only genuinely cross-domain operations consult the grant map. A
//	domain string outside the closed AllDomains set (including the empty
//	string and R-14.100's reserved "plugin.__host__" namespace, which is
//	a PluginStorage key prefix, never a domain) is fail-closed rejected
//	by Check before the grant map is even consulted — an unrecognized
//	domain can never accidentally satisfy a lookup. Grant expiry (Exp) is
//	checked against an injected Clock, never a bare time.Now (Art.7.3,
//	forbidigo + internal/build/clockgate.go). Art.1: the grant map itself
//	is fully real and evaluated end to end by this package's own tests;
//	what is NOT yet consumed by a real caller is the composition-root
//	wiring that turns a *CapabilityRegistry into providers/sqlite's
//	locally-declared GrantChecker seam (executor.go/scope.go in that
//	package) — that adapter belongs to whichever ticket assembles the
//	daemon's composition root (cmd/ or internal/daemon), not this one,
//	exactly as SocketProbe and Migrator's real implementations are wired
//	in elsewhere. capability_test.go's SQLite integration test builds a
//	small local adapter itself to prove the wiring shape is correct, but
//	that adapter is test-only.
//
// SPORT: internal.storage.capability.CapabilityRegistry/ADDED,
//
//	internal.storage.capability.Grant/ADDED,
//	internal.storage.capability.Op/ADDED (P1-E02-W1-S02-T5).

// Op is a bitmask of capability operations a Grant covers. Values combine
// with bitwise OR (e.g. OpRead|OpWrite for a full read-write grant).
type Op uint8

const (
	// OpRead covers Get and Scan (cross-domain reads).
	OpRead Op = 1 << iota
	// OpWrite covers Put and Delete (cross-domain writes).
	OpWrite
)

// Grant is one explicit, revocable permission for SrcDomain to perform Ops
// against DstDomain's data. A zero-value Exp means the grant never
// expires; a non-zero Exp is checked against CapabilityRegistry's injected
// Clock on every Check call.
type Grant struct {
	SrcDomain DomainID
	DstDomain DomainID
	Ops       Op
	Exp       time.Time
}

// ErrDomainForbidden is the fail-closed sentinel Check returns whenever a
// cross-domain operation has no matching, unexpired grant: absent,
// revoked, or expired all collapse to this one error (06-FORGE-SPEC.md
// §5.20 — the caller cannot distinguish "never granted" from "granted then
// revoked" from the error alone, which is the correct fail-closed
// posture). Wraps cascade.KindPermissionDenied per this ticket's task
// text (task 2): the caller lacks the rights the operation requires, with
// no elevation path offered by this layer.
var ErrDomainForbidden = cascade.New(cascade.KindPermissionDenied, "storage: cross-domain access denied (no matching capability grant)")

// ErrCapabilityWidening is the sentinel Check returns when a request's Ops
// is not a subset of the matching grant's Ops (e.g. a grant covering only
// OpRead cannot satisfy a request for OpRead|OpWrite). Wraps
// cascade.KindPolicyDenied per this ticket's task text (task 2): the
// grant itself was evaluated and found present, but policy (the grant's
// own scope) refuses the wider request — distinct from ErrDomainForbidden,
// where no grant was found at all.
var ErrCapabilityWidening = cascade.New(cascade.KindPolicyDenied, "storage: requested operations exceed the granted capability")

// grantKey is the CapabilityRegistry map key: exactly one Grant may be
// active for a given (src, dst) pair at a time. Grant replaces any prior
// grant for the same pair rather than merging Ops, so a caller narrowing a
// grant (re-Grant with fewer Ops) cannot be defeated by a stale wider
// entry left behind.
type grantKey struct {
	Src DomainID
	Dst DomainID
}

// allDomainSet is AllDomains reindexed as a set, built once at package
// init from domains.go's single source of truth (never duplicated as a
// second literal list here, so a future R-14.5 amendment to AllDomains
// changes this set automatically).
var allDomainSet = buildDomainSet()

func buildDomainSet() map[DomainID]bool {
	m := make(map[DomainID]bool, len(AllDomains))
	for _, meta := range AllDomains {
		m[meta.ID] = true
	}
	return m
}

// validDomain reports whether id is a member of the closed R-14.5 ten-
// domain set. The empty string and R-14.100's reserved
// "plugin.__host__" PluginStorage namespace are both, correctly, not
// members — see this file's package doc.
func validDomain(id DomainID) bool {
	return id != "" && allDomainSet[id]
}

// CapabilityRegistry is the in-process, ephemeral GrantRegistry this
// ticket ships (full_desc "P1 scope"): a goroutine-safe in-memory map,
// live for one daemon session. I/S-17.T1 (Wave 2) extends this seam with
// a durable, policy-engine-backed registry; CapabilityRegistry is the
// integration point that extension replaces or wraps, not a throwaway.
type CapabilityRegistry struct {
	mu     sync.RWMutex
	grants map[grantKey]Grant
	clock  Clock
}

// NewCapabilityRegistry returns an empty CapabilityRegistry. clock is
// required (non-nil) — it is consulted by Check whenever a matching
// grant's Exp is non-zero, and is never read as a bare time.Now.
func NewCapabilityRegistry(clock Clock) *CapabilityRegistry {
	return &CapabilityRegistry{grants: make(map[grantKey]Grant), clock: clock}
}

// var assertion: CapabilityRegistry satisfies domains.go's pre-existing
// GrantRegistry interface exactly (Register(ctx, DomainID) error) — the
// EXTEND-not-duplicate proof this file's package doc promises. Bootstrap
// can be handed a *CapabilityRegistry directly as its BootstrapOpts.GrantRegistry.
var _ GrantRegistry = (*CapabilityRegistry)(nil)

// Register implements storage.GrantRegistry: domain's self-grant, "domain
// may always fully access its own data" — Bootstrap calls this once per
// domain as each anchor table is created. Self-grants are stored in the
// same grant map cross-domain grants use (src == dst), even though Check
// never actually consults the map for a same-domain request (see Check's
// doc) — recording it keeps the map a complete, inspectable record of
// every domain's access rights, self included.
func (r *CapabilityRegistry) Register(ctx context.Context, domain DomainID) error {
	return r.Grant(ctx, Grant{SrcDomain: domain, DstDomain: domain, Ops: OpRead | OpWrite})
}

// Grant records g, replacing any prior grant for the same (SrcDomain,
// DstDomain) pair. Both domains must be members of the closed R-14.5 set
// (validDomain) — a caller attempting to register a grant for an unknown
// or empty domain gets a KindInvalidInput error immediately, rather than
// silently storing a grant that can never legitimately match a Check
// call (Check's own domain validation would reject it anyway; failing at
// registration time surfaces the bug where it was introduced).
func (r *CapabilityRegistry) Grant(_ context.Context, g Grant) error {
	if !validDomain(g.SrcDomain) || !validDomain(g.DstDomain) {
		return cascade.Newf(cascade.KindInvalidInput,
			"storage: capability grant domain not in the closed R-14.5 set (src=%q dst=%q)", g.SrcDomain, g.DstDomain)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.grants[grantKey{Src: g.SrcDomain, Dst: g.DstDomain}] = g
	return nil
}

// Revoke removes any grant for (src, dst). Revoking a grant that does not
// exist is not an error — Revoke is idempotent, mirroring provider.Store's
// own idempotent-Delete convention. A revoked grant's next Check call
// fails closed with ErrDomainForbidden (fail-closed escape test (b)).
func (r *CapabilityRegistry) Revoke(_ context.Context, src, dst DomainID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.grants, grantKey{Src: src, Dst: dst})
	return nil
}

// Check reports whether src may perform op against dst's data right now.
// Same-domain access (src == dst, and src a valid domain) always succeeds
// without a grant-map lookup — a domain never needs a capability grant to
// touch its own data; that is the write executor's ordinary per-domain
// fairness queue, not this ticket's cross-domain layer. Every other path
// is fail-closed: an invalid domain on either side (empty, unrecognized,
// or the reserved plugin.__host__ namespace masquerading as a domain)
// returns ErrDomainForbidden before the map is even consulted; a missing,
// revoked, or expired grant also returns ErrDomainForbidden; a grant whose
// Ops does not cover every bit of the requested op returns
// ErrCapabilityWidening. No path returns (nil, nil) — see
// capability_test.go's fail-closed escape tests for all four contract
// cases.
func (r *CapabilityRegistry) Check(_ context.Context, src, dst DomainID, op Op) error {
	if src == dst && validDomain(src) {
		return nil
	}
	if !validDomain(src) || !validDomain(dst) {
		return ErrDomainForbidden
	}

	r.mu.RLock()
	g, ok := r.grants[grantKey{Src: src, Dst: dst}]
	r.mu.RUnlock()
	if !ok {
		return ErrDomainForbidden
	}
	if !g.Exp.IsZero() && !r.clock.Now().Before(g.Exp) {
		return ErrDomainForbidden
	}
	if op&^g.Ops != 0 {
		return ErrCapabilityWidening
	}
	return nil
}
