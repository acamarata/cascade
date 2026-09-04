// Package policy (autonomy_overlay.go): Purpose: the overlay half of the
//
//	[policy] section parser — the allow/ask/deny level lists and the level
//	-name grammar they are written in. Split from autonomy_config.go per
//	R-14.117/Art.10.3 (the 300-line file cap).
//
// Inputs: the decoded [policy] table.
// Outputs: levels per verdict, or a typed refusal.
// Constraints: a level name the ladder does not have is a refusal, not a
//
//	rung; a level named by two lists is a refusal rather than a guess
//	between a tighter and a looser answer. Whether an overlay may APPLY is
//	decided in autonomy_controller.go, which refuses any that widens.
//
// SPORT: internal/policy parseOverlays/ADDED (P1-E09-W2-S18-T1).
package policy

import "strings"

// parseOverlays reads the allow/ask/deny lists. Each is a list of level
// names; a level may appear in at most one list across all three, because
// a level named twice has no defined resolution and guessing one would
// mean guessing between a tighter and a looser answer.
func (c *Config) parseOverlays(table map[string]interface{}) error {
	seen := map[RiskLevel]string{}
	for _, entry := range []struct {
		key string
		v   Verdict
	}{{"allow", VerdictAllow}, {"ask", VerdictAsk}, {"deny", VerdictDeny}} {
		levels, err := parseLevelList(table, entry.key)
		if err != nil {
			return err
		}
		for _, lvl := range levels {
			if prev, dup := seen[lvl]; dup {
				return newConfigError("policy."+entry.key,
					"%s is already named by policy.%s", lvl.String(), prev)
			}
			seen[lvl] = entry.key
		}
		if len(levels) > 0 {
			c.Overlays[entry.v] = levels
		}
	}
	return nil
}

// parseLevelList reads one overlay list and converts each entry to a rung.
func parseLevelList(table map[string]interface{}, key string) ([]RiskLevel, error) {
	raw, ok := table[key]
	if !ok {
		return nil, nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil, newConfigError("policy."+key, "must be a list of level names")
	}
	out := make([]RiskLevel, 0, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok {
			return nil, newConfigError("policy."+key, "every entry must be a level name")
		}
		lvl, err := parseRiskLevelName(name)
		if err != nil {
			return nil, newConfigError("policy."+key,
				"%q is not a risk level (want L0, L1, L2, L3 or L4)", sanitize(name))
		}
		out = append(out, lvl)
	}
	return out, nil
}

// parseRiskLevelName maps "L0".."L4" (in either case) to its rung. An
// unrecognised spelling is an error rather than a rung, so no input can
// name a level the ladder does not have.
func parseRiskLevelName(name string) (RiskLevel, error) {
	want := strings.ToUpper(strings.TrimSpace(name))
	for lvl := L0; lvl <= L4; lvl++ {
		if riskLevelNames[lvl] == want {
			return lvl, nil
		}
	}
	return L4, newConfigError("policy", "%q is not a risk level", sanitize(name))
}
