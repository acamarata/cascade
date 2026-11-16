package crunch

import (
	"errors"
	"testing"
)

// checkSymbolsTestCase defines a test case for TestCheckSymbols.
type checkSymbolsTestCase struct {
	name        string
	provided    map[string]bool
	ticket      string
	expectErr   bool
	expectedErr error
}

// checkSymbolsTestCases contains all test cases for TestCheckSymbols.
var checkSymbolsTestCases = []checkSymbolsTestCase{
	{
		name:        "unresolved contract returns ErrUnresolvedContract",
		provided:    map[string]bool{},
		ticket:      "This ticket references @CONTRACT_A and @CONTRACT_B.",
		expectErr:   true,
		expectedErr: ErrUnresolvedContract,
	},
	{
		name: "all contracts provided returns nil",
		provided: map[string]bool{
			"CONTRACT_A": true,
			"CONTRACT_B": true,
		},
		ticket:      "This ticket references @CONTRACT_A and @CONTRACT_B.",
		expectErr:   false,
		expectedErr: nil,
	},
	{
		name: "symbols checked only for provided contracts",
		provided: map[string]bool{
			"CONTRACT_A": true,
		},
		ticket:      "This ticket references @CONTRACT_A and @CONTRACT_B.",
		expectErr:   true,
		expectedErr: ErrUnresolvedContract,
	},
	{
		name:        "no contract references returns nil",
		provided:    map[string]bool{},
		ticket:      "This ticket has no contract references.",
		expectErr:   false,
		expectedErr: nil,
	},
	{
		name: "partial contracts provided returns error",
		provided: map[string]bool{
			"CONTRACT_A": true,
		},
		ticket:      "References @CONTRACT_A, @CONTRACT_B, and @CONTRACT_C.",
		expectErr:   true,
		expectedErr: ErrUnresolvedContract,
	},
}

// TestCheckSymbols verifies that contract symbols are checked correctly.
func TestCheckSymbols(t *testing.T) {
	for _, tc := range checkSymbolsTestCases {
		t.Run(tc.name, func(t *testing.T) {
			sc := &SymbolChecker{Provided: tc.provided}
			err := sc.CheckSymbols(tc.ticket)
			if tc.expectErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, tc.expectedErr) {
					t.Fatalf("expected %v, got %v", tc.expectedErr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
			}
		})
	}
}

// TestResetRetries verifies that ResetRetries clears contract retry state.
func TestResetRetries(t *testing.T) {
	RecordRetry("CONTRACT_A")
	RecordRetry("CONTRACT_A")
	if GetRetryCount("CONTRACT_A") != 2 {
		t.Fatalf("expected 2 retries before reset, got %d", GetRetryCount("CONTRACT_A"))
	}

	ResetRetries()

	if GetRetryCount("CONTRACT_A") != 0 {
		t.Fatalf("expected 0 retries after reset, got %d", GetRetryCount("CONTRACT_A"))
	}
}
