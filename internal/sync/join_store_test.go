package sync

import (
	"context"
	"testing"
)

// TestJoinStoreInterfaceCompliance verifies that the Store interface exists
// with the correct Join method signature, and that all required types and
// sentinel errors compile.
func TestJoinStoreInterfaceCompliance(t *testing.T) {
	// Verify sentinel errors are defined and non-nil.
	errs := []struct {
		name string
		err  error
	}{
		{"ErrJoinVersionTooLow", ErrJoinVersionTooLow},
		{"ErrJoinDuplicate", ErrJoinDuplicate},
		{"ErrJoinMalformed", ErrJoinMalformed},
		{"ErrJoinOverBound", ErrJoinOverBound},
	}
	for _, tc := range errs {
		if tc.err == nil {
			t.Errorf("%s is nil", tc.name)
		}
	}

	// Verify protocol version constant exists.
	if JoinProtocolVersion < 1 {
		t.Errorf("JoinProtocolVersion = %d, want >= 1", JoinProtocolVersion)
	}

	// Verify types compile by constructing zero values.
	_ = JournalState{}
	_ = PeerEntry{}
	_ = JoinRequest{}
	_ = JoinResult{}

	// Verify Store interface has Join method with correct signature
	// by assigning a compliant implementation.
	var s Store = &complianceStore{}
	_, _ = s.Join(context.Background(), JoinRequest{})
}

// complianceStore is a minimal implementation to prove the Store interface
// compiles with the expected Join signature.
type complianceStore struct{}

func (complianceStore) Join(_ context.Context, _ JoinRequest) (JoinResult, error) {
	return JoinResult{}, nil
}
