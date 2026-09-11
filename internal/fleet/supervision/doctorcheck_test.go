package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/acamarata/cascade/internal/doctor"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/internal/storage/storetest"
)

func TestAttentionCheckRunOKOnReachableStoreAndLiveSubscription(t *testing.T) {
	store := NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	check := NewAttentionCheck(store, func() bool { return true })
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusOK {
		t.Errorf("Status = %v, want StatusOK; message=%q", result.Status, result.Message)
	}
}

func TestAttentionCheckRunWarnsOnDeadSubscription(t *testing.T) {
	store := NewStore(storetest.NewMemStore(), runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	check := NewAttentionCheck(store, func() bool { return false })
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusWarn {
		t.Errorf("Status = %v, want StatusWarn", result.Status)
	}
}

func TestAttentionCheckRunErrorsOnNilStore(t *testing.T) {
	check := NewAttentionCheck(nil, nil)
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusError {
		t.Errorf("Status = %v, want StatusError for a nil store", result.Status)
	}
}

func TestAttentionCheckRunErrorsOnUnreachableStore(t *testing.T) {
	store := NewStore(brokenKVStore{}, runtime.NewFixedClock(time.Now()), nil, sequentialIDGenerator(), 0)
	check := NewAttentionCheck(store, func() bool { return true })
	result, err := check.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != doctor.StatusError {
		t.Errorf("Status = %v, want StatusError when the domain table is unreachable", result.Status)
	}
}

func TestAttentionCheckMetadataAndFix(t *testing.T) {
	check := NewAttentionCheck(nil, nil)
	if check.Metadata().Fixable {
		t.Error("Metadata().Fixable = true, want false")
	}
	if _, err := check.Fix(context.Background()); err != doctor.ErrCheckNotFixable {
		t.Errorf("Fix() err = %v, want ErrCheckNotFixable", err)
	}
	if check.Name() != AttentionCheckName {
		t.Errorf("Name() = %q, want %q", check.Name(), AttentionCheckName)
	}
	if check.Describe() == "" {
		t.Error("Describe() is empty")
	}
}
