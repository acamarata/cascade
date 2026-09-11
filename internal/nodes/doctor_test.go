package nodes

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/testkit"
	"github.com/acamarata/cascade/pkg/cascade"
)

func TestHealthCheckEmptyFleet(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Now())
	check := NewHealthCheck(NewRecordStore(newMemRecordBackend(), clock), clock, 0)
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("got status %v, want StatusOK for an empty fleet", res.Status)
	}
}

func TestHealthCheckAllReachable(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(newMemRecordBackend(), clock)
	id := testIdentity(t, "h1")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	rec.LastSeen = clock.Now()
	if err := store.put(rec); err != nil {
		t.Fatal(err)
	}
	check := NewHealthCheck(store, clock, time.Minute)
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != doctor.StatusOK {
		t.Fatalf("got status %v, want StatusOK", res.Status)
	}
}

func TestHealthCheckStaleNodeWarns(t *testing.T) {
	clock := testkit.NewFrozenClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	store := NewRecordStore(newMemRecordBackend(), clock)
	id := testIdentity(t, "h2")
	rec, err := store.Enroll(id, TierWorkerTrusted)
	if err != nil {
		t.Fatal(err)
	}
	rec.LastSeen = clock.Now().Add(-time.Hour)
	if err := store.put(rec); err != nil {
		t.Fatal(err)
	}
	check := NewHealthCheck(store, clock, time.Minute)
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != doctor.StatusWarn {
		t.Fatalf("got status %v, want StatusWarn for a stale node", res.Status)
	}
	if res.Detail == "" {
		t.Fatal("expected Detail to name the not-reachable node")
	}
}

func TestHealthCheckNilStoreIsError(t *testing.T) {
	check := NewHealthCheck(nil, testkit.NewFrozenClock(time.Now()), time.Minute)
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("got status %v, want StatusError for a nil store (Art.1: never a silent OK)", res.Status)
	}
}

func TestHealthCheckMetadataAndFix(t *testing.T) {
	check := NewHealthCheck(NewRecordStore(newMemRecordBackend(), testkit.NewFrozenClock(time.Now())), testkit.NewFrozenClock(time.Now()), 0)
	meta := check.Metadata()
	if meta.Fixable {
		t.Fatal("expected Fixable=false")
	}
	if _, err := check.Fix(context.Background()); err != doctor.ErrCheckNotFixable {
		t.Fatalf("got %v, want ErrCheckNotFixable", err)
	}
	if check.Name() != NodesCheckName {
		t.Fatalf("got name %q, want %q", check.Name(), NodesCheckName)
	}
	if check.Describe() == "" {
		t.Fatal("expected a non-empty Describe()")
	}
}

// TestHealthCheckCanceledContextIsError proves Run refuses on an
// already-canceled context rather than reading the store anyway.
func TestHealthCheckCanceledContextIsError(t *testing.T) {
	check := NewHealthCheck(NewRecordStore(newMemRecordBackend(), testkit.NewFrozenClock(time.Now())), testkit.NewFrozenClock(time.Now()), 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := check.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("got status %v, want StatusError for a canceled context", res.Status)
	}
}

// TestHealthCheckListErrorIsStatusError proves an unreadable record
// store is reported as StatusError, not silently treated as empty.
func TestHealthCheckListErrorIsStatusError(t *testing.T) {
	backend := newMemRecordBackend()
	backend.loadErr = cascade.New(cascade.KindUnavailable, "simulated storage outage")
	clock := testkit.NewFrozenClock(time.Now())
	check := NewHealthCheck(NewRecordStore(backend, clock), clock, 0)
	res, err := check.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != doctor.StatusError {
		t.Fatalf("got status %v, want StatusError for an unreadable store", res.Status)
	}
}
