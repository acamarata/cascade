package write

import (
	"errors"
	"testing"
)

type mockStore struct {
	count int
	err   error
}

func (m *mockStore) PendingTasks(phase string) (int, error) {
	return m.count, m.err
}

func TestCheckPhaseIdleWorkLeftFailsClosedOnInvalidPhase(t *testing.T) {
	storeErr := errors.New("store error")
	tests := []struct {
		name    string
		store   PhaseWorkStore
		phase   string
		wantErr error
	}{
		{
			name:    "empty phase",
			store:   &mockStore{},
			phase:   "",
			wantErr: ErrInvalidPhase,
		},
		{
			name:    "whitespace phase",
			store:   &mockStore{},
			phase:   "   ",
			wantErr: ErrInvalidPhase,
		},
		{
			name:    "nil store",
			store:   nil,
			phase:   "phase1",
			wantErr: ErrPhaseStoreUnavailable,
		},
		{
			name:    "negative count",
			store:   &mockStore{count: -1},
			phase:   "phase1",
			wantErr: ErrInvalidWorkCount,
		},
		{
			name:    "store error",
			store:   &mockStore{err: storeErr},
			phase:   "phase1",
			wantErr: storeErr,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep, crunch, err := CheckPhaseIdleWorkLeft(tt.store, tt.phase)
			if rep != nil {
				t.Errorf("expected nil report, got %v", rep)
			}
			if crunch {
				t.Errorf("expected crunch to be false, got %v", crunch)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}
