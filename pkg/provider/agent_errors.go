package provider

// Purpose: the AD/S-61.T1 AgentProvider sentinel errors. Each wraps
//
//	exactly one frozen pkg/cascade Kind, following the same convention
//	compliance.go and every other pkg/provider file already uses (never
//	errors.New — see the contradiction note in this ticket's journal).
//
// Inputs: none — these are values, not behavior.
// Outputs: none.
// Constraints: every sentinel below is a *cascade.Error, so
//
//	errors.Is(err, provider.ErrJobNotFound) works regardless of which
//	constructor produced err, per *cascade.Error.Is's Kind-only
//	comparison; a caller that needs the specific sentinel identity (rather
//	than just the Kind) can still compare with errors.Is against these
//	exact values since they are the Kind's own zero-message instance.
//
// SPORT: pkg.provider.AgentProvider/EXTEND (P1-E30-W6-S61-T1).

import "github.com/acamarata/cascade/pkg/cascade"

// The AD/S-61.T1 AgentProvider sentinel errors, each a distinct
// *cascade.Error wrapping one frozen taxonomy Kind.
var (
	// ErrEntitlement is returned by Spawn when the lane's
	// CompliancePosture reports no programmatic entitlement.
	ErrEntitlement = cascade.New(cascade.KindPolicyDenied, "provider: no programmatic entitlement for this lane")
	// ErrJobNotFound is returned by Status/Cancel/Collect/Artifacts for an
	// unknown job id.
	ErrJobNotFound = cascade.New(cascade.KindNotFound, "provider: agent job not found")
	// ErrPlatform is returned when a driver cannot run on the host
	// platform (a tier-2 refusal).
	ErrPlatform = cascade.New(cascade.KindUnsupported, "provider: agent driver unsupported on this platform")
	// ErrHarnessIncompatible is the one uniform RUNTIME refusal when
	// protocol negotiation finds no version in the intersection of the
	// adapter's range and the harness's range (R-21.158) — never a
	// build-time-only refusal.
	ErrHarnessIncompatible = cascade.New(cascade.KindConflict, "provider: no compatible protocol version in range")
	// ErrDataClassDowngrade is returned by WithDataClass when a caller
	// tries to lower a resolved data class (R-21.143).
	ErrDataClassDowngrade = cascade.New(cascade.KindInvalidInput, "provider: data class cannot be lowered")
	// ErrOutOfOrderEvent is returned by event ordering enforcement on a
	// sequence regression (R-21.158).
	ErrOutOfOrderEvent = cascade.New(cascade.KindIntegrity, "provider: agent event sequence regression")
	// ErrPreSpawnSecretFound is returned by Spawn (via PreSpawnScan) when
	// the pre-spawn worktree scan hits (R-21.177); the spawn is aborted
	// fail-closed and no child is started.
	ErrPreSpawnSecretFound = cascade.New(cascade.KindCapabilityDenied, "provider: pre-spawn secret scan found a credential-shaped value")
	// ErrCancelUnconfirmed is returned by Cancel when process-group exit
	// is NOT confirmed within the cancel deadline (R-21.174/R-21.140); the
	// job stays AgentRunCancelling.
	ErrCancelUnconfirmed = cascade.New(cascade.KindTimeout, "provider: agent cancel deadline expired without confirmed exit")
)
