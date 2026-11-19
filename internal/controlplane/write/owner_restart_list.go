package write

import "sort"

// List returns a filtered and sorted slice of audit events matching the given kind.
// Events are sorted by At ascending, with equal timestamps preserving insertion order.
// Mutating the returned slice does not affect storage.
// A nil store, unknown kind, or no matches yields a non-nil empty slice.
func (s *AuditStore) List(kind string) []AuditEvent {
	if s == nil {
		return []AuditEvent{}
	}
	result := make([]AuditEvent, 0)
	for _, e := range s.events {
		if e.Kind == kind {
			result = append(result, e)
		}
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].At.Before(result[j].At)
	})
	return result
}
