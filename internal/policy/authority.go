// Package policy (authority.go): Purpose: define authority request and grant
// types for authority-boundary policy checks.
//
// Inputs: an AuthorityRequest describes an action requiring authority
// approval.
// Outputs: an AuthorityGrant represents the approval granted.
package policy

// AuthorityRequest represents an action requiring authority approval.
// It must not contain any bool fields or any field named "bypass" to satisfy
// the reflection contract for authority boundaries.
type AuthorityRequest struct {
	// Action is the name of the action being requested.
	Action string `json:"action"`
	// ParamsDigest is a hash of the parameters for the action.
	ParamsDigest string `json:"params_digest"`
}

// AuthorityGrant represents an approval granted for an AuthorityRequest.
// It must not contain any bool fields or any field named "bypass" to satisfy
// the reflection contract for authority boundaries.
type AuthorityGrant struct {
	// RequestID is the identifier of the request being granted.
	RequestID string `json:"request_id"`
	// GrantedBy is the subject who granted the approval.
	GrantedBy string `json:"granted_by"`
}
