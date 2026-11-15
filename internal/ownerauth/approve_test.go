package ownerauth

import (
	"testing"
)

func TestApproveRejectsAgentSession(t *testing.T) {
	req := &ApprovalRequest{
		SessionID: "test-session",
		Action:    "test-action",
	}
	cred := &OwnerCredential{
		Service: "test-service",
		Account: "test-account",
		Token:   "test-token",
	}

	result, err := Approve(req, cred)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Granted {
		t.Error("expected approval to be rejected for agent session")
	}
	if result.Reason != "agent sessions cannot approve" {
		t.Errorf("expected reason 'agent sessions cannot approve', got %q", result.Reason)
	}
}

func TestApproveInvalidSession(t *testing.T) {
	req := &ApprovalRequest{
		SessionID: "",
		Action:    "test-action",
	}
	cred := &OwnerCredential{
		Service: "test-service",
		Account: "test-account",
		Token:   "test-token",
	}

	_, err := Approve(req, cred)
	if err != ErrInvalidSession {
		t.Errorf("expected ErrInvalidSession, got %v", err)
	}
}

func TestApproveUnauthorized(t *testing.T) {
	req := &ApprovalRequest{
		SessionID: "test-session",
		Action:    "test-action",
	}

	_, err := Approve(req, nil)
	if err != ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}
