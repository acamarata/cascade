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
