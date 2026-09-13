// Purpose: unit coverage for cascadepa_review_wiring.go's adapter,
//
//	isolated from the real home directory (Art.7.1: a fake pathResolver
//	stands in for runtime.NewDefaultPathProvider) and, per
//	internal/build's no-network-unit-lane gate (Art.7.2), never naming
//	the "net" package itself -- the real client.UnixDialer (production
//	code) is used against a socket path nothing listens on, matching
//	cascadepa_wiring_test.go's own pattern for the identical class of
//	call site.
//
// SPORT: internal/plugins:cascadepa-review-wiring (TEST) -- P1-E22-W5-S47-T4.
package plugins

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/runtime"
	cascadepa "github.com/acamarata/cascade/plugins/cascade-pa"
)

func TestCascadePAReviewClient_List_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePAReviewClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.List(context.Background())
	if err == nil {
		t.Fatal("List: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("List: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestCascadePAReviewClient_List_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePAReviewClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	listing, err := c.List(context.Background())
	if err == nil {
		t.Fatal("List: err = nil, want a transport error")
	}
	if len(listing.Pending) != 0 {
		t.Errorf("List: listing = %+v on error, want zero pending", listing)
	}
	if got := err.Error(); !containsAll(got, "daemon not running or unreachable") {
		t.Errorf("List: err = %q, want internal/client's own classified transport error", got)
	}
}

func TestCascadePAReviewClient_Act_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePAReviewClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.Act(context.Background(), "claim-1", "accept")
	if err == nil {
		t.Fatal("Act: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Act: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestCascadePAReviewClient_Act_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePAReviewClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	res, err := c.Act(context.Background(), "claim-1", "accept")
	if err == nil {
		t.Fatal("Act: err = nil, want a transport error")
	}
	if res != (cascadepa.ReviewChatActResult{}) {
		t.Errorf("Act: res = %+v on error, want the zero value", res)
	}
	if got := err.Error(); !containsAll(got, "daemon not running or unreachable") {
		t.Errorf("Act: err = %q, want internal/client's own classified transport error", got)
	}
}

func TestCascadePAReviewClient_Forget_PathResolutionFailure(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePAReviewClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	_, err := c.Forget(context.Background(), "claim-1")
	if err == nil {
		t.Fatal("Forget: err = nil, want a path-resolution error")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("Forget: err = %v, want it to wrap %v", err, wantErr)
	}
}

func TestCascadePAReviewClient_Forget_TransportUnreachable(t *testing.T) {
	socket := missingSocketPath(t)
	c := newCascadePAReviewClient(client.UnixDialer, time.Second, func() (runtime.PathProvider, error) {
		return fakePathProvider{socket: socket}, nil
	})
	res, err := c.Forget(context.Background(), "claim-1")
	if err == nil {
		t.Fatal("Forget: err = nil, want a transport error")
	}
	if res != (cascadepa.ReviewChatForgetResult{}) {
		t.Errorf("Forget: res = %+v on error, want the zero value", res)
	}
	if got := err.Error(); !containsAll(got, "daemon not running or unreachable") {
		t.Errorf("Forget: err = %q, want internal/client's own classified transport error", got)
	}
}

// TestCascadePAReviewClient_CheckDigest_PropagatesListError proves
// CheckDigest is a thin wrapper over List (this file's own doc comment:
// "a digest check is simply a review.list read") rather than an
// independent code path: a List failure must surface as a CheckDigest
// failure, never a silently-zeroed signal.
func TestCascadePAReviewClient_CheckDigest_PropagatesListError(t *testing.T) {
	wantErr := errors.New("boom: no home directory")
	c := newCascadePAReviewClient(nil, time.Second, func() (runtime.PathProvider, error) {
		return nil, wantErr
	})
	signal, err := c.CheckDigest(context.Background())
	if err == nil {
		t.Fatal("CheckDigest: err = nil, want List's path-resolution error to propagate")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("CheckDigest: err = %v, want it to wrap %v", err, wantErr)
	}
	if signal != (cascadepa.DigestSignal{}) {
		t.Errorf("CheckDigest: signal = %+v on error, want the zero value", signal)
	}
}
