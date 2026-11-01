# Transient retry

A model call that fails for a transient upstream reason is retried in one
place: the conductor's Executor. Drivers only mark a failure as transient.
They never retry it themselves.

## The marker (`pkg/provider/transient.go`)

`provider.MarkTransient(err, TransientInfo{Cause, Status, RetryAfter})`
wraps a driver error. The error's Kind, wire code and message stay the same.
`provider.TransientOf(err)` reads the mark back through any number of
`cascade.Wrap` layers.

The cause set is closed:

| Cause | Meaning |
|---|---|
| `http_5xx` | any 5xx except 501 and 505 |
| `overloaded` | status 529, or an error body of type `overloaded_error` |
| `stream_api_error` | an SSE `error` event of type `api_error` or `overloaded_error` |
| `transport` | a connection reset, abort, broken pipe or early EOF while the caller's context was live |

The anthropic driver (`providers/anthropic/transient.go`) classifies by
status, error type and transport outcome only, never by message text.
These are never marked: any 4xx (429 belongs to the quota spill), 501, 505,
and a failure caused by the caller's own cancellation or deadline.

## The policy (`internal/conductor/transient_retry.go`)

`RetryPolicy{MaxAttempts, Base, Cap}`. `DefaultRetryPolicy()` is 3 attempts
per lane, a 500ms base and an 8s cap.

The pause before attempt n+1 is a full-jitter draw, uniform over
`[0, min(Cap, Base * 2^(n-1))]`, taken from the injected seeded
`*rand.Rand`. A Retry-After hint up to Cap is the lower bound. The pause
goes through the injected `Sleeper`, which returns early when the context
ends. The code never calls `time.Sleep`.

A lane stops taking attempts when:

- the attempt count reaches MaxAttempts;
- the error is not transient;
- the context is done;
- the time left before the deadline (by the injected clock) is shorter than
  the pause, so a pause never runs past the deadline;
- Retry-After exceeds Cap;
- `LaneEligible` reports the lane ineligible before the next attempt.

When a lane stops on a transient error, the Executor calls `Router.Select`
once more with that lane excluded and applies the same policy on the new
lane. A capability denial uses this same single re-selection, so a call
re-selects at most once and makes at most `2 * MaxAttempts` attempts.

These are never retried and never trigger the transient spill: a 4xx
(including a 429 even if something marked it), permission, capability or
policy denial, cancellation, deadline, and credential or helper failures.
These carry no mark, or a Kind outside `unavailable` and `internal`.

## Streams

A stream is retried only while nothing has reached the caller. An error
event that arrives before the first forwarded event is held back. If the
attempt is retried, the caller never sees it. Once any event has been
forwarded, a failure is final. It reaches the caller once, as the single
terminal error event.

## Records

Every attempt the Executor gives up on is attributed under its own usage
row (`<job>#attempt-<n>`). The final row's `Attempt` is the total attempt
count. The call's one terminal audit record carries `attempts` (int) and
`transient` (true when any attempt failed transiently) in its Explain
payload, next to `lane_id`, `outcome` and `reason`.

## Wiring

`Executor.SetRetry(policy, clock, sleeper, rnd, eligible)` installs the
policy. With none installed, every call makes one attempt per lane.
The daemon builds its Executor inside `internal/daemon`. So the composition
in `cmd/cascade/daemon_unix_conductor.go` passes `conductor.RetryOption`
with its router options, and the Executor built over that router applies it
before its first dispatch. The daemon uses the default policy, its clock, a
timer-based sleeper that honours the context, and a source seeded once from
the clock. `eligible` is nil until provider health is wired (P1-GWY-09).

Retry does not write lane state or provider health.
