package ownerauth

import (
	"reflect"
	"testing"
	"time"
)

func TestStandingGrantTypes(t *testing.T) {
	// Test StandingGrant struct has required fields
	grant := StandingGrant{
		OwnerID:   "test-owner",
		LaneOps:   []string{"read"},
		GrantOps:  []string{"create"},
		JobsOps:   []string{"execute"},
		ExpiresAt: time.Now().Add(DefaultDuration),
		Revoked:   false,
	}

	grantType := reflect.TypeOf(grant)

	for _, field := range []string{"OwnerID", "LaneOps", "GrantOps", "JobsOps", "ExpiresAt", "Revoked"} {
		if _, found := grantType.FieldByName(field); !found {
			t.Errorf("StandingGrant missing %s field", field)
		}
	}

	// Test DefaultDuration constant
	expectedDuration := 7 * 24 * time.Hour
	if DefaultDuration != expectedDuration {
		t.Errorf("DefaultDuration = %v, want %v", DefaultDuration, expectedDuration)
	}

	// Test RequestKind constants exist
	_ = KindLane
	_ = KindGrant
	_ = KindJobs

	// Test sentinel errors exist
	if ErrNotOwnerSession == nil {
		t.Error("ErrNotOwnerSession is nil")
	}
	if ErrGrantRevoked == nil {
		t.Error("ErrGrantRevoked is nil")
	}
	if ErrGrantExpired == nil {
		t.Error("ErrGrantExpired is nil")
	}
}

func TestRevokeAndRenew(t *testing.T) {
	t.Run("Revoke sets Revoked to true", func(t *testing.T) {
		grant := &StandingGrant{
			OwnerID:   "test-owner",
			ExpiresAt: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
			Revoked:   false,
		}

		grant.Revoke()

		if !grant.Revoked {
			t.Error("expected Revoked to be true after Revoke")
		}
	})

	t.Run("Renew extends ExpiresAt on valid grant", func(t *testing.T) {
		grant := &StandingGrant{
			OwnerID:   "test-owner",
			ExpiresAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Revoked:   false,
		}
		duration := 24 * time.Hour

		err := grant.Renew(duration)

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		if !grant.ExpiresAt.Equal(expected) {
			t.Errorf("expected ExpiresAt %v, got %v", expected, grant.ExpiresAt)
		}
	})

	t.Run("Renew returns ErrGrantRevoked on revoked grant", func(t *testing.T) {
		grant := &StandingGrant{
			OwnerID:   "test-owner",
			ExpiresAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Revoked:   true,
		}

		err := grant.Renew(24 * time.Hour)

		if err != ErrGrantRevoked {
			t.Errorf("expected ErrGrantRevoked, got %v", err)
		}
	})

	t.Run("revoke stops renewals at once", func(t *testing.T) {
		grant := &StandingGrant{
			OwnerID:   "test-owner",
			ExpiresAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Revoked:   false,
		}

		grant.Revoke()
		err := grant.Renew(24 * time.Hour)

		if err != ErrGrantRevoked {
			t.Errorf("expected ErrGrantRevoked after revoke, got %v", err)
		}
	})
}
