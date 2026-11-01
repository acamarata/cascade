// Purpose: the one home of transient retry (P1-GWY-03,
//   contract:transient-error-class): a failure a driver marked transient is
//   retried on the same lane with bounded full-jitter backoff that never
//   sleeps past the call deadline, then spills once to another eligible
//   lane under the same policy. The capability-denied failover (R-21.213)
//   is the same single re-selection, so a call re-selects at most once.
// Inputs: an attemptFn per lane, the injected clock, sleeper and seeded rnd.
// Outputs: the final selection, usage and error, plus the attempt count and
//   whether any transient failure was seen, for the call's audit record
//   and usage row.
// Constraints: never retries a 4xx (429 belongs to the quota spill), a
//   permission, capability or policy denial, a cancellation, a deadline, or
//   a credential or helper failure; never retries a stream once output
//   reached the caller; never sleeps with time.Sleep. Lane state and
//   provider health are not touched here (WID-11, GWY-09).
// SPORT: conductor.transient-retry/ADD (P1-GWY-03).

package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/acamarata/cascade/internal/audit"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// RetryPolicy bounds transient retry on one lane.
type RetryPolicy struct {
	// MaxAttempts is the attempt ceiling per lane (3 by default).
	MaxAttempts int
	// Base is the first backoff ceiling (500ms by default).
	Base time.Duration
	// Cap is the largest backoff and the largest honoured Retry-After
	// (8s by default).
	Cap time.Duration
}

// DefaultRetryPolicy is 3 attempts per lane, a 500ms base and an 8s cap.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, Base: 500 * time.Millisecond, Cap: 8 * time.Second}
}

// Sleeper pauses for d. It returns ctx.Err() when ctx ends first.
type Sleeper func(ctx context.Context, d time.Duration) error

// LaneEligible reports whether lane may take another attempt. A nil
// LaneEligible means always eligible; P1-GWY-09 wires provider health.
type LaneEligible func(ctx context.Context, lane LaneID) bool

// retrySeam is one installed policy. rnd is not safe for concurrent use,
// so mu guards every draw.
type retrySeam struct {
	policy   RetryPolicy
	clock    Clock
	sleep    Sleeper
	eligible LaneEligible
	mu       sync.Mutex
	rnd      *rand.Rand
}

func newRetrySeam(p RetryPolicy, clock Clock, sleep Sleeper, rnd *rand.Rand, eligible LaneEligible) *retrySeam {
	if p.MaxAttempts < 1 || clock == nil || sleep == nil || rnd == nil {
		return nil
	}
	p.Base, p.Cap = max(p.Base, 0), max(p.Cap, 0)
	return &retrySeam{policy: p, clock: clock, sleep: sleep, eligible: eligible, rnd: rnd}
}

// SetRetry installs transient retry. The daemon composition applies it
// (through RetryOption); with none installed, or with a zero MaxAttempts
// or a nil clock, sleeper or rnd, every call makes one attempt per lane.
func (e *Executor) SetRetry(p RetryPolicy, clock Clock, sleep Sleeper, rnd *rand.Rand, eligible LaneEligible) {
	e.retry.Store(newRetrySeam(p, clock, sleep, rnd, eligible))
}

// routerRetry carries a RetryOption's policy from the router the
// composition builds to the Executor built over it.
var routerRetry sync.Map // *DefaultRouter -> *retrySeam

// RetryOption is SetRetry for a composition that hands its router options
// to the code that builds the Executor: the Executor over this router
// applies the policy before its first dispatch. SetRetry on the Executor
// itself takes precedence.
func RetryOption(p RetryPolicy, clock Clock, sleep Sleeper, rnd *rand.Rand, eligible LaneEligible) RouterOption {
	return func(r *DefaultRouter) {
		if s := newRetrySeam(p, clock, sleep, rnd, eligible); s != nil {
			routerRetry.Store(r, s)
		}
	}
}

