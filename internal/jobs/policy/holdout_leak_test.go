package policy

import "testing"

// holdoutLeakCase defines a single test scenario for holdout leak detection.
type holdoutLeakCase struct {
	name       string
	proposal   string
	holdoutIDs []string
	want       HoldoutResult
}

// holdoutLeakCases contains all test scenarios for holdout leak detection.
var holdoutLeakCases = []holdoutLeakCase{
	{
		name:       "parser with [H-104] returns approved",
		proposal:   "This proposal references [H-104]",
		holdoutIDs: []string{"H-104"},
		want:       HoldoutResult{Status: StatusApproved, Reason: "", Matched: ""},
	},
	{
		name:       "empty proposal with valid holdout set approves",
		proposal:   "",
		holdoutIDs: []string{"H-104"},
		want:       HoldoutResult{Status: StatusApproved, Reason: "", Matched: ""},
	},
	{
		name:       "invalid holdout set rejects even with empty proposal",
		proposal:   "",
		holdoutIDs: []string{"invalid id!"},
		want:       HoldoutResult{Status: StatusRejected, Reason: ReasonInvalidHoldoutIDs, Matched: ""},
	},
	{
		name:       "case variant h-104 does not match H-104",
		proposal:   "Reference to h-104",
		holdoutIDs: []string{"H-104"},
		want:       HoldoutResult{Status: StatusApproved, Reason: "", Matched: ""},
	},
	{
		name:       "malformed ID rejects",
		proposal:   "test",
		holdoutIDs: []string{"bad id"},
		want:       HoldoutResult{Status: StatusRejected, Reason: ReasonInvalidHoldoutIDs, Matched: ""},
	},
	{
		name:       "empty ID rejects",
		proposal:   "test",
		holdoutIDs: []string{""},
		want:       HoldoutResult{Status: StatusRejected, Reason: ReasonInvalidHoldoutIDs, Matched: ""},
	},
	{
		name:       "duplicate IDs reject",
		proposal:   "test",
		holdoutIDs: []string{"H-104", "H-104"},
		want:       HoldoutResult{Status: StatusRejected, Reason: ReasonInvalidHoldoutIDs, Matched: ""},
	},
}

// TestHoldoutLeakClean verifies holdout leak detection, validation, and matching rules.
func TestHoldoutLeakClean(t *testing.T) {
	for _, tt := range holdoutLeakCases {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckHoldoutLeak(tt.proposal, tt.holdoutIDs)
			if got != tt.want {
				t.Errorf("CheckHoldoutLeak() = %v, want %v", got, tt.want)
			}
		})
	}
}
