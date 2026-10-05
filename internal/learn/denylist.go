package learn

// Purpose: the hard denylist of the learned-config authority. C11
//   (sot/constitution.md) forbids learned output from writing policy,
//   security, egress, approval, destructive or minimum-verification rules;
//   this table is what makes that mechanical. DenyRules is the ordered
//   rule table and MatchDenylist its one query.
// Inputs: dotted config paths (FieldPath).
// Outputs: the first matching DenyRule per path, in path order.
// Constraints: closed Go data. No config key, plugin, RPC or proposal
//   payload adds, repoints or removes a rule; a new rule is a reviewed
//   change to this table and to testdata/denylist_golden.json together.
//   Rule Index is the rule's position and is stable: P2 records it.
// SPORT: internal.learn.DenyRules/ADDED, internal.learn.MatchDenylist/ADDED
//   (P1-LRN-01).

import (
	"regexp"
	"strings"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FieldPath is a dotted config key such as "ci.local.timeout_seconds".
// Inside a deny matcher or a target path, a segment "*" matches exactly one
// segment and a trailing ".*" matches one or more segments.
type FieldPath string

// DenyRule is one row of the hard denylist. Index is the rule's position in
// DenyRules and never changes once shipped.
type DenyRule struct {
	Index   int
	Area    DenylistArea
	Matcher FieldPath
}

// pathSegment is one lowercase TOML bare-key segment. Upper case is refused
// rather than folded: a path that differs from a denied key only by case is
// invalid input, never a key that slips past the table.
var pathSegment = regexp.MustCompile(`^[a-z0-9_-]+$`)

// denyRuleTable is the ordered hard denylist; first match wins. Sources:
// C11; the Epic Invalidations CI, GWY and GEN; P2-INT requirement 4b;
// P2-BLD-P1-1 (fabric.class.*).
var denyRuleTable = [...]struct {
	area    DenylistArea
	matcher FieldPath
}{
	{AreaAuthRules, "policy.auth.*"},
	{AreaAuthRules, "policy.autonomy_profile"},
	{AreaAuthRules, "policy.deny_list.*"},
	{AreaApprovalRules, "policy.approval_batch_window_s"},
	{AreaApprovalRules, "policy.approval_batch_cap"},
	{AreaApprovalRules, "grants.*"},
	{AreaDestructivePermissions, "policy.destructive.*"},
	{AreaAuthRules, "policy.*"},
	{AreaSecretAccess, "secrets.*"},
	{AreaSecretAccess, "vault.*"},
	{AreaTrustRoots, "elevation.*"},
	{AreaTrustRoots, "trust.*"},
	{AreaTrustRoots, "nodes.trust_tier"},
	{AreaTrustRoots, "registry.pubkey_path"},
	{AreaMandatorySecurityGates, "jobs.gates.*"},
	{AreaMandatorySecurityGates, "jobs.risk.*"},
	{AreaMandatorySecurityGates, "ci.policy.*"},
	{AreaMinimumVerification, "ci.requirements.*"},
	{AreaMinimumVerification, "ci.attestation.*"},
	{AreaMinimumVerification, "review.*"},
	{AreaMinimumVerification, "pbd.*"},
	{AreaProviderComplianceFlags, "providers.compliance.*"},
	{AreaModelAuthoringCapability, "providers.capabilities.authoring"},
	{AreaEgressRules, "egress.*"},
	{AreaEgressRules, "hooks.egress.*"},
	{AreaEgressRules, "agents.egress.*"},
	{AreaEgressRules, "ci.mirror_remote"},
	{AreaEgressRules, "gateways.*.base_url"},
	{AreaEgressRules, "gateways.*.header"},
	{AreaEgressRules, "registry.url"},
	{AreaEgressRules, "telemetry.*"},
	{AreaEgressRules, "sync.*"},
	{AreaExecutionAuthority, "gateways.*"},
	{AreaExecutionAuthority, "agents.auth.*.gateway"},
	{AreaExecutionAuthority, "ci.toolchain_versions"},
	{AreaExecutionAuthority, "nodes.repos.*"},
	{AreaExecutionAuthority, "fabric.class.*"},
	{AreaExecutionAuthority, "plugins.*"},
	{AreaExecutionAuthority, "fleet.harness_binary"},
	{AreaExecutionAuthority, "conductor.retry.*"},
	{AreaExecutionAuthority, "conductor.degrade_after"},
	{AreaExecutionAuthority, "context.generation.*"},
	{AreaExecutionAuthority, "harness.*"},
	{AreaBackupGuarantees, "backup.*"},
}

// DenyRules returns an ordered copy of the hard denylist. Index equals the
// position; the first rule that matches a path wins.
func DenyRules() []DenyRule {
	out := make([]DenyRule, len(denyRuleTable))
	for i, r := range denyRuleTable {
		out[i] = DenyRule{Index: i, Area: r.area, Matcher: r.matcher}
	}
	return out
}

// MatchDenylist returns the first matching rule for each path that matches
// one, in path order. No match at all is (nil, nil). An empty list or an
// invalid path is KindInvalidInput: a query that cannot be answered is
// refused, never read as "not denied". A path naming an ancestor table of a
// denied key (for example "gateways.acct-a") matches too, because writing
// that table would replace the denied keys under it.
func MatchDenylist(paths []FieldPath) ([]DenyRule, error) {
	if len(paths) == 0 {
		return nil, cascade.New(cascade.KindInvalidInput, "learn: denylist query needs at least one path")
	}
	rules := DenyRules()
	var out []DenyRule
	for _, p := range paths {
		if err := validateConcretePath(p); err != nil {
			return nil, err
		}
		for _, r := range rules {
			if matcherCovers(r.Matcher, p) {
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}

// validateConcretePath refuses an empty path, an empty segment, a segment
// outside the lowercase bare-key alphabet, and a wildcard: a config key is
// concrete, only matchers carry "*".
func validateConcretePath(p FieldPath) error {
	if p == "" {
		return cascade.New(cascade.KindInvalidInput, "learn: config path is empty")
	}
	for _, seg := range strings.Split(string(p), ".") {
		if !pathSegment.MatchString(seg) {
			return cascade.New(cascade.KindInvalidInput,
				"learn: config path must be dot-separated lowercase bare-key segments")
		}
	}
	return nil
}

// validateMatcher is validateConcretePath for a matcher: a segment may also
// be "*".
func validateMatcher(m FieldPath) error {
	if m == "" {
		return cascade.New(cascade.KindInvalidInput, "learn: matcher is empty")
	}
	for _, seg := range strings.Split(string(m), ".") {
		if seg != "*" && !pathSegment.MatchString(seg) {
			return cascade.New(cascade.KindInvalidInput,
				"learn: matcher must be dot-separated lowercase bare-key segments or \"*\"")
		}
	}
	return nil
}

// matcherMatches reports whether matcher m matches concrete path p: each "*"
// segment matches one segment and a trailing "*" matches one or more.
func matcherMatches(m, p FieldPath) bool {
	ms := strings.Split(string(m), ".")
	ps := strings.Split(string(p), ".")
	last := len(ms) - 1
	if ms[last] == "*" && len(ps) > len(ms) {
		ps = append(ps[:last], strings.Join(ps[last:], "."))
	}
	if len(ps) != len(ms) {
		return false
	}
	for i, seg := range ms {
		if seg != "*" && seg != ps[i] {
			return false
		}
	}
	return true
}

// matcherCovers reports whether m matches p or p is an ancestor table of a
// key m matches (p's segments are a proper prefix of m's, "*" matching any).
func matcherCovers(m, p FieldPath) bool {
	if matcherMatches(m, p) {
		return true
	}
	ms := strings.Split(string(m), ".")
	ps := strings.Split(string(p), ".")
	if len(ps) >= len(ms) {
		return false
	}
	for i, seg := range ps {
		if ms[i] != "*" && ms[i] != seg {
			return false
		}
	}
	return true
}
