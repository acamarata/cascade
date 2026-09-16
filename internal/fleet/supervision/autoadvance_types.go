// Purpose: the tier-1 auto-advance vocabulary — the six verdicts the
//
//	ceiling can produce, and the facts it decides from.
//
// WHY A VERDICT AND NOT A BOOL. Every refusal here has a DIFFERENT
//
//	remedy: raise the profile's ceiling, mark the instruction source
//	trusted, elevate deliberately, enable auto-advance at all, or fix
//	whatever made the decision unreadable. A bool would route all five to
//	the same "not approved" and leave an operator guessing which one they
//	hit. The verdict is also what the audit record and the attention item
//	carry, so it is the thing a person reads later.
//
// Inputs: an action descriptor, the policy engine's own decision, the
//
//	classified risk level, and the instruction source's trust tag.
//
// Outputs: one Verdict.
// Constraints: the zero Verdict is NOT a permissive value. An
//
//	uninitialised verdict reads as fail_closed_denied, so a path that
//	forgot to set one cannot be mistaken for an approval.
//
// SPORT: internal/fleet/supervision:autoadvance-types (ADD) — P1-E18-W4-S39-T2.
package supervision

import (
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/corpus"
)

// Verdict is one auto-advance decision.
//
// It is a string rather than an integer because it is written to audit
// records and attention items that outlive this build: a renumbered
// constant would silently change what an old record means, while a renamed
// string fails loudly at the reader.
type Verdict string

// The six verdicts. Exactly one is an approval.
const (
	// VerdictFailClosedDenied is the ZERO value, deliberately: an
	// unreadable classification, a panic, or a path that never set a
	// verdict all resolve to the most restrictive answer.
	VerdictFailClosedDenied Verdict = ""
	// VerdictAutoApproved is the only approval: L0/L1, profile permits,
	// trusted source, non-elevated verb.
	VerdictAutoApproved Verdict = "auto_approved"
	// VerdictCeilingRefused means the risk level is above what auto-advance
	// may ever reach, or above what this profile permits.
	VerdictCeilingRefused Verdict = "ceiling_refused"
	// VerdictUntrustedRefused means the instruction came from an untrusted
	// source, which never auto-advances at any level.
	VerdictUntrustedRefused Verdict = "untrusted_refused"
	// VerdictElevatedRefused means the action is an elevated verb, which
	// requires same-turn human authorization and cannot be pre-approved.
	VerdictElevatedRefused Verdict = "elevated_refused"
	// VerdictProfileDisabled means auto-advance is switched off for this
	// profile — the default state of an unconfigured install.
	VerdictProfileDisabled Verdict = "profile_disabled"
)

// String renders the verdict, mapping the zero value to its real name so a
// log line never shows an empty field.
func (v Verdict) String() string {
	if v == VerdictFailClosedDenied {
		return "fail_closed_denied"
	}
	return string(v)
}

// Approved reports whether v permits the action to proceed without a human
// turn. Exactly one verdict does.
func (v Verdict) Approved() bool { return v == VerdictAutoApproved }

// ActionDescriptor is the action under evaluation, in the terms this
// decision needs: what it is, and whether it is an elevated verb.
//
// It carries no command text. The classifier has already run by the time
// this stage does, and re-reading the command here would be a second
// classification that could disagree with the first (R-21.236 resolves the
// rung exactly once).
type ActionDescriptor struct {
	// Ref names the action for the audit record and the attention item.
	Ref string
	// Origin is where the action came from (hook, scheduler, …).
	Origin string
	// Verb is the RPC method name, when the action is one. Empty for an
	// action that is not a verb call.
	Verb string
	// Elevated reports whether Verb is on the §5.14 elevated list. It is
	// supplied by the caller, which already consulted the canonical table
	// during policy evaluation, rather than re-derived here from a second
	// copy of it.
	Elevated bool
}

// PolicyDecision is the engine's own answer, carried forward so this stage
// can only ever NARROW it.
//
// AutoAdvance is the engine's ceiling result — it already folds in the
// profile's per-level slot. This stage adds the conditions the engine does
// not see: the instruction's trust tag and the profile's master switch.
type PolicyDecision struct {
	// Verdict is what the policy engine decided.
	Verdict policy.Verdict
	// AutoAdvance is the engine's own per-level ceiling result.
	AutoAdvance bool
	// Level is the risk level the engine actually evaluated.
	Level policy.RiskLevel
}

// TrustTag is the instruction source's propagated trust, as the retrieval
// corpus resolves it. It is that package's type rather than a local copy:
// a second trust vocabulary is a second place for "untrusted" to mean
// something slightly different.
type TrustTag = corpus.TrustLevel
