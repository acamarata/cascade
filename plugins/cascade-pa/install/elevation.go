package install

// Purpose (this file): the seam over 06-FORGE-SPEC.md Sec5.14's local
//   elevation flow (nonce -> user-session helper local-auth ->
//   hardware-backed signed attestation) that a process-tier or
//   grant-expanding `plugin add` requires. The real flow lives behind
//   internal/elevation and internal/plugins' composition root, which this
//   package cannot import (R-16.62, R-14.69); a composition root injects
//   the real implementation via SetElevator, matching events.go's
//   SetEventBus and plugin.go's SetConversations precedents exactly.
// Inputs: an ElevationRequest naming the candidate and its RuntimeMode.
// Outputs: an ElevationResult reporting whether the flow was approved, or
//   a non-nil error when the flow itself could not run.
// Constraints: fail-closed -- an unconfigured or erroring Elevator NEVER
//   reads as approved; Flow (flow.go) treats err != nil the same as
//   Approved == false. Imports pkg/plugin and pkg/cascade only (Art.10.2).
// SPORT: plugins/cascade-pa/install (ADD) -- P1-E24-W5-S50-T4.

import (
	"context"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/plugin"
)

// AddOutcome mirrors internal/plugins.AddOutcome's three terminal states.
// A distinct type, not a re-export: cascade-pa cannot import internal/
// (R-16.62, R-14.69), so an Installer implementation translates the real
// AddOutcome into this one at the composition-root boundary.
type AddOutcome int

const (
	// AddOutcomeInstalled reports a fresh or version-changed install.
	AddOutcomeInstalled AddOutcome = iota
	// AddOutcomeAlreadyInstalled is the Sec5.9 idempotent case.
	AddOutcomeAlreadyInstalled
	// AddOutcomeElevationRequired mirrors 06 Sec5.14; no write happened.
	AddOutcomeElevationRequired
)

// AddRequest is the Installer's input. Witness proves a completed local
// elevation flow approved THIS exact install (see ElevationWitness); the
// zero Witness means "no elevation was performed" -- a first attempt, or a
// non-elevated candidate. An Installer must refuse an elevated write (a
// candidate needing elevation) when Witness.Valid() is false: the caller's
// own assertion that elevation happened is never sufficient on its own (06
// Sec5.14 -- the chat confirm alone can never satisfy an elevated add).
type AddRequest struct {
	Candidate plugin.Candidate
	Witness   ElevationWitness
}

// ElevationWitness is proof that the real Sec5.14 local elevation flow
// (nonce -> user-session helper local-auth -> hardware-backed signed
// attestation) approved this exact install. Its one field is unexported
// and its ONLY constructor is VerifyElevationWitness, which verifies the
// caller's Attestation against an AttestationVerifier before minting
// anything -- there is no composite literal, no setter, and (round-2
// rework, T0 decision D2) no constructor that accepts a bare string:
// a caller cannot mint a valid witness by asserting a request id, only by
// producing an attestation that a real verifier accepts, mirroring
// plugin.VerifiedIndex's own "proof, not assertion" design
// (pkg/plugin/intents.go).
type ElevationWitness struct {
	// requestID is the attestation's own request id (rpc.Attestation.RequestID
	// in the real flow) -- a fresh, unguessable identifier minted only at
	// the moment a real attestation succeeded, never a caller-supplied
	// value.
	requestID string
}

// Valid reports whether w carries a non-empty request id. The zero
// ElevationWitness{} -- an AddRequest built without ever calling Elevate,
// or a caller that merely asserted elevation happened -- is never valid.
func (w ElevationWitness) Valid() bool { return w.requestID != "" }

// Attestation is the install-package-local shape of a completed local
// elevation attestation, presented to VerifyElevationWitness. It is
// deliberately NOT internal/rpc.Attestation: this package cannot import
// internal/rpc (Art.10.2, R-16.62, R-14.69), so the real Elevator
// (internal/plugins/cascadepa_install_elevator.go) converts its own
// rpc.Attestation into this shape at the boundary.
type Attestation struct {
	// RequestID is the attestation's own fresh, unguessable identifier.
	RequestID string
	// ActionHash, Nonce, PubkeyFingerprint, IssuedUnix, ExpUnix and SigB64
	// are the remaining signed-envelope fields a real AttestationVerifier
	// needs to independently verify the attestation (signature, replay,
	// expiry) -- carried here so the verification itself happens on the
	// composition-root side of the boundary, never inside this package.
	ActionHash        string
	Nonce             string
	PubkeyFingerprint string
	IssuedUnix        int64
	ExpUnix           int64
	SigB64            string
}

