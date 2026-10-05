package egress

import (
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// EgressClass names one outbound destination kind. The name repeats the
// package on purpose: every call site outside this package reads
// `egress.EgressClass`, and at a security boundary an unambiguous name at
// the call site is worth more than a shorter one at the declaration.
//
// key, and it is also the identifier a registrant's ticket owns: two
// subsystems that write to different destinations never share a class,
// because a shared class would let one subsystem's policy admit the
// other's content.
//
//nolint:revive // the stutter is deliberate; see the comment above. It is the registry
type EgressClass string

// SensitivityTier is the classification the CALLER declares for the
// content it is about to send. It is never derived from the bytes and
// never carried on a context: a []byte declares nothing about itself, and
// a value that can be forgotten by dropping a context is not a
// classification. It is an alias of provider.SensitivityTier, the one
// canonical tier type (contract:sensitivity-tier), so there is no
// conversion table between the two and no second parse.
type SensitivityTier = provider.SensitivityTier

// The four tiers. There is deliberately no permissive zero value: the zero
// value is TierRestricted, so a caller that forgets to classify gets the
// strict answer rather than the convenient one.
const (
	// TierLocalOnly is content that must not leave the machine at all
	// unless the destination class was registered to admit it.
	TierLocalOnly = provider.SensitivityLocalOnly
	// TierRestricted is content admitted only by classes whose
	// registrant set AllowRestricted. It is the zero value.
	TierRestricted = provider.SensitivityRestricted
	// TierInternal is content admitted by any enabled class.
	TierInternal = provider.SensitivityInternal
	// TierPublic is content admitted always.
	TierPublic = provider.SensitivityPublic
)

// ResolveTier returns the tier the policy actually applies. A declared
// tier resolves to itself; a value above TierPublic is not a tier this
// build can reason about and resolves to TierLocalOnly, the narrowest
// member. (A function, not a method: methods cannot be declared on an
// alias of another package's type.)
func ResolveTier(t SensitivityTier) SensitivityTier {
	if !t.Valid() {
		return TierLocalOnly
	}
	return t
}

// InterceptConfig is what a registrant declares about one class.
type InterceptConfig struct {
	// Enabled reports whether the class may carry bytes at all. A
	// registered class with Enabled false refuses every call with
	// ErrClassDisabled; it is registered so that the refusal names a
	// known class rather than reading as a typo.
	Enabled bool
	// AllowRestricted admits TierRestricted content on this class.
	AllowRestricted bool
	// AllowLocalOnly admits TierLocalOnly content on this class. Default
	// false: only a registrant that can prove the destination never
	// leaves the machine sets it.
	AllowLocalOnly bool
	// AllowedTiers, when non-empty, narrows the class further: the
	// resolved tier must also appear here. It is an additional gate, never
	// a widening one, so a tier the matrix refuses stays refused however
	// this list reads.
	AllowedTiers []SensitivityTier
	// Owner names the ticket responsible for the class. Diagnostic only;
	// it appears in refusal messages so an operator can find the code that
	// declared the policy they just hit.
	Owner string
}

// The sentinel errors this package returns. Each is wrapped in a
// pkg/cascade Error carrying a frozen taxonomy Kind, so callers can match
// on the sentinel with errors.Is and on the kind with cascade.HasKind.
var (
	// ErrUnknownClass reports a class no registrant registered. It is
	// returned BEFORE any content is examined.
	ErrUnknownClass = errors.New("egress: unknown class")
	// ErrClassDisabled reports a registered class that is not enabled.
	ErrClassDisabled = errors.New("egress: class disabled")
	// ErrSensitivityViolation reports content whose tier the class does
	// not admit.
	ErrSensitivityViolation = errors.New("egress: sensitivity tier not admitted by class")
	// ErrCapabilityRequired reports a call made without a Capability the
	// registry issued.
	ErrCapabilityRequired = errors.New("egress: a registry-issued capability is required")
	// ErrDuplicateClass reports a second registration of one class.
	ErrDuplicateClass = errors.New("egress: class already registered")
	// ErrNoVault reports an engine built without a value source. The
	// exact-value pass cannot run without one, and an engine that skips a
	// pass is an engine that misses secrets.
	ErrNoVault = errors.New("egress: no vault value source configured")
)

// unknownClass builds the ErrUnknownClass return.
func unknownClass(class EgressClass) error {
	return cascade.Wrapf(cascade.KindNotFound, ErrUnknownClass,
		"egress: class %q is not registered; every outbound path registers its class", string(class))
}

// disabledClass builds the ErrClassDisabled return.
func disabledClass(class EgressClass, owner string) error {
	return cascade.Wrapf(cascade.KindPolicyDenied, ErrClassDisabled,
		"egress: class %q (owner %s) is disabled; no bytes may egress on it", string(class), owner)
}