// retrySetup returns the installed policy, adopting the router's on first use.
func (e *Executor) retrySetup() *retrySeam {
	if s := e.retry.Load(); s != nil {
		return s
	}
	if r, ok := e.router.(*DefaultRouter); ok {
		if v, ok := routerRetry.Load(r); ok {
			e.retry.CompareAndSwap(nil, v.(*retrySeam))
		}
	}
	return e.retry.Load()
}

// delay draws the pause before attempt n+1: uniform over
// [0, min(Cap, Base*2^(n-1))], raised to RetryAfter.
func (s *retrySeam) delay(n int, info provider.TransientInfo) time.Duration {
	ceil, b := s.policy.Cap, s.policy.Base
	for i := 1; i < n && b < ceil; i++ {
		b *= 2
	}
	ceil = min(ceil, b)
	s.mu.Lock()
	d := time.Duration(s.rnd.Int64N(int64(ceil) + 1))
	s.mu.Unlock()
	return max(d, info.RetryAfter)
}

// next reports the pause before attempt n+1 on lane, or false to stop the
// lane: the ceiling is reached, ctx is done, Retry-After exceeds Cap, the
// lane is no longer eligible, or the pause would end past the deadline.
func (s *retrySeam) next(ctx context.Context, n int, info provider.TransientInfo, lane LaneID) (time.Duration, bool) {
	if n >= s.policy.MaxAttempts || ctx.Err() != nil || info.RetryAfter > s.policy.Cap || !s.laneEligible(ctx, lane) {
		return 0, false
	}
	d := s.delay(n, info)
	if dl, ok := ctx.Deadline(); ok && dl.Sub(s.clock.Now()) < d {
		return 0, false
	}
	return d, true
}

func (s *retrySeam) laneEligible(ctx context.Context, lane LaneID) bool {
	return s.eligible == nil || s.eligible(ctx, lane)
}

// retryable reports a failure the marker calls transient and the closed
// refusal set does not exclude.
func retryable(ctx context.Context, err error) (provider.TransientInfo, bool) {
	info, ok := provider.TransientOf(err)
	if !ok || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return info, false
	}
	if info.Status >= 400 && info.Status < 500 {
		return info, false
	}
	if k, has := cascade.KindOf(err); has && k != cascade.KindUnavailable && k != cascade.KindInternal {
		return info, false
	}
	return info, true
}

// retryStats is what the call's audit record and usage row carry.
type retryStats struct {
	Attempts  int
	Transient bool
}

// abandonFn sees each superseded attempt, numbered, so its usage is kept.
type abandonFn func(sel provider.Selection, attempt int, u provider.Usage, err error)

// withRetry runs run on sel under the retry policy, then re-selects once
// with sel excluded after a capability denial or an exhausted transient
// failure, and runs the same policy there. Nothing re-selects once output
// reached the caller.
func (e *Executor) withRetry(ctx context.Context, req provider.ModelRequest, sel provider.Selection, run attemptFn, abandoned abandonFn) (provider.Selection, retryStats, provider.Usage, error) {
	seam := e.retrySetup()
	var st retryStats
	u, delivered, err := laneAttempts(ctx, seam, sel, run, abandoned, &st)
	if err == nil || delivered || ctx.Err() != nil {
		return sel, st, u, err
	}
	_, transient := retryable(ctx, err)
	if !cascade.HasKind(err, cascade.KindCapabilityDenied) && (seam == nil || !transient) {
		return sel, st, u, err
	}
	reSel, selErr := e.router.Select(ctx, req, sel.LaneID)
	if selErr != nil {
		return sel, st, u, err
	}
	abandoned(sel, st.Attempts, u, err)
	u, _, err = laneAttempts(ctx, seam, reSel, run, abandoned, &st)
	return reSel, st, u, err
}

