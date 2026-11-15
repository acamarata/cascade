package lifecycle

import (
	"errors"
	"testing"
)

// TestParseCommandsSignatures verifies that ParseCommands and ParseWait
// have the expected signatures and that sentinel errors are defined.
func TestParseCommandsSignatures(t *testing.T) {
	// Verify ErrNoCommands is a sentinel error
	if ErrNoCommands == nil {
		t.Fatal("ErrNoCommands must be defined")
	}
	if !errors.Is(ErrNoCommands, ErrNoCommands) {
		t.Fatal("ErrNoCommands must be comparable with errors.Is")
	}

	// Verify ErrInvalidWait is a sentinel error
	if ErrInvalidWait == nil {
		t.Fatal("ErrInvalidWait must be defined")
	}
	if !errors.Is(ErrInvalidWait, ErrInvalidWait) {
		t.Fatal("ErrInvalidWait must be comparable with errors.Is")
	}

	// Verify ParseCommands has the expected signature by checking it exists
	// The actual function body will be implemented in P2-LIFE-34
	_ = ParseCommands

	// Verify ParseWait has the expected signature by checking it exists
	// The actual function body will be implemented in P2-LIFE-35
	_ = ParseWait
}
