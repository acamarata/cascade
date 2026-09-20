// Purpose: the thread-privacy gate - FILTER 0, ahead of every lane filter
//   Router.Select already runs. A conversation thread carries a
//   §5.16 SensitivityTier (internal/conversation's privacy_mode); this
//   file decides, for every (privacy_mode x lane_type) pair, whether that
//   thread's work may reach that lane, and refuses with
//   ErrSensitivityViolation naming the thread, the mode and the lane.
// Inputs: the thread projection carried on the request context
//   (ContextWithThreadPrivacy, set by the conversation dispatch path) and
//   the same registry-derived lane facts FILTER 2 reads.
// Outputs: the surviving candidates, an explain flag recording how many
//   lanes the mode removed, or ErrSensitivityViolation.
// Constraints: FAIL CLOSED in both directions. An unrecognised tier is
//   restricted (§5.16's own rule for an unset mode), and a lane whose
//   locality cannot be COMPUTED is refused by every mode but public -
//   "unresolved metadata must not weaken restrictions". A context with no
//   thread on it leaves the candidate set exactly as it found it: absence
//   of a thread is not absence of a restriction, because req.Sensitivity
//   still drives FILTER 2 and still resolves to restricted when invalid.
//
// WHY THE THREAD ARRIVES ON THE CONTEXT. provider.ModelRequest is the
// frozen S-22.T1 SDK type and carries no thread identity; pkg/provider is
// outside this ticket's files_scope, and inventing a field on a frozen
// envelope to name a caller's storage object would be the wrong repair
// anyway. The thread is request-scoped metadata, which is what a context
// value is for. Router.Select's signature is unchanged.
//
// CONTRACT DEVIATION (recorded, not papered over). The contract's
// restricted row reads "refuse bridge lanes and any external-API lane not
// explicitly permitted for restricted content". Neither half is
// computable here: the real provider registry's closed vocabularies
// (DriverKind, AuthType, AccountKind, Tier, HealthStatus, CapacityBucket)
// contain no bridge member and no per-lane permit field, which is the
// same deviation filterSensitivity already records for its own
// restricted/internal/public legs. A bridge lane is by construction not
// controller-local, so the local-only row refuses it; the restricted row
// refuses it only when its locality is unresolved. Making the restricted
// row refuse EVERY external lane was rejected: restricted is the §5.16
// default for an unset mode, so that reading refuses every external lane
// for every request that names no tier, which is a routing change this
// ticket is not licensed to make. What would make the cell direct is a
// registry trust-tier field, and R-14.60 strikes inventing one.
//
// SPORT: conductor.privacy/ADD (P1-E20-W5-S44-T2).

package conductor

