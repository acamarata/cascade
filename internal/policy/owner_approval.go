// Package policy (owner_approval.go): Purpose: the owner-approval record
// model and validation. An owner approval is a signed, non-expiring record
// that attests to owner consent for a specific grant. This file declares
// the types, validation logic, and the store interface. The store
// implementation is P2-GOV-82.
//
// Inputs: an OwnerApproval on validation; an OwnerApprovalStore for
// persistence operations.
//
// Outputs: OwnerApproval, Validate, ErrOwnerApprovalInvalid,
// OwnerApprovalStore.
//
// Constraints: FAIL CLOSED. Validate refuses an invalid GrantID, a zero
// IssuedAt, and an empty Attestation with ErrOwnerApprovalInvalid. The
// record has no expiry and no switch: no field named Exp or ExpiresAt, no
// bool field, no field named bypass.
package policy

import (
	"context"
	"errors"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// ErrOwnerApprovalInvalid is the comparison target for every refusal in
// this file: an invalid GrantID, a zero IssuedAt, or an empty Attestation.
var ErrOwnerApprovalInvalid = errors.New("owner-approval-invalid")

// OwnerApproval is a signed, non-expiring record that attests to owner
// consent for a specific grant. It has no expiry field and no switch field
// (no bool, no bypass).
type OwnerApproval struct {
	// GrantID identifies the grant this approval covers.
	GrantID cascade.ID `json:"grant_id"`
	// IssuedAt is when the approval was created.
	IssuedAt time.Time `json:"issued_at"`
	// Attestation is the cryptographic proof of owner consent.
	Attestation []byte `json:"attestation"`
}

// Validate refuses an approval that could not be trusted. It returns
// ErrOwnerApprovalInvalid for an invalid GrantID, a zero IssuedAt, or an
// empty Attestation.
func (a OwnerApproval) Validate() error {
	if a.GrantID == "" {
		return ErrOwnerApprovalInvalid
	}
	if a.IssuedAt.IsZero() {
		return ErrOwnerApprovalInvalid
	}
	if len(a.Attestation) == 0 {
		return ErrOwnerApprovalInvalid
	}
	return nil
}

// OwnerApprovalStore is the persistent owner-approval model. Load returns
// (record, true, nil) when a record is live, (zero, false, nil) when none
// exists, and (zero, false, error) on any failure. A caller that ignores
// the error sees no record.
type OwnerApprovalStore interface {
	// Load returns the live owner approval, if one exists.
	Load(ctx context.Context) (OwnerApproval, bool, error)
	// Save writes an owner approval.
	Save(ctx context.Context, a OwnerApproval) error
	// Revoke removes the owner approval.
	Revoke(ctx context.Context) error
}
