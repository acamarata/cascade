package delegation

// Purpose: the collector's refusal and failure branches: driver errors,
// invalid states, a stopped clock, the deadline watcher's exits and the
// addressed-result reader's integrity checks.

import (
	"context"
	"errors"
	"testing"

	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/notify"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// faultDriver returns a fixed state and the configured errors.
type faultDriver struct {
	provider.AgentProvider
	state      provider.AgentRunState
	statusErr  error
	collectErr error
}

func (d *faultDriver) Status(context.Context, provider.AgentJobID) (provider.AgentRunState, error) {
	return d.state, d.statusErr
}

func (d *faultDriver) Collect(context.Context, provider.AgentJobID) (provider.CollectResult, error) {
	return provider.CollectResult{}, d.collectErr
}

// TestCollectorDriverFaultsFailTheRecord: a Status error, an invalid state
// and a Collect error each end the delegation failed with "collector failed"
// and still release every resource once.
func TestCollectorDriverFaultsFailTheRecord(t *testing.T) {
	boom := errors.New("driver fault")
	for name, d := range map[string]*faultDriver{
		"status":  {statusErr: boom},
		"invalid": {state: provider.AgentRunState("bogus")},
		"collect": {state: provider.AgentRunAccepted, collectErr: boom},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCollectFixture(t)
			f.c.handle.Driver = d
			if err := f.c.run(context.Background()); err == nil {
				t.Fatal("run succeeded on a driver fault")
			}
			f.assertReleased(t, jobs.DelegationFailed, "collector failed")
		})
	}
}

// TestCollectorStoppedClockFails: a running job with a closed tick channel
// fails as unavailable instead of spinning.
func TestCollectorStoppedClockFails(t *testing.T) {
	f := newCollectFixture(t)
	f.c.handle.Driver = &faultDriver{state: provider.AgentRunRunning}
	ticks := make(chan struct{})
	close(ticks)
	f.c.ticks = ticks
	_, _, err := f.c.collect(context.Background())
	if !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("collect = %v, want KindUnavailable", err)
	}
}

// TestWatchDeadlineExits: the watcher returns nil when the collector is done,
// and releases the handle when its context ends or its ticks stop.
func TestWatchDeadlineExits(t *testing.T) {
	f := newCollectFixture(t)
	done := make(chan struct{})
	close(done)
	if err := f.c.watchDeadline(context.Background(), nil, make(chan struct{}, 1), done, func() {}); err != nil {
		t.Fatalf("done = %v", err)
	}
	if f.releases != 0 {
		t.Fatal("watcher released on a finished collector")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := false
	if err := f.c.watchDeadline(ctx, nil, make(chan struct{}, 1), nil, func() { canceled = true }); err != nil || !canceled {
		t.Fatalf("ctx done = %v, canceled %v", err, canceled)
	}
	g := newCollectFixture(t)
	ticks := make(chan struct{})
	close(ticks)
	if err := g.c.watchDeadline(context.Background(), ticks, make(chan struct{}, 1), nil, func() {}); err != nil || g.releases != 1 {
		t.Fatalf("stopped ticks = %v, releases %d", err, g.releases)
	}
}

// TestAddressedResultIntegrity: other kinds are skipped; a payload that does
// not decode or names another job is an integrity error.
func TestAddressedResultIntegrity(t *testing.T) {
	if _, found, err := addressedResult([]notify.Notification{{Kind: "other"}}, "d1"); found || err != nil {
		t.Fatalf("other kind = %v, %v", found, err)
	}
	for name, payload := range map[string]string{"garbage": "{", "foreign": `{"job_id":"d2"}`} {
		_, _, err := addressedResult([]notify.Notification{{Kind: "delegation.result", Payload: []byte(payload)}}, "d1")
		if !cascade.HasKind(err, cascade.KindIntegrity) {
			t.Errorf("%s = %v, want KindIntegrity", name, err)
		}
	}
}

// TestWaitRefusals: an unknown delegation is an error; a canceled caller and
// a stopped clock end the wait with an error.
func TestWaitRefusals(t *testing.T) {
	f := newCollectFixture(t)
	owner := principal.WithCaller(context.Background(), principal.Caller{OwnerCLI: true})
	if _, err := f.c.service.wait(owner, f.c.notify, "missing", 0, nil); err == nil {
		t.Fatal("wait on an unknown delegation succeeded")
	}
	id := f.c.record.ID
	ticks := make(chan struct{})
	close(ticks)
	if _, err := f.c.service.wait(owner, f.c.notify, id, 1<<40, ticks); !cascade.HasKind(err, cascade.KindUnavailable) {
		t.Fatalf("stopped clock = %v, want KindUnavailable", err)
	}
	ctx, cancel := context.WithCancel(owner)
	cancel()
	if _, err := f.c.service.wait(ctx, f.c.notify, id, 1<<40, make(chan struct{})); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v, want context.Canceled", err)
	}
}
