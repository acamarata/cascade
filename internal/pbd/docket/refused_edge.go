package docket

// validateTicketIDs checks that the given slice contains no empty or duplicate IDs.
// A nil or empty slice is considered valid.
// Returns ErrInvalidSnapshot if validation fails.
func validateTicketIDs(ids []string) error {
	if ids == nil {
		return nil
	}

	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" {
			return ErrInvalidSnapshot
		}
		if seen[id] {
			return ErrInvalidSnapshot
		}
		seen[id] = true
	}

	return nil
}

// copyTicketIDs creates a defensive copy of the given slice.
// Returns nil if the input is nil.
func copyTicketIDs(ids []string) []string {
	if ids == nil {
		return nil
	}

	result := make([]string, len(ids))
	copy(result, ids)
	return result
}

// copyRefusedMarks creates a defensive copy of the given map.
// Returns nil if the input is nil.
func copyRefusedMarks(marks map[string]bool) map[string]bool {
	if marks == nil {
		return nil
	}

	result := make(map[string]bool, len(marks))
	for k, v := range marks {
		result[k] = v
	}
	return result
}
