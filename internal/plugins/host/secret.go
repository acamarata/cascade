// Purpose: CheckSecretRef, the credential-custody boundary every
//
//	host_secret_ref call transits: it hands a plugin an opaque reference
//	to a vault-derived value, never the raw value itself.
//
// Inputs: the vault key name a plugin asked to reference.
// Outputs: an opaque SecretHandle on success; a denied, audited error
//
//	otherwise. The raw secret value never appears in either return path.
//
// Constraints: fail closed — a nil SecretBroker and a broker error both
//
//	deny. This package never calls an elevated, human-facing "Get"-style
//	verb: SecretBroker's one method is named Reference and its return
//	type is this package's own non-string SecretHandle (06-FORGE-SPEC
//	§5.21; R-21.211 names this same shape as secrets.Handle, attributed
//	to H/S-15.T3/T4 and I/S-18.T2 — the ticket journal quotes why this
//	package cannot depend on that type, or on internal/secrets.Broker,
//	directly).
//
// SPORT: internal/plugins/host secret-ref-boundary (ADD) — P1-E15-W4-S31-T4.

package host

import (
	"context"

	"github.com/acamarata/cascade/pkg/cascade"
)

// secretCallType names this check in audit entries and denial messages.
const secretCallType = "secret_ref"

// SecretHandle is this package's own opaque, non-string view of a
// vault-derived reference: a value a plugin may hold and pass back to a
// later host call, never the credential itself. It deliberately exposes
// no method that returns the underlying reference as a string or []byte;
// the only way to compare two handles is Equal, and the only way to test
// for the zero value is IsZero.
type SecretHandle struct {
	ref string
}

// IsZero reports whether h is the unset zero value (no reference held).
func (h SecretHandle) IsZero() bool { return h.ref == "" }

// Equal reports whether h and other reference the same vault entry,
// without exposing what that reference is.
func (h SecretHandle) Equal(other SecretHandle) bool { return h.ref == other.ref }

// String never renders the reference: it is deliberately opaque so a
// SecretHandle logged, fmt.Sprintf'd, or included in an error message by
// mistake cannot leak the reference (and, transitively, cannot leak
// anything that could be mistaken for the secret it points to).
func (h SecretHandle) String() string {
	if h.IsZero() {
		return "secret-handle(unset)"
	}
	return "secret-handle(set)"
}

// newSecretHandle wraps ref as an opaque handle. Unexported: only this
// package's SecretBroker adapters (outside this package) construct one,
// through the SecretBroker interface's return value — never a plugin, and
// never from a string a plugin supplied.
func newSecretHandle(ref string) SecretHandle { return SecretHandle{ref: ref} }

// SecretBroker is the seam onto the vault broker's non-elevated reference
// verb. A composition root outside internal/plugins/** (see capability.go's
// doc comment) adapts it to the real credential store; Reference must
// return an opaque reference, never the raw stored value, and must itself
// enforce which keys the calling plugin may reference — this package
// trusts the seam's own authorization, the same way CheckGeneric trusts
// PolicyEngine's.
type SecretBroker interface {
	Reference(ctx context.Context, key string) (SecretHandle, error)
}

// ErrNoSecretBroker reports a CheckSecretRef call on an enforcer built
// with no SecretBroker. Missing infrastructure denies; it never falls
// back to a "no broker configured, allow" default.
var ErrNoSecretBroker = cascade.New(cascade.KindCapabilityDenied, "host_secret_ref: no secret broker configured")

// CheckSecretRef resolves key to an opaque SecretHandle through e's
// SecretBroker. A nil broker, an empty key, and a broker error all deny
// (fail closed); the raw secret value is never read, held, or returned by
// this function — SecretBroker.Reference's own return type makes that
// impossible to get wrong here. A successful reference is audited
// (06-FORGE-SPEC §5.21: "every granted secret-ref access is also
// logged"); the audited reason names the key, never the resolved
// reference or the underlying value.
func (e *HostBoundaryEnforcer) CheckSecretRef(ctx context.Context, key string) (SecretHandle, error) {
	if key == "" {
		return SecretHandle{}, e.Deny(ctx, secretCallType, cascade.New(cascade.KindInvalidInput, "host_secret_ref: empty key"))
	}
	if e.broker == nil {
		return SecretHandle{}, e.Deny(ctx, secretCallType, ErrNoSecretBroker)
	}
	handle, err := e.broker.Reference(ctx, key)
	if err != nil {
		return SecretHandle{}, e.Deny(ctx, secretCallType, cascade.Wrapf(cascade.KindCapabilityDenied, err, "host_secret_ref: %q", key))
	}
	e.allow(ctx, secretCallType, "reference resolved for key "+key)
	return handle, nil
}
