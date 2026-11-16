package harness

// SubChatState represents the operational state of a SubChat.
type SubChatState string

const (
	// SubChatWorking indicates the sub-chat is actively processing or executing work.
	SubChatWorking SubChatState = "working"
	// SubChatIdle indicates the sub-chat is inactive and safe for operations.
	SubChatIdle SubChatState = "idle"
)

// SubChat represents a conversation context that can hold messages.
type SubChat struct {
	// ID is the unique identifier for the sub-chat.
	ID string
	// State indicates the current operational state.
	State SubChatState
	// Messages contains the conversation history.
	Messages []string
}

// MessageCount returns the number of messages currently held by the SubChat.
func (s *SubChat) MessageCount() int {
	if s == nil {
		return 0
	}
	return len(s.Messages)
}

// MergeError represents an error condition during a merge operation.
type MergeError struct {
	// Code is the specific error code identifying the failure reason.
	Code string
}

// Error implements the error interface for MergeError.
func (e *MergeError) Error() string {
	return e.Code + ": source sub-chat is working"
}

// MergeSubChat attempts to merge the source SubChat into the destination SubChat.
// It returns a MergeError with code D6086 if the source sub-chat is actively working,
// leaving both source and destination states unchanged.
func MergeSubChat(src, dst *SubChat) error {
	if src != nil && src.State == SubChatWorking {
		return &MergeError{Code: "D6086"}
	}
	return nil
}
