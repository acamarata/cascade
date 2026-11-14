// Package ownerauth provides core types for owner authentication and approval workflows.
package ownerauth

// OwnerCredential represents a stored credential for an owner's service account.
type OwnerCredential struct {
	// Service identifies the service or provider (e.g., "github", "aws").
	Service string

	// Account is the account identifier within the service.
	Account string

	// Token is the authentication token or secret for the account.
	Token string
}

// ApprovalRequest represents a request for owner approval of an action.
type ApprovalRequest struct {
	// SessionID uniquely identifies the approval session.
	SessionID string

	// Action describes the action requiring approval.
	Action string
}

// ApprovalResult represents the outcome of an approval decision.
type ApprovalResult struct {
	// Granted indicates whether the approval was granted.
	Granted bool

	// Reason provides context for the approval decision.
	Reason string
}
