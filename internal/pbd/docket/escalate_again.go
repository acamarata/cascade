// Package docket provides ticket escalation utilities.
package docket

// escalateTicket represents a support ticket with escalation tracking state.
type escalateTicket struct {
	ResetCount int
	TriedLanes []string
}

const maxResets = 4

// escalateAgain resets an escalated ticket only while an untried big lane
// remains and t.ResetCount < 4. On reset it increments ResetCount and returns
// true. It returns false when ResetCount is 4 or when every big lane is
// already in t.TriedLanes.
func escalateAgain(t *escalateTicket, bigLanes []string) bool {
	if t.ResetCount >= maxResets {
		return false
	}
	for _, bl := range bigLanes {
		untried := true
		for _, tried := range t.TriedLanes {
			if tried == bl {
				untried = false
				break
			}
		}
		if untried {
			t.ResetCount++
			return true
		}
	}
	return false
}
