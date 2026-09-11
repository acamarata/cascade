// Purpose: the model.execute door's sentinel errors, each wrapping exactly
//   one frozen pkg/cascade Kind. ErrSensitivityLocalOnly is declared
//   NOWHERE in this package (R-21.217): the sentinels below are the whole
//   set.
// Inputs: none.
// Outputs: none.
// Constraints: reuses the frozen 14-kind taxonomy only; never invents a
//   kind. CONTRACT DEVIATION (recorded, not papered over): the ticket
//   text says ErrSecurityPipelineNotReady wraps "the frozen precondition
//   kind", but pkg/cascade's Kind enum (pkg/cascade/kinds.go) is frozen at
//   fourteen members and has no KindPrecondition - see the journal for
//   both sides quoted. This file wraps KindUnavailable instead: a caller
//   seeing ErrSecurityPipelineNotReady is told a dependency (the daemon
//   composition root's collaborator wiring) is not yet in place, which is
//   the same "retry once the dependency exists" semantic KindUnavailable
//   already carries elsewhere in this repo, and never silently invents a
//   fifteenth kind.
// SPORT: conductor.execute/ADD (P1-E11-W3-S22-T1).

package conductor

import "github.com/acamarata/cascade/pkg/cascade"

// ErrInvalidRequest is returned for a malformed ModelRequest, an
// empty/unknown task_class, an unresolvable sensitivity tier, or any
// classifier error (R-21.208 terminal deny - never an approval/elevation
// path).
var ErrInvalidRequest = cascade.New(cascade.KindInvalidInput, "conductor: invalid model request")

// ErrNoLane is returned when Router.Select finds no candidate lane -
// including the local-only case where no controller-local lane exists.
// Execute never falls through to an external provider on this error.
var ErrNoLane = cascade.New(cascade.KindUnavailable, "conductor: no candidate lane")

// ErrSensitivityViolation is returned when the sensitivity gate empties the
// candidate set: a restricted or local-only request that no remaining lane
// may serve.
var ErrSensitivityViolation = cascade.New(cascade.KindPolicyDenied, "conductor: sensitivity gate refused every candidate lane")

// ErrSecurityPipelineNotReady is returned by Execute and ExecuteStream,
// with ZERO provider calls made, until all six R-21.206 collaborators are
// installed. See this file's header comment for the KindUnavailable
// substitution.
var ErrSecurityPipelineNotReady = cascade.New(cascade.KindUnavailable, "conductor: security pipeline not ready")

// ErrEgressSubstitutionFailed is returned when the EgressSubstitutor
// (sensitivity.go, P1-E11-W3-S22-T3) refuses or fails to substitute an
// outbound payload. The payload never reaches the provider on this error.
var ErrEgressSubstitutionFailed = cascade.New(cascade.KindInternal, "conductor: egress substitution refused the outbound payload")

// ErrConstructionFailed is returned by NewExecutor when a required
// collaborator (the router, the provider resolver, or any of the six
// R-21.206 collaborators) is nil. Construction never succeeds without a
// real audit broker (R-21.206 A).
var ErrConstructionFailed = cascade.New(cascade.KindInvalidInput, "conductor: executor construction requires every collaborator")
