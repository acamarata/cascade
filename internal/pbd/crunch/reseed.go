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
func InjectReseedRecap(payload CodePayload, reseedPending bool, history []HistoryItem) CodePayload {
	if !reseedPending {
		return payload
	}
	return payload
}
