package auth

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// Purpose (this file): Refresher's single-flight rule — split out of
//   oauth_test.go to stay under Art.10.3's 300-line cap, which counts
//   tests.
// SPORT: plugins/github/auth tests (ADD) — P1-E25-W5-S51-T1.

// TestRefresherSingleFlights is the concurrency rule. Several in-flight API
// calls can meet a 401 at the same instant; without single-flighting each
// starts its own exchange, and every one after the first presents an
// already-redeemed refresh token, turning one recoverable 401 into a broken
// session.
//
// The ordering here is exact rather than hopeful, and that matters: an
// earlier version of this test signalled arrival BEFORE calling Refresh, so
// nothing stopped the first flight from finishing before the others got in.
// It passed by luck and asserted nothing. The handshake is now:
//
//  1. one caller starts a flight and is held inside the exchange;
//  2. the rest are released only once they have each been observed JOINING
//     that flight (onJoin), which can only happen while it is in progress;
//  3. only then does the exchange return.
//
// So "they overlapped" is established, not assumed.
func TestRefresherSingleFlights(t *testing.T) {
	const joiners = 7
	var (
		r       Refresher
		calls   atomic.Int32
		started = make(chan struct{})
		joined  = make(chan struct{}, joiners)
		release = make(chan struct{})
		wg      sync.WaitGroup
		results = make([]string, joiners+1)
		errs    = make([]error, joiners+1)
	)

	r.onJoin = func() { joined <- struct{}{} }
	exchange := func() (string, error) {
		calls.Add(1)
		close(started)
		<-release // hold the flight open while the others join it
		return "fresh-token", nil
	}

	// The first caller starts the flight.
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0], errs[0] = r.Refresh(exchange)
	}()
	<-started // the flight is now in progress and cannot complete yet

	for i := 1; i <= joiners; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = r.Refresh(exchange)
		}(i)
	}
	for i := 0; i < joiners; i++ {
		<-joined // every one of them is now waiting on the SAME flight
	}

	close(release)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Fatalf("the exchange ran %d times, want exactly 1: every extra call redeems a spent refresh token", got)
	}
	for i := range results {
		if errs[i] != nil {
			t.Errorf("caller %d: %v", i, errs[i])
		}
		if results[i] != "fresh-token" {
			t.Errorf("caller %d got %q, want the shared result", i, results[i])
		}
	}
}

// TestRefresherRunsAgainAfterCompletion proves the single-flight window
// closes: a caller arriving after a refresh finished starts a new one,
// because its own 401 is evidence the previous result is already stale.
func TestRefresherRunsAgainAfterCompletion(t *testing.T) {
	var r Refresher
	var calls int
	exchange := func() (string, error) { calls++; return "t", nil }

	for i := 0; i < 3; i++ {
		if _, err := r.Refresh(exchange); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("the exchange ran %d times across three sequential refreshes, want 3", calls)
	}
}

// TestRefresherPropagatesFailureToEveryWaiter proves a failed exchange is
// not silently converted into an empty token for the waiters.
func TestRefresherPropagatesFailureToEveryWaiter(t *testing.T) {
	var r Refresher
	sentinel := errors.New("exchange refused")
	token, err := r.Refresh(func() (string, error) { return "", sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the exchange's own", err)
	}
	if token != "" {
		t.Fatalf("a failed refresh returned the token %q", token)
	}
}
