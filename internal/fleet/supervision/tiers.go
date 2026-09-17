// Purpose: the supervision TIER — which of the three ways a held action is
//
//	put to a human, selected by configuration rather than derived from the
//	action (R-16.54). Tier 1 is hook-mediated auto-advance (S-39.T2), tier
//	2 attaches a PTY and asks on it, tier 3 only ever suggests.
//
// WHY CONFIG AND NOT A FLAG. R-16.54 struck the invented
//
//	`--supervision-tier` flag. A tier chosen per invocation would let the
//	caller pick how much supervision its own action receives, which is the
//	one party that must not choose. It is `[fleet.supervision].tier` in
//	the config file, a hot-reload key, read live.
//
// WHY PARSED HERE AND NOT IN internal/runtime. internal/runtime/config.go
//
//	is at 282 of Art.10.3's 300 lines, the same cap that already pushed
//	`[conductor.quota]` out of it. This follows conductor.ParseQuotaConfig
//	exactly: the CONSUMING package reads its own section out of the
//	generic Extra tree runtime.Config already carries for sections
//	config.go does not own (R-14.247 §8).
//
// Inputs: the decoded config tree.
// Outputs: a validated Tier, or a typed config error.
// Constraints: a MISSING section resolves to Tier1 — the 08 §3 row's own
//
//	default, and the least autonomous of the three, so the safe value is
//	also the default value. A PRESENT but unrecognised value is an ERROR,
//	never a silent fallback: an operator who wrote `tier = 4` meant
//	something, and guessing which of three things they meant is worse than
//	refusing the file.
//
// SPORT: fleet.supervision.Tier/ADDED (P1-E18-W4-S39-T3). The contract
//
//	names this type SupervisionTier; the lint wall's stutter rule refuses
//	supervision.SupervisionTier, so it is Tier. The CONFIG KEY the
//	contract fixes -- [fleet.supervision].tier -- is unchanged, which is
//	the part an operator sees.

package supervision

import "github.com/acamarata/cascade/pkg/cascade"

// Tier names how a held action reaches a human.
type Tier int

// The three tiers. The zero value is deliberately NOT a member: a
// zero-valued tier is a tier nobody chose, and Valid rejects it, so it can
// never be read as "tier 1" by accident.
const (
	// TierHookMediated (1) is S-39.T2's auto-advance path: the hook
	// decides, within the profile's ceiling, and nothing attaches to a
	// terminal.
	TierHookMediated Tier = 1
	// TierPTYAttached (2) holds every action at risk level L2 or above
	// until a human approves it on an attached pseudo-terminal.
	TierPTYAttached Tier = 2
	// TierSuggestOnly (3) never executes. Every pending action becomes a
	// suggestion a human reads later.
	TierSuggestOnly Tier = 3
)

// DefaultSupervisionTier is what an absent [fleet.supervision] section
// resolves to (08-INIT-CONFIG-SPEC §3's own `tier=1` row).
const DefaultSupervisionTier = TierHookMediated

// ErrInvalidSupervisionConfig wraps a rejected [fleet.supervision] value.
// Kept as a sentinel, on conductor.ErrInvalidQuotaConfig's terms, so a
// caller can errors.Is against it independently of the message text.
var ErrInvalidSupervisionConfig = cascade.New(cascade.KindInvalidInput,
	"supervision: invalid [fleet.supervision] config")

// Valid reports whether t is one of the three tiers.
func (t Tier) Valid() bool {
	return t >= TierHookMediated && t <= TierSuggestOnly
}

// String renders the tier for logs and refusals.
func (t Tier) String() string {
	switch t {
	case TierHookMediated:
		return "tier1-hook-mediated"
	case TierPTYAttached:
		return "tier2-pty-attached"
	case TierSuggestOnly:
		return "tier3-suggest-only"
	default:
		return "tier-unset"
	}
}

// ParseSupervisionConfig reads [fleet.supervision].tier out of extra, the
// generic map runtime.Config carries for every section config.go does not
// itself own.
//
// divergent reports that no section was present, so a caller can emit the
// divergence event the other section parsers emit; it is not an error, and
// the returned tier is the default in that case.
func ParseSupervisionConfig(extra map[string]interface{}) (tier Tier, divergent bool, err error) {
	raw, ok := lookupSupervisionTable(extra)
	if !ok {
		return DefaultSupervisionTier, true, nil
	}
	v, present := raw["tier"]
	if !present {
		return DefaultSupervisionTier, false, nil
	}
	n, ok := toTierInt(v)
	if !ok {
		return 0, false, cascade.Wrap(cascade.KindInvalidInput, ErrInvalidSupervisionConfig,
			"fleet.supervision.tier must be an integer: 1 (hook-mediated), 2 (PTY-attached) or 3 (suggest-only)")
	}
	parsed := Tier(n)
	if !parsed.Valid() {
		return 0, false, cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSupervisionConfig,
			"fleet.supervision.tier = %d is not a tier: want 1 (hook-mediated), 2 (PTY-attached) or 3 (suggest-only)", n)
	}
	return parsed, false, nil
}

// lookupSupervisionTable finds extra["fleet"]["supervision"] as a map, or
// reports absent.
func lookupSupervisionTable(extra map[string]interface{}) (map[string]interface{}, bool) {
	if extra == nil {
		return nil, false
	}
	fleetRaw, ok := extra["fleet"]
	if !ok {
		return nil, false
	}
	fleetTable, ok := fleetRaw.(map[string]interface{})
	if !ok {
		return nil, false
	}
	supRaw, ok := fleetTable["supervision"]
	if !ok {
		return nil, false
	}
	sup, ok := supRaw.(map[string]interface{})
	return sup, ok
}

// toTierInt accepts either decoded-TOML integer representation
// (pelletier/go-toml/v2 decodes bare integers as int64), matching
// conductor.toInt64's own reasoning.
func toTierInt(v interface{}) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	default:
		return 0, false
	}
}
