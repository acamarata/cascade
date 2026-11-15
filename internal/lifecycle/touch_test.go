package lifecycle

import "testing"

// TestTouchpointTypesExist verifies that the touchpoint domain types
// and constants are properly defined.
func TestTouchpointTypesExist(t *testing.T) {
	// Verify TouchpointKind values exist
	_ = TouchpointKind(0)
	planningKind := PlanningKind
	completeKind := CompleteKind
	if planningKind == completeKind {
		t.Error("Planning and Complete kinds should be distinct")
	}

	// Verify ChatState values exist
	_ = ChatState(0)
	waitingState := WaitingState
	archivedState := ArchivedState
	if waitingState == archivedState {
		t.Error("Waiting and Archived states should be distinct")
	}

	// Verify chat name constants exist
	if planningChatName == "" {
		t.Error("planningChatName constant should not be empty")
	}
	if completeChatName == "" {
		t.Error("completeChatName constant should not be empty")
	}

	// Verify TouchpointChat struct has required fields
	chat := TouchpointChat{
		Name:         "Test Chat",
		Kind:         PlanningKind,
		State:        WaitingState,
		TemplateText: "Template",
		ProjectID:    "proj-123",
	}

	if chat.Name != "Test Chat" {
		t.Error("TouchpointChat Name field not working")
	}
	if chat.Kind != PlanningKind {
		t.Error("TouchpointChat Kind field not working")
	}
	if chat.State != WaitingState {
		t.Error("TouchpointChat State field not working")
	}
	if chat.TemplateText != "Template" {
		t.Error("TouchpointChat TemplateText field not working")
	}
	if chat.ProjectID != "proj-123" {
		t.Error("TouchpointChat ProjectID field not working")
	}
}

// TestPlanningChatOpensAtRTP verifies that OpenPlanningChat creates
// a properly configured touchpoint chat.
func TestPlanningChatOpensAtRTP(t *testing.T) {
	// Test successful creation
	projectID := "proj-123"
	templateText := "Planning template text"

	chat, err := OpenPlanningChat(projectID, templateText)
	if err != nil {
		t.Fatalf("OpenPlanningChat returned unexpected error: %v", err)
	}

	if chat == nil {
		t.Fatal("OpenPlanningChat returned nil chat")
	}

	// Verify chat name
	if chat.Name != "P5 Planning" {
		t.Errorf("Expected chat name 'P5 Planning', got '%s'", chat.Name)
	}

	// Verify state is Waiting
	if chat.State != WaitingState {
		t.Errorf("Expected chat state WaitingState, got %v", chat.State)
	}

	// Verify template text is stored as first message
	if chat.TemplateText != templateText {
		t.Errorf("Expected template text '%s', got '%s'", templateText, chat.TemplateText)
	}

	// Verify project ID
	if chat.ProjectID != projectID {
		t.Errorf("Expected project ID '%s', got '%s'", projectID, chat.ProjectID)
	}

	// Test empty projectID error case
	chat, err = OpenPlanningChat("", templateText)
	if err == nil {
		t.Error("Expected error for empty projectID, got nil")
	}
	if chat != nil {
		t.Error("Expected nil chat when projectID is empty")
	}
}

// TestCompleteChatOpensAtRTD verifies that OpenCompleteChat creates
// a properly configured touchpoint chat.
func TestCompleteChatOpensAtRTD(t *testing.T) {
	// Test successful creation
	projectID := "proj-456"
	templateText := "Complete template text"

	chat, err := OpenCompleteChat(projectID, templateText)
	if err != nil {
		t.Fatalf("OpenCompleteChat returned unexpected error: %v", err)
	}

	if chat == nil {
		t.Fatal("OpenCompleteChat returned nil chat")
	}

	// Verify chat name
	if chat.Name != "P5 Complete" {
		t.Errorf("Expected chat name 'P5 Complete', got '%s'", chat.Name)
	}

	// Verify state is Waiting (ask card embeds this)
	if chat.State != WaitingState {
		t.Errorf("Expected chat state WaitingState, got %v", chat.State)
	}

	// Verify template text is stored as first message
	if chat.TemplateText != templateText {
		t.Errorf("Expected template text '%s', got '%s'", templateText, chat.TemplateText)
	}

	// Verify project ID
	if chat.ProjectID != projectID {
		t.Errorf("Expected project ID '%s', got '%s'", projectID, chat.ProjectID)
	}

	// Verify kind
	if chat.Kind != CompleteKind {
		t.Errorf("Expected kind CompleteKind, got %v", chat.Kind)
	}

	// Test empty projectID error case
	chat, err = OpenCompleteChat("", templateText)
	if err == nil {
		t.Error("Expected error for empty projectID, got nil")
	}
	if chat != nil {
		t.Error("Expected nil chat when projectID is empty")
	}
}
