// Purpose: the core Notification type, the Priority/Class/Visibility
//
//	enums this package routes and gates on, and the clock injection
//	every non-test file under internal/notify uses instead of a bare
//	time.Now (R-14.11).
//
// Inputs: none (types only).
// Outputs: none.
// Constraints: Class and Visibility are fail-closed enums: an unset or
//
//	unrecognized value never behaves as an open default. Priority is an
//	ordered int (Urgent < High < Normal < Low) so a numeric comparison
//	is a well-defined priority comparison. No file in this package
//	calls time.Now directly; every timestamp comes from a
//	runtime.Clock passed in at construction.
//
// SPORT: internal.notify.Notification/ADDED (P1-E23-W5-S49-T1).

package notify

import (
	"log/slog"
	"time"

	"github.com/acamarata/cascade/internal/runtime"
)

// Priority orders a Notification for dispatch. Lower values drain first:
// Urgent before High before Normal before Low.
type Priority int

// The closed Priority ordering.
const (
	PriorityUrgent Priority = iota
	PriorityHigh
	PriorityNormal
	PriorityLow
)

// String returns p's lowercase name, or "unknown" for a value outside the
// closed set (never a panic or an empty string).
func (p Priority) String() string {
	switch p {
	case PriorityUrgent:
		return "urgent"
	case PriorityHigh:
		return "high"
	case PriorityNormal:
		return "normal"
	case PriorityLow:
		return "low"
	default:
		return "unknown"
	}
}

// Valid reports whether p is one of the four closed Priority values.
func (p Priority) Valid() bool {
	switch p {
	case PriorityUrgent, PriorityHigh, PriorityNormal, PriorityLow:
		return true
	}
	return false
}

// Class discriminates how a Notification's recipients are determined.
// Class has NO permissive zero value: an unset, unknown, or unresolvable
// Class is treated as Scoped with an unresolvable scope, so
// ScopeDeliveryPredicate withholds it from every session (fail closed).
type Class string

// The Class vocabulary. classUnknown is never a case a caller sets — it is
// what an empty or unrecognized Class resolves to.
const (
	ClassScoped         Class = "scoped"
	ClassAddressed      Class = "addressed"
	ClassGlobalCritical Class = "global-critical"
	classUnresolvable   Class = ""
)

// Resolve returns c's effective Class for delivery purposes: c itself if
// it is one of the three named values, otherwise ClassScoped paired with
// an unresolvable scope (the fail-closed default — see scope.go).
func (c Class) Resolve() Class {
	switch c {
	case ClassScoped, ClassAddressed, ClassGlobalCritical:
		return c
	case classUnresolvable:
		return ClassScoped
	default:
		return ClassScoped
	}
}

// Visibility gates which sessions may discover a Notification through the
// Inbox query surface (R-16.60). Visibility has NO permissive zero value:
// an unset or unrecognized Visibility resolves to VisibilityPrivate, the
// most restrictive member. Visibility is deliberately a string type (not
// a closed Go iota switch) so a later ruling (R-21.38 adds
// VisibilityExecutive) extends the vocabulary without an exhaustive
// switch statement anywhere in this package needing to change shape.
type Visibility string

// The known Visibility values. Any string outside this set — including
// the empty string — resolves to VisibilityPrivate via Resolve.
const (
	VisibilityPrivate   Visibility = "private"
	VisibilityScoped    Visibility = "scoped"
	VisibilityShared    Visibility = "shared"
	VisibilityExecutive Visibility = "executive"
)

// knownVisibilities is consulted by Resolve. It is a var, not a switch, so
// a future Visibility value is one line here rather than an edit to every
// exhaustive switch in the package (the ticket's own requirement).
var knownVisibilities = map[Visibility]bool{
	VisibilityPrivate:   true,
	VisibilityScoped:    true,
	VisibilityShared:    true,
	VisibilityExecutive: true,
}

