// Package policy provides holdout leak detection for proposals.
package policy

import (
	"regexp"
	"strings"
)

// StatusApproved indicates the proposal passed holdout checks.
const StatusApproved = "Approved"

// StatusRejected indicates the proposal failed holdout checks.
const StatusRejected = "Rejected"

// ReasonHoldoutLeak indicates a holdout ID was found in the proposal.
const ReasonHoldoutLeak = "HOLDOUT_ID_LEAK"

// ReasonInvalidHoldoutIDs indicates the holdout ID set is malformed or invalid.
const ReasonInvalidHoldoutIDs = "INVALID_HOLDOUT_IDS"

// HoldoutResult represents the outcome of a holdout leak check.
type HoldoutResult struct {
	// Status is either StatusApproved or StatusRejected.
	Status string
	// Reason explains why a proposal was rejected; empty if approved.
	Reason string
	// Matched is the holdout ID that leaked into the proposal; empty if none.
	Matched string
}

var holdoutIDRegex = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validateHoldoutIDs checks that all holdout IDs are valid, non-empty, and unique.
// Returns false if any ID is malformed, empty, or duplicated.
func validateHoldoutIDs(holdoutIDs []string) bool {
	seen := make(map[string]struct{}, len(holdoutIDs))
	for _, id := range holdoutIDs {
		if id == "" {
			return false
		}
		if !holdoutIDRegex.MatchString(id) {
			return false
		}
		if _, exists := seen[id]; exists {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

// isLeak checks if the holdout ID leaks into the proposal.
// An ID enclosed in square brackets (e.g. "[H-104]") is considered a reference and not a leak.
func isLeak(proposal, id string) bool {
	idx := 0
	for {
		pos := strings.Index(proposal[idx:], id)
		if pos == -1 {
			return false
		}
		absPos := idx + pos
		enclosed := absPos > 0 &&
			proposal[absPos-1] == '[' &&
			absPos+len(id) < len(proposal) &&
			proposal[absPos+len(id)] == ']'
		if !enclosed {
			return true
		}
		idx = absPos + len(id)
	}
}

// CheckHoldoutLeak checks if a proposal leaks any holdout IDs.
// It validates the holdout ID set first, then searches the proposal for exact matches.
// Returns a HoldoutResult with the appropriate status, reason, and matched ID.
func CheckHoldoutLeak(proposal string, holdoutIDs []string) HoldoutResult {
	if !validateHoldoutIDs(holdoutIDs) {
		return HoldoutResult{
			Status:  StatusRejected,
			Reason:  ReasonInvalidHoldoutIDs,
			Matched: "",
		}
	}

	for _, id := range holdoutIDs {
		if isLeak(proposal, id) {
			return HoldoutResult{
				Status:  StatusRejected,
				Reason:  ReasonHoldoutLeak,
				Matched: id,
			}
		}
	}

	return HoldoutResult{
		Status:  StatusApproved,
		Reason:  "",
		Matched: "",
	}
}
