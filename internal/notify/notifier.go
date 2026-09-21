// Purpose: Notifier — the R-16.60b DIRECT producer entry point (Deliver, and
//
//	the digest-only DeliverNow), split out of inbox.go so this ticket's
//	gate-bypassing variant does not push that file past Art.10.3's 300-line
//	cap (declared in docs/architecture.md § Away mode, files_scope
//	deviation). inbox.go keeps the consumer/query surface.
//
// Inputs: a caller-populated Notification from CI fan-out, delegation
//
//	results, the away-mode return digest, and future producers.
//
// Outputs: the validated Notification enqueued by Priority — Deliver through
//
//	the away-mode accumulation gate (accumulate.go), DeliverNow past it.
//
// Constraints: no bare time.Now (Timestamp comes from the injected clock);
//
//	a Notification missing the R-16.5 scope fields its resolved Class
//	requires is refused with a typed error, never enqueued half-scoped.
//
// SPORT: internal.notify.Notifier/ADDED (P1-E23-W5-S49-T1).

package notify

import (
	"context"

	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
)

// Notifier is the R-16.60b direct producer entry point: Deliver validates
// the R-16.5 scope fields, assigns Timestamp from the injected clock, and
// enqueues by priority — the same path event-bus-decoded notifications
// take (router.go).
type Notifier struct {
	queues *queueSet
	clock  runtime.Clock
}

// NewNotifier returns a Notifier enqueueing into queues, timestamping with
// clock.
func NewNotifier(queues *queueSet, clock runtime.Clock) *Notifier {
	return &Notifier{queues: queues, clock: clock}
}

// Deliver validates n's required scope fields for its resolved Class,
// assigns Timestamp, and enqueues n by Priority. It returns
// errMissingScopeFields for an Addressed Notification with no
// TargetSession, or a Scoped Notification with neither OriginScope nor
// TargetScope set.
func (nf *Notifier) Deliver(ctx context.Context, n Notification) error {
	n, err := nf.prepare(ctx, n)
	if err != nil {
		return err
	}
	nf.queues.enqueue(n)
	return nil
}

// DeliverNow is Deliver with the away-mode accumulation gate BYPASSED: n
// reaches its priority queue even while away mode is buffering. It exists for
// exactly one caller, DigestCompiler (digest.go): the return digest of an
// episode that has already ended must be dispatched, not buffered into the
// next episode and reduced to a count inside that episode's own digest. Every
// ordinary producer calls Deliver, so a direct Deliver at 02:00 is still
// accumulated exactly like a bus-decoded event.
func (nf *Notifier) DeliverNow(ctx context.Context, n Notification) error {
	n, err := nf.prepare(ctx, n)
	if err != nil {
		return err
	}
	nf.queues.enqueueNow(n)
	return nil
}

// prepare is Deliver/DeliverNow's shared half: the context check, the R-16.5
// scope-field validation for n's resolved Class, and the injected-clock
// Timestamp. It returns the notification to enqueue, or the typed failure.
func (nf *Notifier) prepare(ctx context.Context, n Notification) (Notification, error) {
	if err := ctx.Err(); err != nil {
		return n, cascade.Wrap(cascade.KindCanceled, err, "notify: Deliver canceled")
	}
	switch n.Class.Resolve() {
	case ClassAddressed:
		if n.TargetSession == "" {
			return n, errMissingScopeFields
		}
	case ClassScoped:
		if n.OriginScope == "" && n.TargetScope == "" {
			return n, errMissingScopeFields
		}
	case ClassGlobalCritical, classUnresolvable:
		// GlobalCritical needs no scope fields; classUnresolvable is
		// listed only because Resolve()'s return type still enumerates
		// it — Resolve() never actually produces it here.
	}
	n.Timestamp = nf.clock.Now()
	return n, nil
}
