package auth

import "sync"

// Purpose (this file): single-flight token renewal — the rule that several
//
//	callers meeting a 401 at the same instant produce ONE refresh, not one
//	each.
//
// Inputs: an exchange function supplied by the caller.
// Outputs: the renewed token, handed to every caller that waited.
// Constraints: split out of oauth.go when that file reached the 300-line
//
//	cap. It is a self-contained concern with its own invariant, so it is
//	the natural seam rather than an arbitrary cut.
//
// SPORT: plugins/github/auth:refresh (ADD) — P1-E25-W5-S51-T1.

// Refresher single-flights token renewal.
//
// Several in-flight API calls can meet a 401 at the same instant. Without
// this, each would start its own refresh, and every exchange after the
// first would present an already-redeemed refresh token — which GitHub
// rejects, turning one recoverable 401 into a broken session. The zero
// value is ready to use.
type Refresher struct {
	// onJoin, when set, is called each time a caller JOINS an in-flight
	// refresh rather than starting one.
	//
	// It exists so the single-flight invariant can be asserted
	// deterministically. Without it a test can only launch goroutines and
	// hope they overlap — and a test that hopes is a test that passes for
	// the wrong reason, which this one did until the race was found. Nil
	// in production: nothing outside a test ever sets it.
	onJoin func()

	mu      sync.Mutex
	running *refreshCall
}

// refreshCall is one in-flight refresh every waiter shares.
type refreshCall struct {
	done  chan struct{}
	token string
	err   error
}

// Refresh runs exchange at most once concurrently, returning the same
// result to every caller that arrived while it was running.
//
// A caller that arrives DURING a refresh waits for it rather than starting
// another; a caller that arrives after one finished starts a new one, since
// its own 401 is evidence the previous result is already stale.
func (r *Refresher) Refresh(exchange func() (string, error)) (string, error) {
	r.mu.Lock()
	if call := r.running; call != nil {
		r.mu.Unlock()
		if r.onJoin != nil {
			r.onJoin()
		}
		<-call.done
		return call.token, call.err
	}
	call := &refreshCall{done: make(chan struct{})}
	r.running = call
	r.mu.Unlock()

	call.token, call.err = exchange()
	close(call.done)

	r.mu.Lock()
	r.running = nil
	r.mu.Unlock()
	return call.token, call.err
}
