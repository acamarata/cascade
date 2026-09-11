// Package policy (risk_gates_config.go): Purpose: parses
//
//	[policy.risk_gates] (R-16.70(b), AH/S-69.T1) into Config.RiskGates,
//	a RAW risk-class-name -> gate-step-name-list map.
//
// Inputs: the same generic [policy] table ParseConfig already decoded;
//
//	risk_gates is one more key inside it.
//
// Outputs: Config.RiskGates, or a typed refusal.
//
// Constraints: this package validates STRUCTURE and the closed
//
//	risk-class-name vocabulary only (a table, with recognised class
//	keys and string-list values) -- it does NOT validate individual
//	gate-step names against AC/S-59.T4's GateStep vocabulary, because
//	internal/policy cannot import internal/jobs: internal/jobs already
//	imports internal/conductor, which imports internal/hooks/egress,
//	which imports internal/secrets, which imports internal/policy --
//	adding internal/policy -> internal/jobs closes that into an import
//	cycle. jobs.BuildRiskGateOverlay (riskgates_overlay.go) is where
//	gate-step-name validation actually happens; the daemon's
//	composition root (cmd/cascade/daemon_unix_policy.go, which imports
//	both packages) runs it BEFORE the config-reload swap, so a bad name
//	still refuses the reload before anything is applied
//	(validate-before-write) -- one layer higher than this file, not
//	inside it.
//
// SPORT: internal/policy risk-gates-overlay/ADD (P1-E34-W7-S69-T1).
package policy

import (
	"sort"
	"strings"
)

// riskGateClassNames is the closed key set [policy.risk_gates] accepts
// -- one per AC/S-59.T4's four RiskClass members, restated here only as
// the TOML key vocabulary (not a gate-set table; this package still
// defines no gate data).
var riskGateClassNames = map[string]bool{
	"low": true, "normal": true, "high": true, "critical": true,
}

// parseRiskGates reads table["risk_gates"] into c.RiskGates. An absent
// key leaves c.RiskGates nil; a present key must be a table whose own
// keys are all recognised risk-class names and whose values are all
// string lists.
func (c *Config) parseRiskGates(table map[string]interface{}) error {
	raw, ok := table["risk_gates"]
	if !ok {
		return nil
	}
	section, ok := raw.(map[string]interface{})
	if !ok {
		return newConfigError("policy.risk_gates", "must be a table")
	}

	overlay := make(map[string][]string, len(section))
	unknownClasses := make([]string, 0, len(section))
	for key, value := range section {
		if !riskGateClassNames[key] {
			unknownClasses = append(unknownClasses, sanitize(key))
			continue
		}
		names, err := parseGateStepList(key, value)
		if err != nil {
			return err
		}
		overlay[key] = names
	}
	if len(unknownClasses) > 0 {
		sort.Strings(unknownClasses)
		return newConfigError("policy.risk_gates",
			"unrecognised risk class(es): %s", strings.Join(unknownClasses, ", "))
	}
	if len(overlay) > 0 {
		c.RiskGates = overlay
	}
	return nil
}

// parseGateStepList decodes one [policy.risk_gates] class's value: a
// list of gate-step name strings. The names themselves are validated
// one layer up, by jobs.BuildRiskGateOverlay -- this function only
// checks the list SHAPE (a list of strings), which this package can do
// without importing internal/jobs.
func parseGateStepList(class string, value interface{}) ([]string, error) {
	items, ok := value.([]interface{})
	if !ok {
		return nil, newConfigError("policy.risk_gates."+class, "must be a list of gate step names")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok {
			return nil, newConfigError("policy.risk_gates."+class, "every entry must be a string")
		}
		if strings.TrimSpace(name) == "" {
			return nil, newConfigError("policy.risk_gates."+class, "entries may not be empty")
		}
		out = append(out, name)
	}
	return out, nil
}
