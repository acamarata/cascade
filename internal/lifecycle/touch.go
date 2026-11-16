package lifecycle

import "errors"

// TouchpointKind represents the type of touchpoint in the lifecycle.
type TouchpointKind int

const (
	// PlanningKind represents a planning touchpoint.
	PlanningKind TouchpointKind = iota
	// CompleteKind represents a complete touchpoint.
	CompleteKind
)

// ChatState represents the state of a chat.
type ChatState int

const (
	// WaitingState represents a waiting chat. It is the zero value: every chat the plan opens starts in Waiting.
	WaitingState ChatState = iota
	// ArchivedState represents an archived chat.
	ArchivedState
)

const (
	planningChatName = "P5 Planning"
	completeChatName = "P5 Complete"
)

// TouchpointChat represents a chat associated with a touchpoint.
type TouchpointChat struct {
	Name         string
	Kind         TouchpointKind
	State        ChatState
	TemplateText string
	ProjectID    string
}

// OpenPlanningChat creates a new planning touchpoint chat for the given project.
// It returns an error if the projectID is empty.
func OpenPlanningChat(projectID, templateText string) (*TouchpointChat, error) {
	if projectID == "" {
		return nil, errors.New("projectID cannot be empty")
	}

	return &TouchpointChat{
		Name:         planningChatName,
		Kind:         PlanningKind,
		State:        WaitingState,
		TemplateText: templateText,
		ProjectID:    projectID,
	}, nil
}

// OpenCompleteChat creates a new complete touchpoint chat for the given project.
// The chat acts as a port; it embeds the ask card marking the chat as waiting.
// It returns an error if the projectID is empty.
func OpenCompleteChat(projectID, templateText string) (*TouchpointChat, error) {
	if projectID == "" {
		return nil, errors.New("projectID cannot be empty")
	}

	return &TouchpointChat{
		Name:         completeChatName,
		Kind:         CompleteKind,
		State:        WaitingState,
		TemplateText: templateText,
		ProjectID:    projectID,
	}, nil
}

// ConfirmPlan simulates the forge confirmation flow by transitioning
// a Planning chat in Waiting state to Archived state.
// It returns an error if chat is nil, if Kind is not Planning, or if State is already Archived.
func ConfirmPlan(chat *TouchpointChat) error {
	if chat == nil {
		return errors.New("chat cannot be nil")
	}
	if chat.Kind != PlanningKind {
		return errors.New("chat kind must be Planning")
	}
	if chat.State == ArchivedState {
		return errors.New("chat is already archived")
	}

	chat.State = ArchivedState
	return nil
}