// Resolve returns v's effective Visibility: v itself if known, otherwise
// VisibilityPrivate (fail closed).
func (v Visibility) Resolve() Visibility {
	if knownVisibilities[v] {
		return v
	}
	return VisibilityPrivate
}

// Notification is one routable item: an internal event decoded off the
// C/S-04.T3 bus, or a direct Notifier.Deliver call from a producer such as
// AF/S-66.T3 (CI fan-out) or AD/S-62.T2 (delegation results). The router
// never sends this anywhere itself — see router.go's package doc.
type Notification struct {
	// ID uniquely identifies this Notification for Ack/Read/Expire.
	ID string
	// Source names the producer (e.g. an EventKind, or a caller-supplied
	// tag for a direct Deliver call).
	Source string
	// Priority orders this Notification in the per-priority dispatch
	// queues (dispatch.go).
	Priority Priority
	// Payload is the producer-opaque body.
	Payload []byte
	// DeepLink is a cascade:// URI hinting where a consuming surface
	// should route the user. The router never resolves it itself.
	DeepLink string
	// Timestamp is assigned by Notifier.Deliver from the injected clock;
	// never a caller-supplied wall-clock value.
	Timestamp time.Time
	// Sent reports whether the dispatch loop has fanned this Notification
	// out to at least one subscriber.
	Sent bool

	// OriginScope and TargetScope are opaque scope-graph identifiers
	// (internal/context/scope.Ref.ID values) used by ScopeDeliveryPredicate
	// for a Scoped Notification.
	OriginScope string
	TargetScope string
	// TargetSession is required for an Addressed Notification: the exact
	// session ID that alone may receive it.
	TargetSession string
	// TargetTask optionally narrows a Scoped Notification further; it is
	// informational only in this ticket (consumed by future surfaces).
	TargetTask string

	// Class determines the delivery predicate scope.go applies. Use
	// Class.Resolve() before evaluating delivery, never the raw field.
	Class Class
	// Visibility gates Inbox discoverability. Use Visibility.Resolve()
	// before evaluating discoverability, never the raw field.
	Visibility Visibility
	// ExpiresAt, when non-zero and at-or-before the injected clock's Now,
	// makes this Notification ineligible for dispatch and for
	// Inbox.List/Read; it is swept into the expired counts, never
	// silently discarded.
	ExpiresAt time.Time
	// CorrelationID links related Notifications (e.g. one CI run's
	// started/failed pair) for a consuming surface; opaque to the router.
	CorrelationID string
}

// expired reports whether n is past its ExpiresAt as of now. A zero
// ExpiresAt never expires.
func (n Notification) expired(now time.Time) bool {
	return !n.ExpiresAt.IsZero() && !now.Before(n.ExpiresAt)
}

// Service bundles the four collaborators a composition root wires
// together to run the notification router: the Registry subscribers
// register against, the Dispatcher that drains and fans out, the Inbox
// query surface, and the Notifier direct producer entry point. The event
// bus router (router.go) is constructed separately from Service because it
// needs the composition root's own *events.Bus subscription — Service
// carries no bus dependency, keeping this package's core free of that
// coupling.
type Service struct {
	Registry   *Registry
	Dispatcher *Dispatcher
	Inbox      *Inbox
	Notifier   *Notifier
	Router     *NotificationRouter
}

// NewService wires one Registry/Dispatcher/Inbox/Notifier/Router set from
// cfg, clock, and log. Nothing in this package calls NewService itself —
// a daemon composition root does, subscribing Service.Router.Run to a
// live events.Bus and registering subscribers against Service.Registry.
func NewService(cfg Config, clock runtime.Clock, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	queues := newQueueSet(cfg, log)
	registry := NewRegistry()
	inbox := NewInbox()
	return &Service{
		Registry:   registry,
		Dispatcher: NewDispatcher(queues, registry, inbox, clock, log),
		Inbox:      inbox,
		Notifier:   NewNotifier(queues, clock),
		Router:     NewNotificationRouter(queues, clock, log),
	}
}
