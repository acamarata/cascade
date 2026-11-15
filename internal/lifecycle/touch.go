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
	// OpenState represents an open chat.
	OpenState ChatState = iota
	// WaitingState represents a waiting chat.
	WaitingState
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
