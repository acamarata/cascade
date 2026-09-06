// Purpose: CompliancePosture (R-16.10): the vendor-terms self-declaration
//   every ModelProvider (and, per the same ruling, AD/S-61.T1 AgentProvider)
//   publishes through its Capabilities descriptor, so that "FOSS Cascade is
//   capacity orchestration over legitimately authenticated profiles, never
//   a quota bypass" is a structural property a caller can inspect, not only
//   a sentence in a doc.
// Inputs: none at this layer - NewCompliancePosture takes the caller's
//   declared fields; construction is the only place credential_sharing is
//   decided, and it always decides "forbidden".
// Outputs: none.
// Constraints: credential_sharing is always forbidden - CredentialSharing
//   is not a caller-settable field of NewCompliancePosture's signature, so
//   there is no argument through which a driver could ask for anything
//   else. Validate fails closed on any CompliancePosture value whose
//   CredentialSharing has been set to something other than
//   CredentialSharingForbidden by a struct literal outside this
//   constructor.
// SPORT: pkg.provider.modelprovider-contract/ADD (P1-E10-W3-S19-T1).

package provider

import "github.com/acamarata/cascade/pkg/cascade"

// CredentialSharingPolicy names the credential-sharing posture a
// CompliancePosture declares. It has exactly one valid member,
// CredentialSharingForbidden - R-16.10 makes credential sharing always
// forbidden, never a per-driver choice.
type CredentialSharingPolicy string

// CredentialSharingForbidden is CredentialSharingPolicy's one valid member.
const CredentialSharingForbidden CredentialSharingPolicy = "forbidden"

// CompliancePosture is the R-16.10 vendor-terms self-declaration a
// ModelProvider publishes via Capabilities.CompliancePosture.
type CompliancePosture struct {
	// AuthModes lists the authentication modes this lane supports (e.g.
	// "api-key", "oauth", "session-cookie"). Kept as a plain string slice
	// rather than a closed enum, since the set of vendor auth modes is
	// open-ended and owned by each driver, not by this contract.
	AuthModes []string
	// InteractiveEntitlement reports whether this lane's credentials were
	// obtained through an interactive, human-present login flow.
	InteractiveEntitlement bool
	// ProgrammaticEntitlement reports whether this lane's credentials were
	// obtained through a programmatic (API key, service account) grant.
	ProgrammaticEntitlement bool
	// AutomationModes lists the automation modes this lane's vendor terms
	// permit (e.g. "batch", "agent", "scheduled"). Open-ended for the same
	// reason as AuthModes.
	AutomationModes []string
	// CredentialSharing is always CredentialSharingForbidden.
	// NewCompliancePosture is the only constructor and never accepts a
	// different value; Validate refuses any other value found on a
	// hand-built CompliancePosture.
	CredentialSharing CredentialSharingPolicy
	// Pacing free-text-describes the rate-limiting cadence this lane's
	// vendor terms impose (e.g. "steady", "burst-then-cooldown"). Kept as
	// a description rather than a numeric rate: the exact shape of a
	// vendor's pacing rule is driver-specific and not this contract's to
	// normalize.
	Pacing string
	// MultiProfileEnabled reports whether more than one authenticated
	// profile may be active for this lane concurrently.
	MultiProfileEnabled bool
}

// NewCompliancePosture builds a CompliancePosture from the caller's declared
// fields, unconditionally setting CredentialSharing to
// CredentialSharingForbidden - there is no parameter through which a driver
// can request anything else.
func NewCompliancePosture(
	authModes []string,
	interactiveEntitlement bool,
	programmaticEntitlement bool,
	automationModes []string,
	pacing string,
	multiProfileEnabled bool,
) CompliancePosture {
	return CompliancePosture{
		AuthModes:               authModes,
		InteractiveEntitlement:  interactiveEntitlement,
		ProgrammaticEntitlement: programmaticEntitlement,
		AutomationModes:         automationModes,
		CredentialSharing:       CredentialSharingForbidden,
		Pacing:                  pacing,
		MultiProfileEnabled:     multiProfileEnabled,
	}
}

// Validate fails closed: a CompliancePosture whose CredentialSharing is not
// exactly CredentialSharingForbidden is invalid, however it was
// constructed. A caller that skips NewCompliancePosture and builds a struct
// literal directly cannot silently ship a permissive value past this check.
func (p CompliancePosture) Validate() error {
	if p.CredentialSharing != CredentialSharingForbidden {
		return cascade.Newf(cascade.KindInvalidInput,
			"provider: compliance_posture.credential_sharing must be %q, got %q",
			CredentialSharingForbidden, p.CredentialSharing)
	}
	return nil
}
