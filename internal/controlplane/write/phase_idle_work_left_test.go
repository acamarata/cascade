package write

import (
	"errors"
	"testing"
)

type mockStore struct {
	count int
	err   error
}

func (m *mockStore) PendingTasks(_ string) (int, error) {
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
		{"empty phase", &mockStore{}, "", ErrInvalidPhase},
		{"whitespace phase", &mockStore{}, "   ", ErrInvalidPhase},
		{"nil store", nil, "phase1", ErrPhaseStoreUnavailable},
		{"negative count", &mockStore{count: -1}, "phase1", ErrInvalidWorkCount},
		{"store error", &mockStore{err: storeErr}, "phase1", storeErr},
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
