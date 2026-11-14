// Package standards defines quality policy types and landing rules.
package standards

// PhaseMode represents the phase enforcement mode for a policy.
type PhaseMode string

const (
	// Strict is the phase mode that returns an error on gate failure.
	Strict PhaseMode = "strict"

	// Forged is the phase mode used after ship date that warns on gate failure.
	Forged PhaseMode = "forged"
)

// Policy holds the phase mode and CAPP_QUALITY environment value.
type Policy struct {
	// Mode is the current phase mode, either Strict or Forged.
	Mode PhaseMode

	// Quality is the CAPP_QUALITY env value, typically "warn" or "enforce".
	Quality string
}

// Allows determines whether a ticket may land based on the policy and gate result.
// If gateResult is nil or points to false, Allows returns false meaning no landing.
// When CAPP_QUALITY is not "enforce", it defaults to warn behavior.
func (p *Policy) Allows(_ string, _ string, gateResult *bool) bool {
	if gateResult == nil {
		return false
	}
	if !*gateResult {
		return false
	}
	return true
}
