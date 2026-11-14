package docket

// Replan returns a defensive copy of snapshot with TicketIDs replaced
// in exact plan order. RefusedMarks are copied without deleting or
// changing any existing key/value, even for dropped IDs. New plan
// IDs get no map entry. Invalid snapshot or plan returns an empty
// Snapshot and ErrInvalidSnapshot.
func Replan(snapshot Snapshot, plan Plan) (Snapshot, error) {
	seenSnap := make(map[string]bool, len(snapshot.TicketIDs))
	for _, id := range snapshot.TicketIDs {
		if id == "" || seenSnap[id] {
			return Snapshot{}, ErrInvalidSnapshot
		}
		seenSnap[id] = true
	}
	for k := range snapshot.RefusedMarks {
		if k == "" {
			return Snapshot{}, ErrInvalidSnapshot
		}
	}

	seenPlan := make(map[string]bool, len(plan.TicketIDs))
	for _, id := range plan.TicketIDs {
		if id == "" || seenPlan[id] {
			return Snapshot{}, ErrInvalidSnapshot
		}
		seenPlan[id] = true
	}

	marks := make(map[string]bool, len(snapshot.RefusedMarks))
	for k, v := range snapshot.RefusedMarks {
		marks[k] = v
	}

	newIDs := make([]string, len(plan.TicketIDs))
	copy(newIDs, plan.TicketIDs)

	return Snapshot{
		TicketIDs:    newIDs,
		RefusedMarks: marks,
	}, nil
}
