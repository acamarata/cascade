// Purpose: the daemon's operator-presence signal. A PresenceTracker records
//
//	the instant of the last call the operator demonstrably made: a call that
//	reached its handler carrying a verified elevation witness (the proof that
//	a human answered an elevation challenge with the enrolled key) and that
//	then succeeded. It implements notify.PresenceSource for away mode.
//
// Inputs: an injected runtime.Clock and, per dispatched call, the context,
//
//	method and params the RPC registry passes through its middleware chain.
//
// Outputs: LastActivity (zero until the first attested success) and
//
//	PresenceMiddleware, which the composition root mounts inside the
//	elevation middleware (phaseMiddleware order 20).
//
// Constraints: the only evidence of presence is rpc.VerifiedElevationFrom:
//
//	its fields are unexported and only the elevation middleware mints it, so
//	nothing outside internal/rpc can fake presence. The witness must be minted
//	by the issuer the tracker was bound to (Bind, the daemon's own elevation
//	middleware), so a witness from any other gate never counts; a tracker that
//	was never bound counts nothing. It must also be
//	for THIS method and THESE params (a witness for another verb says nothing
//	about this call). Nothing else counts: not a read, a poll, a status call,
//	an agent or hook-driven call, a session or bus event, a failed call, a
//	call the elevation middleware refused. The clock is read AFTER the
//	handler returns a nil error, never before. A nil tracker, a nil clock or
//	a clock that reports the zero time records nothing (the operator stays
//	"away", the side R-14.295 fails toward). The stored instant only moves
//	forward, so concurrent attested calls cannot rewind it.
//
// SPORT: internal/daemon PresenceTracker/PresenceMiddleware (ADD) — P1-HRN-10.

package daemon

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
)

// Compile-time proof that the tracker is the presence seam away mode reads.
var _ notify.PresenceSource = (*PresenceTracker)(nil)

// PresenceTracker holds the instant of the operator's last attested call.
// The zero instant (never observed) is stored as 0 and reported as the zero
// time.Time.
type PresenceTracker struct {
	clock runtime.Clock
	last  atomic.Int64 // UnixNano of the last attested success; 0 = never
	// issuer is the verify-only handle of the elevation middleware whose
	// witnesses count. nil (never bound) matches nothing.
	issuer atomic.Pointer[rpc.WitnessVerifier]
}

// NewPresenceTracker returns a tracker that stamps attested calls with clock.
// A nil clock yields a tracker that never records (fail toward "away").
func NewPresenceTracker(clock runtime.Clock) *PresenceTracker {
	return &PresenceTracker{clock: clock}
}

// Bind sets the elevation middleware whose witnesses count as presence, by its
// verify-only handle (daemon.ElevationStatus.Verifier). Until it is called, and
// when it is called with the zero verifier, the tracker counts nothing.
func (t *PresenceTracker) Bind(by rpc.WitnessVerifier) {
	if t != nil {
		t.issuer.Store(&by)
	}
}

// LastActivity returns when the operator last made an attested call, or the
// zero Time when none has been observed. It is safe on a nil tracker.
func (t *PresenceTracker) LastActivity() time.Time {
	if t == nil {
		return time.Time{}
	}
	n := t.last.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

// record stamps the tracker with the clock's current time. It never moves the
// stored instant backwards, and ignores a missing clock, the zero Time and
// any instant at or before the epoch (none can be told apart from "never
// observed").
func (t *PresenceTracker) record() {
	if t == nil || t.clock == nil {
		return
	}
	at := t.clock.Now()
	if at.IsZero() {
		return // UnixNano of the zero Time is undefined
	}
	n := at.UnixNano()
	// n <= cur also refuses every instant at or before the epoch, because the
	// stored value starts at 0 and never goes below it.
	for {
		cur := t.last.Load()
		if n <= cur || t.last.CompareAndSwap(cur, n) {
			return
		}
	}
}

// PresenceMiddleware returns the middleware that feeds t. It must be
// registered INSIDE the elevation middleware (registered later, so the
// witness is already in the context it receives) and sees the attested
// "_args" params the elevation middleware passes on. After the next handler
// returns a nil error it records the clock iff the context carries a verified
// elevation witness for this method and these params. It never records before
// next returns, and never on an error. A nil tracker yields a pass-through.
func PresenceMiddleware(t *PresenceTracker) rpc.MiddlewareFunc {
	return func(method string, next rpc.HandlerFunc) rpc.HandlerFunc {
		if t == nil {
			return next
		}
		return func(ctx context.Context, params json.RawMessage) (any, error) {
			attested := t.attestedFor(ctx, method, params)
			res, err := next(ctx, params)
			if err != nil {
				return res, err
			}
			if attested {
				t.record()
			}
			return res, nil
		}
	}
}

// attestedFor reports whether ctx carries a verified elevation witness, minted
// by the bound issuer, for exactly this method and these params. The witness is
// read from the context before the handler runs, so what is checked is what the
// handler saw.
func (t *PresenceTracker) attestedFor(ctx context.Context, method string, params json.RawMessage) bool {
	w, ok := rpc.VerifiedElevationFrom(ctx)
	if !ok || w.Method() != method || w.ParamsHash() != rpc.ParamsHash(params) {
		return false
	}
	by := t.issuer.Load()
	return by != nil && w.IssuedBy(*by)
}
