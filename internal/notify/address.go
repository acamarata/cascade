// Purpose: principal-addressed notifications: the Addressee a verified
//
//	caller resolves to, and the read-back (Service.ReadAddressed /
//	ReadAddressedByID) that alone returns an item addressed to a caller
//	principal or to the owner CLI.
//
// Inputs: the transport-verified principal.Caller in the request context
//
//	(P1-SEC-18); never a scope, session or principal ID taken from the
//	request itself.
//
// Outputs: the caller's own Addressed items, oldest first.
//
// Constraints: fail closed. No caller, or a caller that is neither a
//
//	principal nor the owner CLI, is refused KindPermissionDenied. An empty
//	target never matches, a principal never matches the owner-CLI target,
//	and the owner CLI never matches a principal or session target. An item
//	reaches the Inbox only once the dispatcher has drained it, so a read
//	before that drain returns nothing rather than a partial view.
//
// SPORT: internal.notify.ReadAddressed/ADDED (P1-AGT-04).

package notify

import (
	"context"
	"sort"

	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
)

// OwnerCLITarget is the TargetPrincipal value that addresses an item to the
// owner CLI. No principal can read it, even one whose ID is this literal.
const OwnerCLITarget = "owner-cli"

var (
	// errAddressedTargets reports an Addressed item naming both a session
	// and a principal, or a non-Addressed item naming a principal.
	errAddressedTargets = cascade.New(cascade.KindInvalidInput,
		"notify: an addressed notification names exactly one of target session and target principal")
	// errNoAddressee reports a context with no usable verified caller.
	errNoAddressee = cascade.New(cascade.KindPermissionDenied,
		"notify: addressed read needs a verified caller principal or the owner CLI")
	// errNotAddressee reports an item that exists but is not addressed to
	// the calling principal or owner CLI.
	errNotAddressee = cascade.New(cascade.KindPermissionDenied,
		"notify: notification is not addressed to this caller")
)

// Addressee is who an addressed read is answered for: a principal (its ID
// and the session it is bound to) or the owner CLI. Exactly one is set.
type Addressee struct {
	PrincipalID string
	SessionID   string
	OwnerCLI    bool
}

// AddresseeFrom maps a verified caller to its Addressee. A principal maps to
// its ID and SessionID; the owner CLI maps to OwnerCLI. A caller that is
// neither, a principal with an empty ID, or a principal claiming the
// owner-CLI target as its ID is refused KindPermissionDenied.
func AddresseeFrom(c principal.Caller) (Addressee, error) {
	switch {
	case c.Principal != nil:
		id := c.Principal.ID
		if id == "" || id == OwnerCLITarget {
			return Addressee{}, errNoAddressee
		}
		return Addressee{PrincipalID: id, SessionID: c.Principal.SessionID}, nil
	case c.OwnerCLI:
		return Addressee{OwnerCLI: true}, nil
	default:
		return Addressee{}, errNoAddressee
	}
}

// owns reports whether n is an Addressed item addressed to a. Every
// comparison requires a non-empty target, so a zero field never matches.
func (a Addressee) owns(n Notification) bool {
	if n.Class.Resolve() != ClassAddressed {
		return false
	}
	if a.OwnerCLI {
		return n.TargetSession == "" && n.TargetPrincipal == OwnerCLITarget
	}
	if n.TargetPrincipal != "" {
		return n.TargetSession == "" && n.TargetPrincipal != OwnerCLITarget &&
			a.PrincipalID != "" && n.TargetPrincipal == a.PrincipalID
	}
	return n.TargetSession != "" && n.TargetSession == a.SessionID
}

// addresseeFromContext resolves the verified caller bound to ctx.
func addresseeFromContext(ctx context.Context) (Addressee, error) {
	if err := ctx.Err(); err != nil {
		return Addressee{}, cascade.Wrap(cascade.KindCanceled, err, "notify: addressed read canceled")
	}
	c, ok := principal.CallerFrom(ctx)
	if !ok {
		return Addressee{}, errNoAddressee
	}
	return AddresseeFrom(c)
}

// ReadAddressed returns the caller's own Addressed items carrying
// correlationID, oldest first. The caller comes from principal.CallerFrom;
// no caller is KindPermissionDenied. Another caller's items, expired items
// and items the dispatcher has not drained yet are never returned; none
// matching is an empty result and a nil error.
func (s *Service) ReadAddressed(ctx context.Context, correlationID string) ([]Notification, error) {
	who, err := addresseeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	ib := s.Inbox
	ib.mu.RLock()
	var out []Notification
	for _, rec := range ib.records {
		if rec.expired || rec.n.expired(now) || rec.n.CorrelationID != correlationID {
			continue
		}
		if who.owns(rec.n) {
			out = append(out, rec.n)
		}
	}
	ib.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ReadAddressedByID returns the Addressed item id when it is addressed to
// the caller. No caller, or an item addressed to anyone else, is
// KindPermissionDenied; an id never recorded is KindNotFound, as is the
// caller's own item once expired.
func (s *Service) ReadAddressedByID(ctx context.Context, id string) (Notification, error) {
	who, err := addresseeFromContext(ctx)
	if err != nil {
		return Notification{}, err
	}
	ib := s.Inbox
	ib.mu.RLock()
	defer ib.mu.RUnlock()
	rec, ok := ib.records[id]
	switch {
	case !ok:
		return Notification{}, ErrNotificationUnknown
	case !who.owns(rec.n):
		return Notification{}, errNotAddressee
	case rec.expired || rec.n.expired(s.clock.Now()):
		return Notification{}, ErrNotificationExpired
	}
	return rec.n, nil
}
