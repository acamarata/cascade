// Purpose: the two host policy seams the bridge consults before it lets an
//   inbound command or an outbound byte through — the §5.14 elevated-verb
//   table and the egress sensitivity tier — expressed pkg-facing so a
//   plugin package can hold them without importing internal/ (R-14.69).
//
// Inputs: an inbound command string (a Telegram message's text or a
//   callback_query's data), and the host's own policy implementations.
//
// Outputs: VerbCandidates turns a command-shaped string into the RPC verb
//   names it could mean; RefusesElevated asks the HOST's table about them.
//
// Constraints, and why the shape matters: the earlier draft classified
//   elevated verbs with a four-string prefix list inside the plugin. That
//   list is a second, silently-drifting copy of the elevated-verb table,
//   and "/node enroll" was not on it. Classification therefore belongs to
//   the host, which owns the one canonical table (internal/rpc's
//   elevationTable, transcribed from 06 §5.14); this package only maps
//   transport syntax onto verb NAMES and asks. The unwired default refuses
//   every candidate, so a half-wired host cannot admit an elevated verb.
//
//   Only a COMMAND-SHAPED input produces candidates (a leading "/" for
//   message text; callback data is always machine-generated command data).
//   Ordinary prose is not a verb and must stay dispatchable, or the bridge
//   would refuse every chat message it exists to carry.
//
// SPORT: plugins/cascade-pa ElevationPolicy/ADDED, SensitivityTier/ADDED,
//   VerbCandidates/ADDED (P1-E23-W5-S48-T1).

package cascadepa

import "strings"

// SensitivityTier is the egress classification a bridge adapter declares
// for content it is about to send. The values are the exact strings
// internal/hooks/egress's own SensitivityTier uses, so the host adapter is
// a cast and not a translation table that could disagree.
type SensitivityTier string

const (
	// TierLocalOnly is content that must never leave the machine.
	TierLocalOnly SensitivityTier = "local-only"
	// TierRestricted is content only a class registered AllowRestricted
	// may carry. The bridge class is not one.
	TierRestricted SensitivityTier = "restricted"
	// TierInternal is the tier the bridge's own operational replies carry.
	TierInternal SensitivityTier = "internal"
	// TierPublic is content admitted always.
	TierPublic SensitivityTier = "public"
)

// ElevationPolicy answers whether a verb name is an elevated verb. The host
// implements it over its canonical §5.14 table; nothing in this package
// keeps a copy of that table.
type ElevationPolicy interface {
	// IsElevatedVerb reports whether verb requires local presence.
	IsElevatedVerb(verb string) bool
}

// unconfiguredElevationPolicy is the fail-closed default: every candidate
// verb is elevated, so an unwired host refuses every command rather than
// admitting one it could not classify.
type unconfiguredElevationPolicy struct{}

func (unconfiguredElevationPolicy) IsElevatedVerb(string) bool { return true }

// ElevationOrRefuseAll substitutes the fail-closed default for a nil
// policy. Exported so every bridge adapter resolves nil the same way.
func ElevationOrRefuseAll(p ElevationPolicy) ElevationPolicy {
	if p == nil {
		return unconfiguredElevationPolicy{}
	}
	return p
}

// verbAliases maps the bare slash-command shorthands an operator actually
// types in a chat window onto the RPC verb names the elevated-verb table
// is keyed by. It is transport syntax, NOT policy: every entry's value is
// a name the host's table decides about, and adding one here can only ever
// cause MORE questions to be asked, never fewer.
var verbAliases = map[string]string{
	"enroll":  "node.enroll",
	"upgrade": "node.upgrade",
	"remove":  "node.remove",
	"grant":   "perms.grant",
	"revoke":  "perms.revoke",
	"rotate":  "vault.rotate",
}

// VerbCandidates returns every RPC verb name a command-shaped input could
// mean, most specific first. An input that is not command-shaped (ordinary
// prose) yields none.
//
// commandShaped is passed rather than inferred so the two transports keep
// their own rule: a message's text is a command only with a leading "/",
// while a callback_query's data is always machine-generated command data.
func VerbCandidates(raw string, commandShaped bool) []string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "/") {
		trimmed, commandShaped = strings.TrimPrefix(trimmed, "/"), true
	}
	if !commandShaped || trimmed == "" {
		return nil
	}
	tokens := strings.FieldsFunc(strings.ToLower(trimmed), func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ':' || r == '='
	})
	if len(tokens) == 0 {
		return nil
	}
	var out []string
	if alias, ok := verbAliases[tokens[0]]; ok {
		out = append(out, alias)
	}
	if len(tokens) > 1 {
		out = append(out, tokens[0]+"."+tokens[1])
	}
	return append(out, tokens[0])
}

// RefusesElevated reports whether raw names an elevated verb under policy.
// It is the one call site every bridge transport uses, so the text path and
// the callback path cannot drift apart.
func RefusesElevated(policy ElevationPolicy, raw string, commandShaped bool) bool {
	p := ElevationOrRefuseAll(policy)
	for _, verb := range VerbCandidates(raw, commandShaped) {
		if p.IsElevatedVerb(verb) {
			return true
		}
	}
	return false
}
