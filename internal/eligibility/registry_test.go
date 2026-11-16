package eligibility

import (
	"errors"
	"testing"
	"time"
)

// frozenClock is a test clock that returns a fixed time.
type frozenClock struct {
	now time.Time
}

// Now returns the frozen time.
func (f *frozenClock) Now() time.Time {
	return f.now
}

// registryTestCase holds a single scenario for TestRegistryDefaultsDeny.
type registryTestCase struct {
	// name is the subtest name.
	name string
	// run executes the subtest logic.
	run func(t *testing.T, r Registry, base Row, clk *frozenClock)
}

// registryTests enumerates all scenarios checked by TestRegistryDefaultsDeny.
var registryTests = []registryTestCase{
	{
		name: "default deny for unknown operation",
		run: func(t *testing.T, r Registry, _ Row, _ *frozenClock) {
			row, err := r.Get("unknown-op")
			if err != nil {
				t.Fatalf("Get returned unexpected error: %v", err)
			}
			if row.PermittedBrowser {
				t.Errorf("expected PermittedBrowser to be false by default")
			}
			if row.ConsumerChatAutomation {
				t.Errorf("expected ConsumerChatAutomation to be false by default")
			}
			if row.ProviderAuthorised {
				t.Errorf("expected ProviderAuthorised to be false by default")
			}
		},
	},
	{
		name: "set base row",
		run: func(t *testing.T, r Registry, base Row, _ *frozenClock) {
			if err := r.Set(base); err != nil {
				t.Fatalf("Set failed for base row: %v", err)
			}
		},
	},
	{
		name: "reject loosening without elevation",
		run: func(t *testing.T, r Registry, base Row, _ *frozenClock) {
			loosened := base
			loosened.PermittedBrowser = true
			err := r.Set(loosened)
			if !errors.Is(err, ErrLooseningWithoutElevation) {
				t.Errorf("expected ErrLooseningWithoutElevation, got: %v", err)
			}
		},
	},
	{
		name: "allow tightening",
		run: func(t *testing.T, r Registry, base Row, _ *frozenClock) {
			tightened := base
			tightened.PermittedBrowser = false
			tightened.ConsumerChatAutomation = false
			if err := r.Set(tightened); err != nil {
				t.Errorf("Set failed for tightened row: %v", err)
			}
		},
	},
	{
		name: "expired row detected",
		run: func(t *testing.T, r Registry, base Row, clk *frozenClock) {
			expired := base
			expired.Expiry = clk.Now().Add(-1 * time.Hour)
			if !r.IsExpired(expired, clk) {
				t.Errorf("expected IsExpired to return true for expired row")
			}
		},
	},
	{
		name: "valid row not expired",
		run: func(t *testing.T, r Registry, base Row, clk *frozenClock) {
			valid := base
			valid.Expiry = clk.Now().Add(1 * time.Hour)
			if r.IsExpired(valid, clk) {
				t.Errorf("expected IsExpired to return false for valid row")
			}
		},
	},
	{
		name: "consumer automation blocked by default",
		run: func(t *testing.T, r Registry, _ Row, _ *frozenClock) {
			autoRow, err := r.Get("auto-op")
			if err != nil {
				t.Fatalf("Get returned unexpected error: %v", err)
			}
			if autoRow.ConsumerChatAutomation {
				t.Errorf("expected ConsumerChatAutomation to be blocked by default")
			}
		},
	},
}

// TestRegistryDefaultsDeny verifies that unregistered operations default to deny,
// expiry is enforced, and loosening changes are rejected.
func TestRegistryDefaultsDeny(t *testing.T) {
	r := NewMemoryRegistry()
	now := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := &frozenClock{now: now}

	baseRow := Row{
		OperationID:            "op-1",
		TermsSource:            "system",
		LastReviewed:           clk.Now(),
		Expiry:                 clk.Now().Add(1 * time.Hour),
		PermittedBrowser:       false,
		ConsumerChatAutomation: false,
		ProviderAuthorised:     false,
	}

	for _, tc := range registryTests {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, r, baseRow, clk)
		})
	}
}
