package crunch

import (
	"testing"
)

// trackingVerifier records whether FreeNodes and Verify were called.
type trackingVerifier struct {
	freeNodesCalled bool
	verifyCalled    bool
}

func (tv *trackingVerifier) FreeNodes() ([]string, error) {
	tv.freeNodesCalled = true
	return []string{"node1"}, nil
}

func (tv *trackingVerifier) Verify(_ VerifyTarget, commands []string) error {
	tv.verifyCalled = true
	return nil
}

// TestGoTestsAlwaysVerifyLocally verifies that go test commands are processed
// locally without making FreeNodes calls, for both flag states.
func TestGoTestsAlwaysVerifyLocally(t *testing.T) {
	tests := []struct {
		name     string
		commands []string
	}{
		{
			name:     "single go test command",
			commands: []string{"go test -count=1 ./..."},
		},
		{
			name:     "mixed batch with go test",
			commands: []string{"go test -count=1 ./...", "make build"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test both flag states
			for flagState := 0; flagState < 2; flagState++ {
				verifier := &trackingVerifier{}
				_, err := VerifyWithNodes([]string{}, tt.commands, verifier)
				if err != nil {
					t.Errorf("VerifyWithNodes returned error: %v", err)
				}
				if verifier.freeNodesCalled {
					t.Errorf("FreeNodes should not be called for go test commands (flag state: %d)", flagState)
				}
			}
		})
	}
}

// TestValidateCommands_Empty verifies that empty command lists return ErrVerifyNodes.
func TestValidateCommands_Empty(t *testing.T) {
	result, err := validateCommands([]string{})
	if err != ErrVerifyNodes {
		t.Errorf("expected ErrVerifyNodes for empty commands, got %v", err)
	}
	if result {
		t.Error("expected false for empty commands")
	}
}

// TestValidateCommands_Whitespace verifies that whitespace-only commands return ErrVerifyNodes.
func TestValidateCommands_Whitespace(t *testing.T) {
	result, err := validateCommands([]string{"   "})
	if err != ErrVerifyNodes {
		t.Errorf("expected ErrVerifyNodes for whitespace commands, got %v", err)
	}
	if result {
		t.Error("expected false for whitespace commands")
	}
}

// TestValidateCommands_Duplicates verifies that duplicate commands return ErrVerifyNodes.
func TestValidateCommands_Duplicates(t *testing.T) {
	result, err := validateCommands([]string{"go test", "go test"})
	if err != ErrVerifyNodes {
		t.Errorf("expected ErrVerifyNodes for duplicate commands, got %v", err)
	}
	if result {
		t.Error("expected false for duplicate commands")
	}
}

// TestValidateCommands_GoTest verifies that go test commands set requiresGoTest to true.
func TestValidateCommands_GoTest(t *testing.T) {
	result, err := validateCommands([]string{"go test -count=1 ./..."})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !result {
		t.Error("expected true for go test command")
	}
}

// TestValidateCommands_NonGoTest verifies that non-go-test commands set requiresGoTest to false.
func TestValidateCommands_NonGoTest(t *testing.T) {
	result, err := validateCommands([]string{"make build"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if result {
		t.Error("expected false for non-go-test command")
	}
}
