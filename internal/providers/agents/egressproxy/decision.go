package egressproxy

// Purpose: the journal vocabulary (Reason, Phase, Decision), the injected
// Journal and DialFunc seams, and the reason-to-status mapping.
//
// Constraints: the audit Kind set is closed, so this package journals
// through Journal; the spawn shim binds it to the redacting audit log as
// KindPolicyDecide, Actor "agent-driver:<driver-id>", Action
// "agent.egress.connect", Verdict allow|deny, Outcome <Reason>, Explain
// {destination, job_id, phase}.

import (
	"context"
	"net"
	"net/http"
)

// Reason is the outcome a Decision records.
type Reason string

// The closed Reason set.
const (
	ReasonAllowed                  Reason = "allowed"
	ReasonMethodNotConnect         Reason = "method-not-connect"
	ReasonMalformedDestination     Reason = "malformed-destination"
	ReasonDestinationNotAllowed    Reason = "destination-not-allowlisted"
	ReasonProxyAuthRequired        Reason = "proxy-auth-required"
	ReasonTunnelLimit              Reason = "tunnel-limit"
	ReasonJournalUnavailable       Reason = "journal-unavailable"
	ReasonDialFailed               Reason = "dial-failed"
	ReasonResolvedAddressForbidden Reason = "resolved-address-forbidden"
	ReasonConnected                Reason = "connected"
	ReasonClosed                   Reason = "closed"
)

// Phase says where in a connection's life a Decision was taken.
type Phase string

// The closed Phase set: decide is a refusal before any dial, intent is
// written before the dial, confirm after it.
const (
	PhaseDecide  Phase = "decide"
	PhaseIntent  Phase = "intent"
	PhaseConfirm Phase = "confirm"
)

// Decision is one journal row. Destination is empty whenever the request
// target was not a valid authority: a raw target is never journaled.
type Decision struct {
	DriverID    DriverID
	JobID       string
	Destination Destination
	Phase       Phase
	Allowed     bool
	Reason      Reason
}

// Journal records one Decision. An error on the intent row refuses the
// connection before any dial.
type Journal func(ctx context.Context, d Decision) error

// DialFunc opens the upstream connection for an admitted destination.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// statusFor maps a refusal Reason to its HTTP status. A resolved-address
// refusal is a policy refusal (403), not an upstream failure (502).
func statusFor(r Reason) int {
	switch r {
	case ReasonProxyAuthRequired:
		return http.StatusProxyAuthRequired
	case ReasonMalformedDestination:
		return http.StatusBadRequest
	case ReasonTunnelLimit:
		return http.StatusServiceUnavailable
	case ReasonDialFailed:
		return http.StatusBadGateway
	case ReasonMethodNotConnect, ReasonDestinationNotAllowed, ReasonJournalUnavailable,
		ReasonResolvedAddressForbidden, ReasonAllowed, ReasonConnected, ReasonClosed:
		return http.StatusForbidden
	default:
		return http.StatusForbidden
	}
}

// dialReason classifies a dial error. Only the package's own address-guard
// sentinel, matched by identity anywhere in the chain or join tree, is a
// forbidden address; errors.Is would match any error of the same Kind.
func dialReason(err error) Reason {
	if carriesSentinel(err) {
		return ReasonResolvedAddressForbidden
	}
	return ReasonDialFailed
}

// carriesSentinel walks single and multi Unwrap chains for
// errForbiddenAddress by pointer identity.
func carriesSentinel(err error) bool {
	if err == nil {
		return false
	}
	if err == errForbiddenAddress {
		return true
	}
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		return carriesSentinel(u.Unwrap())
	case interface{ Unwrap() []error }:
		for _, e := range u.Unwrap() {
			if carriesSentinel(e) {
				return true
			}
		}
	}
	return false
}