// AttestationVerifier verifies a completed local-elevation attestation
// before a witness for it may be minted. The real implementation
// (internal/plugins/cascadepa_install_elevator.go's
// elevationAttestationVerifier) wraps the SAME rpc.ElevationMiddleware /
// rpc.NonceLedger / rpc.VerifyAttestation gate the elevator itself used to
// issue the challenge -- this package cannot import internal/rpc directly
// (Art.10.2), so this interface is the seam.
type AttestationVerifier interface {
	// Verify checks att against the real trust/nonce state and returns
	// nil only for a genuine, unexpired, unreplayed attestation signed by
	// an enrolled key. Any non-nil error -- invalid signature, expired,
	// already-consumed nonce (replay), unenrolled key -- refuses.
	Verify(ctx context.Context, att Attestation) error
}

// VerifyElevationWitness is the ONLY way to mint a valid ElevationWitness.
// It is the single verification point: verifier.Verify(ctx, att) IS the
// real cryptographic check (the same gate call the elevator already made
// to obtain approval), never a second, independent recheck of the same
// attestation. A nil verifier, a Verify error, or an attestation with no
// request id all fail closed -- the zero, invalid ElevationWitness and a
// non-nil error, never a witness whose Valid() reports true.
func VerifyElevationWitness(ctx context.Context, verifier AttestationVerifier, att Attestation) (ElevationWitness, error) {
	if verifier == nil {
		return ElevationWitness{}, cascade.New(cascade.KindUnavailable,
			"cascade-pa install: no attestation verifier configured; a witness cannot be minted without one")
	}
	if err := verifier.Verify(ctx, att); err != nil {
		return ElevationWitness{}, err
	}
	if att.RequestID == "" {
		return ElevationWitness{}, cascade.New(cascade.KindIntegrity,
			"cascade-pa install: verified attestation carries no request id")
	}
	return ElevationWitness{requestID: att.RequestID}, nil
}

// AddResult is the Installer's answer.
type AddResult struct {
	Outcome AddOutcome
}

// Installer calls the daemon's `plugin.add` RPC (through the pkg/plugin
// SDK host-services client a composition root hands the real
// implementation) -- the identical path `cascade plugin add` uses, never
// a second, simulated install branch.
type Installer interface {
	Add(ctx context.Context, req AddRequest) (AddResult, error)
}

// ElevationRequest names the candidate whose add path returned
// AddOutcomeElevationRequired.
type ElevationRequest struct {
	PluginID string
	Runtime  plugin.RuntimeMode
}

// ElevationResult is the local elevation flow's outcome. Approved is true
// only when the hardware-backed signed attestation succeeded; the chat
// confirm the operator already gave in the Confirm phase never sets this
// on its own (06 Sec5.14 -- a second, independent gate). Witness is set
// only when Approved is true, and is the one value Flow (flow.go) is able
// to carry into the retried Installer.Add call as proof this happened.
type ElevationResult struct {
	Approved bool
	Witness  ElevationWitness
}

// Elevator performs the local elevation flow for req. Elevate must not
// return Approved: true unless the real Sec5.14 flow (nonce -> helper
// local-auth -> signed attestation) actually completed.
type Elevator interface {
	Elevate(ctx context.Context, req ElevationRequest) (ElevationResult, error)
}

// errElevatorUnconfigured is returned by the default Elevator. As with
// errEventBusUnconfigured, this is the correct behavior of a binary that
// has not called SetElevator -- never a fabricated approval.
var errElevatorUnconfigured = cascade.New(cascade.KindUnavailable,
	"cascade-pa install: no elevation helper wired into this session; the composition root "+
		"has not called install.SetElevator")

type unconfiguredElevator struct{}

func (unconfiguredElevator) Elevate(context.Context, ElevationRequest) (ElevationResult, error) {
	return ElevationResult{}, errElevatorUnconfigured
}

// elevatorState guards the package-level Elevator seam.
var elevatorState struct {
	mu sync.RWMutex
	e  Elevator
}

// SetElevator injects the real Elevator implementation. Tests call it
// directly to inject a fake, or pass nil to reset to the unconfigured
// (fail-closed) default.
func SetElevator(e Elevator) {
	elevatorState.mu.Lock()
	elevatorState.e = e
	elevatorState.mu.Unlock()
}

// activeElevator returns the configured Elevator, or
// unconfiguredElevator{} if SetElevator has never been called (or was
// last called with nil).
func activeElevator() Elevator {
	elevatorState.mu.RLock()
	defer elevatorState.mu.RUnlock()
	if elevatorState.e == nil {
		return unconfiguredElevator{}
	}
	return elevatorState.e
}
