package hydration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
	"github.com/acamarata/cascade/pkg/provider"
)

// fixedClock freezes the window's "now".
type fixedClock struct{ at time.Time }

func (c fixedClock) Now() time.Time { return c.at }

// memStore returns a fresh in-memory store and a StoreFunc over it.
func memStore(t *testing.T) (provider.Store, StoreFunc) {
	t.Helper()
	store := storetest.NewMemStore()
	return store, func(context.Context) (provider.Store, func(), error) { return store, func() {}, nil }
}

// TestHydrationDoctorCheckThresholds walks R-16.6a's two boundaries from
// both sides. A check asserted only above its thresholds would pass for
// one that always warned.
func TestHydrationDoctorCheckThresholds(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		events int
		want   doctor.Status
	}{
		{"none is ok", 0, doctor.StatusOK},
		{"one warns", 1, doctor.StatusWarn},
		{"twenty still warns", FailAbove, doctor.StatusWarn},
		{"twenty-one fails", FailAbove + 1, doctor.StatusError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, storeFn := memStore(t)
			for i := 0; i < tc.events; i++ {
				PublishDegraded(context.Background(), store, []byte(`{"reason":"slice_failed"}`))
			}
			got, err := NewCheck(storeFn, fixedClock{at: now}).Run(context.Background())
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got.Status != tc.want {
				t.Fatalf("status = %v, want %v (message %q)", got.Status, tc.want, got.Message)
			}
		})
	}
}

// TestOnlyEventsInsideTheWindowCount proves the window is real. A check
// that counted the whole log would report a machine healthy for a year
// as failing forever after one bad afternoon.
func TestOnlyEventsInsideTheWindowCount(t *testing.T) {
	store := storetest.NewMemStore()
	PublishDegraded(context.Background(), store, []byte(`{"reason":"old"}`))

	// Counted from a "now" two windows after the event was published, so
	// the event is unambiguously outside. Using a one-nanosecond window
	// against the real clock would race the clock's own resolution.
	future := time.Now().Add(2 * DegradedWindow)
	count, err := CountDegraded(context.Background(), store, future, DegradedWindow)
	if err != nil {
		t.Fatalf("CountDegraded: %v", err)
	}
	if count != 0 {
		t.Fatalf("counted %d events published two windows before the cutoff", count)
	}
	if count, err = CountDegraded(context.Background(), store, time.Now(), DegradedWindow); err != nil || count != 1 {
		t.Fatalf("count over the real window = %d (err %v), want 1", count, err)
	}
}

// TestAnUnreadableLogIsAnErrorNotOK is Art.1 stated as a test: a check
// that could not look at its subject must not report it healthy. This
// matters more here than in most checks, because hydration fails OPEN —
// a false OK would mean nothing in the system ever says hydration stopped
// working.
func TestAnUnreadableLogIsAnErrorNotOK(t *testing.T) {
	boom := errors.New("data directory is unreadable")
	failing := func(context.Context) (provider.Store, func(), error) { return nil, nil, boom }

	got, err := NewCheck(failing, runtime.SystemClock{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run returned an error instead of a result: %v", err)
	}
	if got.Status != doctor.StatusError {
		t.Fatalf("status = %v, want StatusError", got.Status)
	}
	if got.Detail != boom.Error() {
		t.Errorf("detail = %q, want the underlying failure", got.Detail)
	}
}

// TestAnUnwiredCheckReportsError covers the build-defect case: a check
// constructed with no store at all.
func TestAnUnwiredCheckReportsError(t *testing.T) {
	got, err := NewCheck(nil, nil).Run(context.Background())
	if err != nil || got.Status != doctor.StatusError {
		t.Fatalf("status = %v (err %v), want StatusError", got.Status, err)
	}
}

// TestTheCheckIsNotFixable pins the declaration against the behaviour:
// doctor's Runner never calls Fix on a check that declares Fixable=false,
// but Fix itself must stay total and correct when called directly.
func TestTheCheckIsNotFixable(t *testing.T) {
	check := NewCheck(nil, nil)
	if check.Metadata().Fixable {
		t.Fatal("the check declares itself fixable")
	}
	if _, err := check.Fix(context.Background()); !errors.Is(err, doctor.ErrCheckNotFixable) {
		t.Fatalf("Fix returned %v, want ErrCheckNotFixable", err)
	}
	if check.Name() != CheckName || check.Describe() == "" {
		t.Errorf("name = %q, describe = %q", check.Name(), check.Describe())
	}
}

// TestPublishingToNothingIsANoOp pins the best-effort contract at the
// writer, where the hook reaches it on paths that are already failing.
func TestPublishingToNothingIsANoOp(t *testing.T) {
	PublishDegraded(context.Background(), nil, []byte(`{}`))
	if count, err := CountDegraded(context.Background(), nil, time.Now(), DegradedWindow); err != nil || count != 0 {
		t.Fatalf("count over a nil store = %d (err %v)", count, err)
	}
}