// laneAttempts runs run on sel until it succeeds, fails non-transiently,
// delivers output, or the policy stops the lane.
func laneAttempts(ctx context.Context, seam *retrySeam, sel provider.Selection, run attemptFn, abandoned abandonFn, st *retryStats) (provider.Usage, bool, error) {
	lane := LaneID(sel.LaneID)
	for n := 1; ; n++ {
		u, delivered, err := run(ctx, sel)
		st.Attempts++
		info, transient := retryable(ctx, err)
		st.Transient = st.Transient || transient
		if err == nil || delivered || !transient || seam == nil {
			return u, delivered, err
		}
		d, ok := seam.next(ctx, n, info, lane)
		if !ok || seam.sleep(ctx, d) != nil || !seam.laneEligible(ctx, lane) {
			return u, delivered, err
		}
		abandoned(sel, st.Attempts, u, err)
	}
}

// abandonedUsage attributes a superseded attempt under its own job id.
func (e *Executor) abandonedUsage(ctx context.Context, req provider.ModelRequest, jobID JobID, start time.Time) abandonFn {
	return func(s provider.Selection, n int, u provider.Usage, err error) {
		e.attributeUsage(ctx, req, attemptJobID(jobID, n), s, n, u, err, start)
	}
}

// retryTrace is ExecutionTrace plus the call's attempt facts.
type retryTrace struct {
	ExecutionTrace
	Attempts  int  `json:"attempts"`
	Transient bool `json:"transient"`
}

// auditAttempts is auditDoor for a dispatched Execute or ExecuteStream call:
// the one terminal record, carrying attempts and transient.
func (e *Executor) auditAttempts(ctx context.Context, verb DoorVerb, tier provider.SensitivityTier, key string, sel provider.Selection, outcome string, cause error, st retryStats) {
	trace := retryTrace{ExecutionTrace: ExecutionTrace{LaneID: sel.LaneID, Outcome: outcome}, Attempts: st.Attempts, Transient: st.Transient}
	if cause != nil {
		trace.Reason = cause.Error()
	}
	explain, _ := json.Marshal(trace)
	_, _ = e.pipeline.cfg.Audit.Append(context.WithoutCancel(ctx), audit.Event{
		Kind: audit.KindPolicyRoute, Actor: "conductor", Action: string(verb),
		ParamsHash: audit.HashParams([]byte(key)), RiskLevel: tier.String(),
		Verdict: outcome, Outcome: outcome, Explain: explain,
	})
}

// streamAttempt is one stream attempt's sink. An error event that arrives
// before anything reached the caller is held back, so a retried attempt
// never shows the caller a failure it recovered from; once output was
// forwarded, every event (an error included) goes to the caller as is.
type streamAttempt struct {
	ctx       context.Context
	ch        chan<- provider.StreamEvent
	u         provider.Usage
	delivered bool
	held      bool
	heldErr   error
	errSent   bool
}

func (a *streamAttempt) sink(ev provider.StreamEvent) error {
	if ev.Kind == provider.StreamEventError && !a.delivered {
		a.held, a.heldErr = true, ev.Err
		return nil
	}
	// A Done that carries no usage must not erase what an earlier Usage
	// event reported (the bundled drivers emit that pair).
	if (ev.Kind == provider.StreamEventUsage || ev.Kind == provider.StreamEventDone) && ev.Usage != (provider.Usage{}) {
		a.u = ev.Usage
	}
	if err := send(a.ctx, a.ch, ev); err != nil {
		return err
	}
	a.delivered = true
	a.errSent = a.errSent || ev.Kind == provider.StreamEventError
	return nil
}

// finish returns the attempt's usage, whether output reached the caller,
// and its error: the driver's, or the held error event's.
func (a *streamAttempt) finish(err error) (provider.Usage, bool, error) {
	if err == nil && a.held {
		err = a.heldErr
		if err == nil {
			err = cascade.New(cascade.KindUnavailable, "conductor: stream ended with an error event")
		}
	}
	return a.u, a.delivered, err
}
