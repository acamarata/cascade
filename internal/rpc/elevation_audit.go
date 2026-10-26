package rpc

// Purpose (this file): the audit seam of the elevation middleware.
// Inputs: one record per elevation step.
// Outputs: ElevationAuditRecord, ElevationAuditor, LocalCeremonyAudit.
// Constraints: a record carries the method and the params HASH, never the
//   params. The middleware appends in a fixed order and refuses when an
//   append fails, so a call whose grant could not be recorded never runs.
//   The auditor is injected: internal/rpc names no audit package.
// SPORT: internal/rpc ElevationAuditor (ADD) — P1-SEC-03.

import "context"

// The phases an elevation record can carry.
const (
	ElevationPhaseAttempt = "attempt"
	ElevationPhaseGrant   = "grant"
	ElevationPhaseDeny    = "deny"
)

// ElevationAuditRecord is one step of an elevated dispatch.
type ElevationAuditRecord struct {
	// Phase is attempt, grant or deny.
	Phase string
	// Method is the elevated verb.
	Method string
	// ParamsHash is ParamsHash of the params the step concerns.
	ParamsHash string
	// Reason is a short constant code for a deny; empty otherwise.
	Reason string
}

// ElevationAuditor records the steps of an elevated dispatch durably. An
// error means the step is not recorded, and the middleware then refuses.
type ElevationAuditor interface {
	AppendElevation(ctx context.Context, rec ElevationAuditRecord) error
}

// LocalCeremonyAudit is the auditor for an in-process ceremony: a CLI verb
// that verifies its own attestation to mint a one-shot proof for an
// operation that records its own outcome. It records nothing. A daemon or
// serve registry never uses it; they take the process's one audit log.
type LocalCeremonyAudit struct{}

// AppendElevation implements ElevationAuditor and records nothing.
func (LocalCeremonyAudit) AppendElevation(context.Context, ElevationAuditRecord) error { return nil }
