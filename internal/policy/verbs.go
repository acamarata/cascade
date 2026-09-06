// Package policy (verbs.go): Purpose: the R-21.207 verb registry and the
//
//	ONE authorization middleware every approval and policy surface verb is
//	dispatched through, whatever origin it arrived from (CLI, RPC, plugin,
//	bridge, node, conductor or hook).
//
// Inputs: a method name and, for an elevated verb, an Attestor that
//
//	answers whether a FRESH LOCAL attestation covers it.
//
// Outputs: VerbSpec, LookupVerb, RegisteredVerbs, Attestor, Authorize,
//
//	ErrVerbUnregistered.
//
// Constraints: FAIL CLOSED. A method this table does not name is REFUSED,
//
//	never defaulted to a permissive rung: that is the whole point of the
//	ruling, and it is why the table is keyed by exact method name with no
//	prefix or wildcard matching. Whether a verb is ELEVATED is DERIVED
//	from internal/rpc's canonical §5.14 table rather than transcribed
//	here, for the reason daemonless_elevation.go already states: a second
//	elevated-verb list is a list that can drift, and the drift direction
//	that matters is the one that silently stops elevating. An elevated
//	verb with no Attestor, or with an Attestor that cannot vouch for a
//	fresh local attestation, is refused with KindElevationRequired.
//
// SPORT: internal/policy VerbSpec/ADDED, LookupVerb/ADDED,
//
//	Authorize/ADDED (P1-E09-W2-S18-T6).
package policy

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// CodeVerbUnregistered is the stable identifier for a refusal caused by a
// method that the verb registry does not name (R-14.152).
const CodeVerbUnregistered = "verb-unregistered"

// ErrVerbUnregistered is the comparison target for that refusal.
var ErrVerbUnregistered = errors.New(CodeVerbUnregistered)

// mcpToolPrefix is the 07-CLI-COMMAND-TREE mirror-rule prefix: a mounted
// verb noun.verb becomes the MCP tool cascade_noun_verb.
const mcpToolPrefix = "cascade_"

// VerbSpec is one registered surface verb and the metadata the one
// authorization middleware decides with.
//
// Elevation is deliberately NOT a field. It is a method (Elevated), so
// there is nothing here for a future edit to set inconsistently with the
// canonical table; see this file's doc comment.
type VerbSpec struct {
	// Method is the exact JSON-RPC method name. It is the registry key.
	Method string
	// Risk is the rung this verb is dispatched at when no narrower rule
	// applies. Read verbs sit at L0; verbs that change queue or grant
	// state sit higher.
	Risk RiskLevel
	// Mutates reports whether the verb changes state. A mutating verb
	// reports what it changed rather than succeeding silently.
	Mutates bool
	// MCPExposed reports whether the verb is mirrored as an MCP tool. It
	// is the ✦ column of 07-CLI-COMMAND-TREE: only read verbs that carry
	// no grant value are mirrored.
	MCPExposed bool
}

// Elevated reports whether this verb needs the §5.14 elevation flow. It is
// derived from internal/rpc's canonical table with nil params, which every
// conditional rule there treats as "cannot prove this is the harmless
// case, elevate".
func (v VerbSpec) Elevated() bool { return rpc.IsElevated(v.Method, nil) }

// MCPTool returns the mirror-rule tool name for a mirrored verb, and the
// empty string for one that is not mirrored. A caller that registers an
// MCP tool from a non-mirrored verb therefore registers nothing rather
// than registering a tool with a plausible-looking name.
func (v VerbSpec) MCPTool() string {
	if !v.MCPExposed {
		return ""
	}
	return mcpToolPrefix + strings.ReplaceAll(v.Method, ".", "_")
}

// verbRegistry is the CLOSED set of approval and policy surface verbs.
//
// The standing-grant verbs are named standing_grant.* rather than
// approval.standing_* because those are the names the canonical §5.14
// elevation table already carries; see this file's doc comment and the
// ticket journal for the contract text that names them otherwise.
var verbRegistry = map[string]VerbSpec{
	"approval.list":         {Method: "approval.list", Risk: L0, MCPExposed: true},
	"approval.show":         {Method: "approval.show", Risk: L0, MCPExposed: true},
	"approval.grant":        {Method: "approval.grant", Risk: L3, Mutates: true},
	"approval.deny":         {Method: "approval.deny", Risk: L2, Mutates: true},
	"approval.expire":       {Method: "approval.expire", Risk: L2, Mutates: true},
	"standing_grant.list":   {Method: "standing_grant.list", Risk: L0, MCPExposed: true},
	"standing_grant.create": {Method: "standing_grant.create", Risk: L3, Mutates: true},
	"standing_grant.change": {Method: "standing_grant.change", Risk: L3, Mutates: true},
	"standing_grant.revoke": {Method: "standing_grant.revoke", Risk: L2, Mutates: true},
	"policy.explain":        {Method: "policy.explain", Risk: L0, MCPExposed: true},
	"policy.check":          {Method: "policy.check", Risk: L0, MCPExposed: true},
	"policy.list":           {Method: "policy.list", Risk: L0, MCPExposed: true},
	"policy.audit_query":    {Method: "policy.audit_query", Risk: L0, MCPExposed: true},
}

// LookupVerb returns the spec for method. An unregistered method is
// REFUSED with KindPolicyDenied: there is no default spec, because a
// default is exactly the silent permission R-21.207 forbids.
func LookupVerb(method string) (VerbSpec, error) {
	spec, ok := verbRegistry[method]
	if !ok {
		return VerbSpec{}, cascade.Wrapf(cascade.KindPolicyDenied, ErrVerbUnregistered,
			"policy: %q is not a registered verb, so it is refused rather than defaulted",
			sanitize(method))
	}
	return spec, nil
}

// RegisteredVerbs returns every registered verb, ordered by method name so
// the result is stable across calls and across builds.
func RegisteredVerbs() []VerbSpec {
	out := make([]VerbSpec, 0, len(verbRegistry))
	for _, spec := range verbRegistry {
		out = append(out, spec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out
}

// Attestor answers whether a fresh LOCAL attestation covers one method.
// It is the seam the elevation helper drops into; this package never
// touches a keystore itself.
type Attestor interface {
	// FreshLocalAttestation returns nil when a fresh local attestation
	// covering method exists, and an error otherwise. An implementation
	// that cannot reach its authenticator returns an error: unreachable
	// is not attested.
	FreshLocalAttestation(ctx context.Context, method string) error
}

// Authorize is the ONE middleware every approval and policy verb passes
// through, from every origin.
//
// The order is the contract: an unregistered verb is refused before
// anything else is consulted, and an elevated verb needs a fresh local
// attestation IN ADDITION to whatever token its handler will go on to
// verify. A nil Attestor is not "no elevation required"; it is "nothing
// can vouch for this", which refuses.
func Authorize(ctx context.Context, method string, att Attestor) (VerbSpec, error) {
	spec, err := LookupVerb(method)
	if err != nil {
		return VerbSpec{}, err
	}
	if !spec.Elevated() {
		return spec, nil
	}
	if att == nil {
		return VerbSpec{}, cascade.Newf(cascade.KindElevationRequired,
			"policy: %s is an elevated verb and no attestation source is enrolled", spec.Method)
	}
	if err := att.FreshLocalAttestation(ctx, spec.Method); err != nil {
		if _, ok := cascade.KindOf(err); ok {
			return VerbSpec{}, err
		}
		return VerbSpec{}, cascade.Wrapf(cascade.KindElevationRequired, err,
			"policy: %s needs a fresh local attestation", spec.Method)
	}
	return spec, nil
}
