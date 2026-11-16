package policy

import "testing"

// TestHoldoutLeakClean verifies holdout leak detection, validation, and matching rules.
func TestHoldoutLeakClean(t *testing.T) {
	tests := []struct {
		name       string
		proposal   string
		holdoutIDs []string
		want       HoldoutResult
	}{
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckHoldoutLeak(tt.proposal, tt.holdoutIDs)
			if got != tt.want {
				t.Errorf("CheckHoldoutLeak() = %v, want %v", got, tt.want)
			}
		})
	}
}
