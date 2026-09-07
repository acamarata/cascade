package secrets

// Purpose: the single-flight refresh collapse -- ten concurrent callers,
//   exactly one exchange -- and the gated exchanger that proves it.
// Constraints: split from oauth_refresh_test.go under Art.10.3's 300-line
//   cap, which that file crossed when the gate waits were bounded. Every
//   wait here is bounded on purpose: an unbounded receive turns a caller
//   that never arrives into a package-wide timeout rather than a failure.
// SPORT: OAUTH_BROKER: ADD (tests).

import (
	"context"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/acamarata/cascade/pkg/provider"
)

func TestOAuthSingleFlightRefreshIssuesExactlyOneRequest(t *testing.T) {
	h, _ := grantedHarness(t)
	// The leader's exchange is held open until every follower has joined,
	// so the collapse is proven rather than won by the scheduler.
	const callers = 10
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	joined := make(chan struct{}, callers)
	h.broker.exchange = &gatedExchanger{inner: h.idp, arrived: arrived, release: release}
	h.broker.joined = joined

	var wg sync.WaitGroup
	results := make([]provider.TokenRecord, callers)
	errs := make([]error, callers)
	// leaderDone closes when caller 0 returns. Its only purpose is to make
	// the wait below bounded: see the select for why an unbounded receive
	// here is a hang rather than a failure.
	leaderDone := make(chan struct{})
	start := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = h.broker.Refresh(context.Background(), defaultAccount)
			if i == 0 {
				close(leaderDone)
			}
		}()
	}
	start(0)

	awaitLeaderAtGate(t, arrived, leaderDone, errs)

	for i := 1; i < callers; i++ {
		start(i)
	}
	awaitFollowersAtGate(t, joined, callers-1)
	close(release)
	wg.Wait()

	if got := h.idp.callCount(); got != 2 {
		t.Fatalf("%d exchanges reached the IdP (1 authorization + 1 refresh expected), want 2", got)
	}
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("caller %d got an error: %v", i, errs[i])
		}
		if results[i].AccessRef != results[0].AccessRef {
			t.Fatalf("caller %d got a different record than the leader", i)
		}
	}
}

// gateArrivalTimeout bounds every wait for a caller to reach the
// single-flight gate. It is generous because it is not measuring anything:
// it exists only so a caller that never arrives fails in seconds with a
// message naming what went wrong, instead of consuming the package's
// 10-minute timeout and taking every other test in internal/secrets down
// with it. Nothing asserts on this duration.
const gateArrivalTimeout = 30 * time.Second

// gatedExchanger holds the first exchange open until release is closed, so
// every concurrent caller has definitely reached the single-flight gate.
type gatedExchanger struct {
	inner   tokenExchanger
	once    sync.Once
	arrived chan struct{}
	release chan struct{}
}

func (g *gatedExchanger) Exchange(ctx context.Context, endpoint string, form url.Values) ([]byte, int, error) {
	g.once.Do(func() {
		g.arrived <- struct{}{}
		<-g.release
	})
	return g.inner.Exchange(ctx, endpoint, form)
}

// awaitLeaderAtGate waits for the leader to reach the gated exchanger.
//
// The wait is bounded, and that is the whole point. This was a bare
// `<-arrived`, which is a hang by construction: it assumes the leader
// reaches the exchanger, so if the leader returns EARLY for any reason --
// a platform refusal, a validation error, a future regression in Refresh
// -- nothing is ever sent and the receive blocks until the package times
// out. The windows lane proved it: Refresh returns before the exchanger is
// touched there, so this test consumed the entire 10-minute timeout and
// took every other test in internal/secrets down with it, reporting a hang
// instead of the one-line refusal that actually happened.
//
// Reading errs[0] after leaderDone is closed is race-free: the close
// happens-after the write.
func awaitLeaderAtGate(t *testing.T, arrived, leaderDone <-chan struct{}, errs []error) {
	t.Helper()
	select {
	case <-arrived:
	case <-leaderDone:
		t.Fatalf("the leader returned without ever reaching the exchanger, so the single-flight gate was never entered: %v", errs[0])
	case <-time.After(gateArrivalTimeout):
		t.Fatalf("the leader neither reached the exchanger nor returned within %s", gateArrivalTimeout)
	}
}

// awaitFollowersAtGate waits for want followers to join the single-flight
// gate, bounded for the same reason as the leader's wait.
func awaitFollowersAtGate(t *testing.T, joined <-chan struct{}, want int) {
	t.Helper()
	for i := 0; i < want; i++ {
		select {
		case <-joined:
		case <-time.After(gateArrivalTimeout):
			t.Fatalf("only %d of %d followers reached the single-flight gate within %s", i, want, gateArrivalTimeout)
		}
	}
}
