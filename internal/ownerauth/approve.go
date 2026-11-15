// Package ownerauth provides core types for owner authentication and approval workflows.
package ownerauth

import (
	"errors"
)

// ErrInvalidSession is returned when the approval request has an empty session ID.
var ErrInvalidSession = errors.New("invalid session")

// ErrUnauthorized is returned when no owner credential is provided.
var ErrUnauthorized = errors.New("unauthorized")

// Approve evaluates an approval request against the provided owner credential.
// It returns ErrInvalidSession if req.SessionID is empty, ErrUnauthorized if cred is nil,
// and rejects agent sessions by returning a result with Granted set to false.
func Approve(req *ApprovalRequest, cred *OwnerCredential) (*ApprovalResult, error) {
	if req.SessionID == "" {
		return nil, ErrInvalidSession
	}
	if cred == nil {
		return nil, ErrUnauthorized
	}

	return &ApprovalResult{
		Granted: false,
		Reason:  "agent sessions cannot approve",
	}, nil
}
