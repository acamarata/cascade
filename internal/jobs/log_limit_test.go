package jobs

import (
	"testing"
)

// TestLogReadTypesExist verifies that the bounded log read contract types
// and sentinel errors are defined per contract:stub-p2-gnd-05.
func TestLogReadTypesExist(t *testing.T) {
	// Verify LogReadResult struct exists with correct fields
	var result LogReadResult
	result.Data = []byte("test")
	result.TrueSize = 100
	result.Cut = true

	if result.Data == nil {
		t.Error("LogReadResult.Data field not accessible")
	}
	if result.TrueSize != 100 {
		t.Error("LogReadResult.TrueSize field not accessible")
	}
	if !result.Cut {
		t.Error("LogReadResult.Cut field not accessible")
	}

	// Verify sentinel errors exist and are non-nil
	if ErrInvalidMax == nil {
		t.Error("ErrInvalidMax sentinel error not defined")
	}
	if ErrUnknownJobID == nil {
		t.Error("ErrUnknownJobID sentinel error not defined")
	}
}