import (
	"context"
	"fmt"
	"net/url"

	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// ThreadPrivacy is the routing-relevant projection of one conversation
// thread: which thread, and the privacy_mode it was created under.
//
// It is a projection rather than the conversation Thread itself because
// internal/conductor must not import internal/conversation to route - the
// router needs two fields, and taking the whole record would make lane
// selection depend on the conversation schema.
type ThreadPrivacy struct {
	// ThreadID names the thread in the refusal. A refusal that does not
	// say WHICH conversation was blocked leaves an operator with a policy
	// error and no way to find the thread that caused it.
	ThreadID string
	// Mode is the thread's privacy_mode. An invalid value (including the
	// zero value of a tier that was never set) is read as restricted.
	Mode provider.SensitivityTier
}

// EffectiveMode is Mode with §5.16's fail-closed default applied.
func (tp ThreadPrivacy) EffectiveMode() provider.SensitivityTier {
	if !tp.Mode.Valid() {
		return provider.SensitivityRestricted
	}
	return tp.Mode
}

// LaneType is the closed set of lane kinds the privacy table decides over.
//
// Exactly three members, because exactly three are COMPUTABLE from the
// real registry: a lane is served by a provider whose BaseURL names this
// machine, names another machine, or cannot be resolved at all. See this
// file's CONTRACT DEVIATION for the fourth kind the contract names and
// the registry does not carry.
type LaneType uint8

// The three closed LaneType members. LaneUnresolved is the ZERO VALUE on
// purpose: a lane type nobody computed must be the one the table treats
// most strictly, never the one it waves through.
const (
	// LaneUnresolved is a lane whose provider has no BaseURL, or one that
	// does not parse. Nothing here can say which machine it reaches.
	LaneUnresolved LaneType = iota
	// LaneControllerLocal is a lane on this machine: a loopback host or a
	// unix socket.
	LaneControllerLocal
	// LaneExternalAPI is a lane that reaches a resolvable host elsewhere.
	LaneExternalAPI
)

// String names the lane type for the explain trail and the refusal.
func (t LaneType) String() string {
	switch t {
	case LaneControllerLocal:
		return "controller-local"
	case LaneExternalAPI:
		return "external-api"
	case LaneUnresolved:
		return "unresolved"
	default:
		// A LaneType nobody declared reads as the strict one, for the same
		// reason LaneUnresolved is the zero value.
		return "unresolved"
	}
}

// ClassifyLane derives a lane's type from its provider record.
//
// It reuses computedLocalityIsLocal - FILTER 2's own predicate - rather
// than re-deriving locality, so the two gates cannot come to opposite
// conclusions about the same lane. The unresolved case is the one
// computedLocalityIsLocal collapses into "not local": it returns false
// both for a provider on another machine and for one with no usable
// BaseURL at all, and those are different facts to a privacy table.
func ClassifyLane(p provider.ProviderInfo) LaneType {
	if computedLocalityIsLocal(p) {
		return LaneControllerLocal
	}
	if !resolvableHost(p) {
		return LaneUnresolved
	}
	return LaneExternalAPI
}

// resolvableHost reports whether a provider record names a host at all.
//
// This is the fact computedLocalityIsLocal discards. It answers false for
// the same two inputs a privacy table must not treat as ordinary external
// lanes: an empty BaseURL, and one url.Parse rejects or that carries no
// host. net/url only - nothing here opens a socket, which is why the
// egress-firewall-gated "net" is not imported (the same reasoning
// computedLocalityIsLocal records for its dotted-prefix loopback check).
func resolvableHost(p provider.ProviderInfo) bool {
	if p.BaseURL == "" {
		return false
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return false
	}
	return u.Hostname() != ""
}

// PrivacyAllows is the (privacy_mode x lane_type) table itself. Every one
// of the twelve cells is decided here, and privacy_test.go asserts each.
//
//	mode \ lane   | controller-local | external-api | unresolved
//	local-only    | allow            | REFUSE       | REFUSE
//	restricted    | allow            | allow (*)    | REFUSE
//	internal      | allow            | allow        | REFUSE
//	public        | allow            | allow        | allow
//
// (*) the CONTRACT DEVIATION in this file's header.
//
// A tier outside the four is read as restricted, matching EffectiveMode
// and §5.16, so the table is total over provider.SensitivityTier's whole
// uint8 range rather than over its four named values.
func PrivacyAllows(mode provider.SensitivityTier, lane LaneType) bool {
	if !mode.Valid() {
		mode = provider.SensitivityRestricted
	}
	switch lane {
	case LaneControllerLocal:
		// Work that never leaves this machine is what every mode permits;
		// local-only is the mode that permits nothing else.
		return true
	case LaneExternalAPI:
		return mode != provider.SensitivityLocalOnly
	case LaneUnresolved:
		// Only content already declared public may ride a lane whose
		// destination this build cannot name.
		return mode == provider.SensitivityPublic
	default:
		// An undeclared LaneType is treated as unresolved, never as one of
		// the two kinds the table lets more modes through.
		return mode == provider.SensitivityPublic
	}
}

// threadPrivacyKey is the unexported context key. Unexported and of a
// private type so no other package can collide with it or forge a thread
// onto a context this package will trust.
type threadPrivacyKey struct{}

// ContextWithThreadPrivacy attaches tp to ctx for the duration of one
// routing call. The conversation dispatch path sets it; Router.Select
// reads it.
func ContextWithThreadPrivacy(ctx context.Context, tp ThreadPrivacy) context.Context {
	return context.WithValue(ctx, threadPrivacyKey{}, tp)
}

// ThreadPrivacyFrom returns the thread attached to ctx, and whether one
// was attached at all.
func ThreadPrivacyFrom(ctx context.Context) (ThreadPrivacy, bool) {
	tp, ok := ctx.Value(threadPrivacyKey{}).(ThreadPrivacy)
	return tp, ok
}

// filterPrivacy is FILTER 0: the thread's privacy_mode, applied before any
// lane is considered for capability, health, quota or cost.
//
// It runs first so that a refusal CAUSED BY THE TABLE says it is a privacy
// refusal. Run after FILTER 1 it would be indistinguishable, to the
// operator reading the error, from "no lane advertises that capability" -
// blocked for the right reason under the wrong name.
//
// That guarantee is about THIS filter's own refusals and no more. If the
// table removes the capable lanes and leaves an incapable local one, the
// caller sees FILTER 1's ErrNoCapableProvider, and only the
// privacy:<mode>:N-filtered flag says why the capable ones were gone.
// Running first cannot fix that; it is why the flag carries the count.
//
// A context with no thread returns the candidates AND THE FLAGS untouched.
// That is not a hole: req.Sensitivity still reaches FILTER 2, and still
// resolves to restricted when invalid. It emits no flag because a gate
// that did nothing has nothing to explain, and because S-23.T5 froze the
// explain trail as a golden - a "no thread here" line on every `cascade
// run` would rewrite five acceptance fixtures to say nothing. The
// presence of a privacy: flag is itself the signal that a thread was in
// scope, so its ABSENCE is the diagnosis for a thread whose mode did not
// reach routing.
func filterPrivacy(ctx context.Context, cands []laneCandidate, flags []string) ([]laneCandidate, []string, error) {
	tp, ok := ThreadPrivacyFrom(ctx)
	if !ok {
		return cands, flags, nil
	}
	mode := tp.EffectiveMode()
	out := make([]laneCandidate, 0, len(cands))
	var refused []laneCandidate
	for _, c := range cands {
		if PrivacyAllows(mode, ClassifyLane(c.provider)) {
			out = append(out, c)
			continue
		}
		refused = append(refused, c)
	}
	flags = append(flags, fmt.Sprintf("privacy:%s:%d-filtered", mode.String(), len(refused)))
	if len(out) == 0 && len(cands) > 0 {
		return out, flags, privacyRefusal(tp, mode, refused)
	}
	return out, flags, nil
}

// privacyRefusal builds the ErrSensitivityViolation the contract names,
// carrying the thread, the mode and a refused lane.
//
// R-21.217 fixes ErrSensitivityViolation and ErrNoLane as THE sentinels
// for this condition and strikes any new one, so this WRAPS the existing
// sentinel rather than declaring a sibling: errors.Is still holds, and the
// detail rides on the message. Callers asserting this condition must check
// the message too - (*cascade.Error).Is compares KIND alone, so errors.Is
// against the sentinel holds for any KindPolicyDenied error.
func privacyRefusal(tp ThreadPrivacy, mode provider.SensitivityTier, refused []laneCandidate) error {
	lane, laneType := "(none)", LaneUnresolved
	if len(refused) > 0 {
		lane = refused[0].lane.LaneName
		laneType = ClassifyLane(refused[0].provider)
	}
	return cascade.Wrapf(cascade.KindPolicyDenied, ErrSensitivityViolation,
		"conductor: thread %s is %s, so it may not use lane %q (%s); %d candidate lane(s) refused, none remain",
		tp.ThreadID, mode.String(), lane, laneType.String(), len(refused))
}
