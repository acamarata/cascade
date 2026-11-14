// Package conductor: merge lease types and contract interface tests.
// SPORT: conductor.merge-lease/ADD (P2-GND-12).

package conductor

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestMergeLeaseTypesExist verifies that the merge lease contract types
// and interface are properly defined: MergeLease struct with required
// fields, ErrNotOwner sentinel error, and LeaseStore interface with
// Acquire, Release, and Takeover methods.
func TestMergeLeaseTypesExist(t *testing.T) {
	// Verify MergeLease struct exists with required fields
	lease := MergeLease{
		Repo:      "test-repo",
		Branch:    "main",
		HolderID:  "holder-123",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}

	leaseType := reflect.TypeOf(lease)
	if leaseType.Kind() != reflect.Struct {
		t.Fatalf("MergeLease should be a struct, got %v", leaseType.Kind())
	}

	// Verify required fields exist
	requiredFields := []string{"Repo", "Branch", "HolderID", "ExpiresAt"}
	for _, fieldName := range requiredFields {
		field, ok := leaseType.FieldByName(fieldName)
		if !ok {
			t.Errorf("MergeLease missing required field: %s", fieldName)
			continue
		}
		if field.Name != fieldName {
			t.Errorf("Field name mismatch: expected %s, got %s", fieldName, field.Name)
		}
	}

	// Verify ErrNotOwner sentinel error exists and is not nil
	if ErrNotOwner == nil {
		t.Error("ErrNotOwner should be a non-nil error")
	}
	if !errors.Is(ErrNotOwner, ErrNotOwner) {
		t.Error("ErrNotOwner should be a proper sentinel error")
	}

	// Verify LeaseStore interface exists with required methods
	var store LeaseStore
	storeType := reflect.TypeOf(&store).Elem()
	if storeType.Kind() != reflect.Interface {
		t.Fatalf("LeaseStore should be an interface, got %v", storeType.Kind())
	}

	// Verify required methods exist
	requiredMethods := []string{"Acquire", "Release", "Takeover"}
	for _, methodName := range requiredMethods {
		method, ok := storeType.MethodByName(methodName)
		if !ok {
			t.Errorf("LeaseStore missing required method: %s", methodName)
			continue
		}
		if method.Name != methodName {
			t.Errorf("Method name mismatch: expected %s, got %s", methodName, method.Name)
		}
	}

	// Verify method signatures have correct parameter and return types
	acquireMethod, ok := storeType.MethodByName("Acquire")
	if ok {
		// Acquire should have: ctx, repo, branch, holderID, ttl -> *MergeLease, error
		if acquireMethod.Type.NumIn() != 5 {
			t.Errorf("Acquire should have 5 parameters, got %d", acquireMethod.Type.NumIn())
		}
		if acquireMethod.Type.NumOut() != 2 {
			t.Errorf("Acquire should return 2 values, got %d", acquireMethod.Type.NumOut())
		}
	}

	releaseMethod, ok := storeType.MethodByName("Release")
	if ok {
		// Release should have: ctx, repo, branch, holderID -> error
		if releaseMethod.Type.NumIn() != 4 {
			t.Errorf("Release should have 4 parameters, got %d", releaseMethod.Type.NumIn())
		}
		if releaseMethod.Type.NumOut() != 1 {
			t.Errorf("Release should return 1 value, got %d", releaseMethod.Type.NumOut())
		}
	}

	takeoverMethod, ok := storeType.MethodByName("Takeover")
	if ok {
		// Takeover should have: ctx, repo, branch, holderID, ttl -> *MergeLease, error
		if takeoverMethod.Type.NumIn() != 5 {
			t.Errorf("Takeover should have 5 parameters, got %d", takeoverMethod.Type.NumIn())
		}
		if takeoverMethod.Type.NumOut() != 2 {
			t.Errorf("Takeover should return 2 values, got %d", takeoverMethod.Type.NumOut())
		}
	}

	// Compile-time check that a LeaseStore can be used
	var _ LeaseStore = (*mockLeaseStore)(nil)
}

// mockLeaseStore is a minimal implementation for compile-time verification
type mockLeaseStore struct{}

func (m *mockLeaseStore) Acquire(ctx context.Context, repo, branch, holderID string, ttl time.Duration) (*MergeLease, error) {
	return nil, nil
}

func (m *mockLeaseStore) Release(ctx context.Context, repo, branch, holderID string) error {
	return nil
}

func (m *mockLeaseStore) Takeover(ctx context.Context, repo, branch, holderID string, ttl time.Duration) (*MergeLease, error) {
	return nil, nil
}
