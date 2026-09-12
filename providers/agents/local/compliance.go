// Purpose: the local lane's R-16.10 CompliancePosture declaration and the
//   task-class-shaped capability set it advertises alongside it.
// Inputs: none at this layer — NewCompliancePosture and
//   baseCapabilities are fixed for every local-model instance; the one
//   variable part (whether `authoring` is present) is resolved by
//   Driver.AdvertisedCapabilities, not here.
// Outputs: provider.CompliancePosture; []Capability.
// Constraints: credential_sharing is CredentialSharingForbidden, the only
//   constructible value of provider.NewCompliancePosture's return type
//   (R-16.10) — there is no parameter through which this package could
//   ask for anything else.
// SPORT: providers.agents.local/ADD (P1-E30-W6-S62-T1).

package local

import "github.com/acamarata/cascade/pkg/provider"

// Capability names one task-class-shaped capability the local lane may
// advertise for a given model id. It is a package-local vocabulary,
// distinct from pkg/provider.Capabilities' six fixed R-14.88 tool-
// capability dimensions, because task-class authoring is not one of
// those six and this ticket adds no seventh (06-FORGE-SPEC.md §5 rule 11
// closes ModelProvider/AgentProvider's own five-plus-compliance shape).
type Capability string

// The four Capability members classify/extract/summarize/authoring name.
// classify/extract/summarize are always advertised; authoring is
// advertised only per model id, only when Driver.AdvertisedCapabilities
// resolves a passing qualification row for it (R-21.170).
const (
	CapabilityClassify  Capability = "classify"
	CapabilityExtract   Capability = "extract"
	CapabilitySummarize Capability = "summarize"
	CapabilityAuthoring Capability = "authoring"
)

// baseCapabilities are advertised for every model id regardless of
// qualification state.
var baseCapabilities = []Capability{CapabilityClassify, CapabilityExtract, CapabilitySummarize}

// NewCompliancePosture builds the local lane's fixed R-16.10 declaration:
// AuthModes ["native"] (no vendor auth — the lane dispatches in-process),
// no interactive entitlement, programmatic entitlement true (nothing
// blocks a caller from spawning a job the moment the lane is
// constructed), AutomationModes ["inline"], CredentialSharingForbidden
// (the only constructible value), and no multi-profile support (a local
// model instance is one profile).
func NewCompliancePosture() provider.CompliancePosture {
	return provider.NewCompliancePosture(
		[]string{"native"},
		false,
		true,
		[]string{"inline"},
		"local-inference",
		false,
	)
}

// capabilities builds the AgentProvider.Capabilities return value. The
// six R-14.88 tool-capability dimensions stay CapabilityUnknown: this
// ticket makes no claim about search/URLFetch/vision/tool-use/long-
// context/structured-output support, an honest "not yet probed" default
// distinct from an explicit refusal.
func capabilities() provider.Capabilities {
	return provider.Capabilities{CompliancePosture: NewCompliancePosture()}
}
