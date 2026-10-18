// Purpose: the typed refusals Fleet.Lease and its companions return. Each
// sentinel wraps exactly one frozen pkg/cascade.Kind; no new kind is
// defined (06-FORGE-SPEC.md §2).
//
// Inputs: a sentinel, the underlying cause (if any) and a detail line.
// Outputs: a leaseError whose chain carries the sentinel and the cause by
// identity, so errors.Is reaches both and cascade.KindOf reads the
// sentinel's kind.
// Constraints: errors.Is on a cascade.Error compares Kind only, so callers
// and tests that need the sentinel itself compare by identity (hasSentinel)
// and by message. A refusal that names removed lanes sorts them, so the
// message is stable.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"fmt"
	"slices"
	"strings"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrNoCapableTier reports that no tier can serve the request right now
// (KindUnavailable; wraps ErrFleetExhausted so either sentinel matches).
var ErrNoCapableTier = cascade.Wrap(cascade.KindUnavailable, ErrFleetExhausted, "capacity: no capable tier can serve this request")

// ErrSensitivityConstraint reports that the request's sensitivity leaves no
// lane or node that may run it (KindPolicyDenied): local-only work stays on
// the controller machine.
var ErrSensitivityConstraint = cascade.New(cascade.KindPolicyDenied, "capacity: the request's sensitivity leaves no lane or node that may run it")

// ErrTier0Reserved reports that tier-0 is the only schedulable tier, it is
// reserved (reserve_tier0) and no jump trigger fired (KindUnavailable).
var ErrTier0Reserved = cascade.New(cascade.KindUnavailable, "capacity: tier-0 is reserved and no jump rule fired")

// ErrDataClassDenied reports that the request's data class forbids every
// remaining lane (KindPolicyDenied).
var ErrDataClassDenied = cascade.New(cascade.KindPolicyDenied, "capacity: the request's data class forbids every remaining lane")

// ErrReservationFailed reports that the reservation could not be made or
// bound (KindUnavailable). The cause stays in the chain.
var ErrReservationFailed = cascade.New(cascade.KindUnavailable, "capacity: the reservation could not be made or bound")

// ErrNoQuotaDomain reports that the selected lane has no quota domain, so
// its capacity is unknown (KindUnavailable). Unknown capacity is never
// unlimited.
var ErrNoQuotaDomain = cascade.New(cascade.KindUnavailable, "capacity: the selected lane has no quota domain")

// ErrLeaseUnknown reports that a lease id is unknown, released, aborted, or
// from before a daemon restart (KindNotFound).
var ErrLeaseUnknown = cascade.New(cascade.KindNotFound, "capacity: lease is unknown, released, aborted or from before a restart")

// leaseError is one refusal: a message, the sentinel it is an instance of and
// the cause beneath it.
type leaseError struct {
	msg      string
	sentinel *cascade.Error
	cause    error
}

func (e *leaseError) Error() string { return e.msg }

// Unwrap exposes the sentinel first, so cascade.KindOf reads its kind, then
// the cause.
func (e *leaseError) Unwrap() []error {
	if e.cause == nil {
		return []error{e.sentinel}
	}
	return []error{e.sentinel, e.cause}
}

// refuse builds a refusal of sentinel with a detail line and an optional
// cause. The message is the sentinel's text, the detail, then the cause.
func refuse(sentinel *cascade.Error, cause error, format string, args ...any) error {
	msg := sentinel.Msg
	if detail := fmt.Sprintf(format, args...); detail != "" {
		msg += ": " + detail
	}
	if cause != nil {
		msg += ": " + cause.Error()
	}
	return &leaseError{msg: msg, sentinel: sentinel, cause: cause}
}

// hasSentinel reports whether sentinel appears in err's chain by identity,
// walking joined errors too. errors.Is is not enough: it compares Kind only.
func hasSentinel(err, sentinel error) bool {
	if err == nil {
		return false
	}
	if err == sentinel {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() []error }:
		return slices.ContainsFunc(u.Unwrap(), func(e error) bool { return hasSentinel(e, sentinel) })
	case interface{ Unwrap() error }:
		return hasSentinel(u.Unwrap(), sentinel)
	}
	return false
}

// removedLane is one lane a filter removed, with why.
type removedLane struct {
	lane   conductor.LaneID
	kind   string
	reason string
}

// describeRemoved renders removed lanes sorted by id, each with its lane type
// and reason, for a refusal's detail line.
func describeRemoved(removed []removedLane) string {
	sorted := slices.Clone(removed)
	slices.SortFunc(sorted, func(a, b removedLane) int { return strings.Compare(string(a.lane), string(b.lane)) })
	parts := make([]string, 0, len(sorted))
	for _, r := range sorted {
		part := fmt.Sprintf("%s (%s)", r.lane, r.kind)
		if r.reason != "" {
			part += ": " + r.reason
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}
