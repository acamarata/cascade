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

	// Check OwnerID field exists
	if _, found := grantType.FieldByName("OwnerID"); !found {
		t.Error("StandingGrant missing OwnerID field")
	}

	// Check LaneOps field exists
	if _, found := grantType.FieldByName("LaneOps"); !found {
		t.Error("StandingGrant missing LaneOps field")
	}

	// Check GrantOps field exists
	if _, found := grantType.FieldByName("GrantOps"); !found {
		t.Error("StandingGrant missing GrantOps field")
	}

	// Check JobsOps field exists
	if _, found := grantType.FieldByName("JobsOps"); !found {
		t.Error("StandingGrant missing JobsOps field")
	}

	// Check ExpiresAt field exists
	if _, found := grantType.FieldByName("ExpiresAt"); !found {
		t.Error("StandingGrant missing ExpiresAt field")
	}

	// Check Revoked field exists
	if _, found := grantType.FieldByName("Revoked"); !found {
		t.Error("StandingGrant missing Revoked field")
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
