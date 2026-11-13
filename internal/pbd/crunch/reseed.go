// Package crunch provides reseed recap injection utilities.
package crunch

// HistoryItem represents a single message in a conversation history.
type HistoryItem struct {
	Role string
	Text string
}

// ReseedRecap is a slice of history items used as a recap for reseeding.
type ReseedRecap []HistoryItem

// CodePayload holds the text and optional reseed recap for a code payload.
type CodePayload struct {
	Text        string
	ReseedRecap ReseedRecap
}

// M1286RecapLimit is the maximum number of history items included in a reseed recap.
const M1286RecapLimit = 3

// InjectReseedRecap returns the payload unchanged when reseedPending is false,
// preserving any preexisting recap. Caller owns code.reseedPending.
// When reseedPending is true, it replaces the recap with a fresh copy of the
// latest min(len(history), M1286RecapLimit) items in their original order,
// or clears the recap to nil when history is empty or nil.
func InjectReseedRecap(payload CodePayload, reseedPending bool, history []HistoryItem) CodePayload {
	if !reseedPending {
		return payload
	}

	limit := M1286RecapLimit
	if len(history) < limit {
		limit = len(history)
	}

	var recap ReseedRecap
	if limit > 0 {
		start := len(history) - limit
		recap = make(ReseedRecap, limit)
		for i := 0; i < limit; i++ {
			recap[i] = history[start+i]
		}
	}

	payload.ReseedRecap = recap
	return payload
}
