// Purpose: the `[fleet.economics]` table parser behind Load (config.go):
//   08-INIT-CONFIG-SPEC.md §3's exploration_rate/allow_premium_in_crunch
//   scalars plus the multipliers/weights hot-override sub-tables. Split
//   out as its own sibling file per R-14.117's established remedy
//   (config_fleet.go/config_logging.go do the same) -- config.go sits
//   near its 300-line cap and this ticket's own files_scope names only
//   config.go/schema.go, but R-16.79 (follow the tree) requires the same
//   file-cap remedy every sibling typed section already took. Recorded
//   here and in this ticket's journal as a files_scope deviation.
//
// CONTRACT DEVIATION (R-16.79, recorded here and in the journal). The
// ticket text says the block is "registered in internal/runtime/schema.go,
// the C/S-04.T1 schema registry". internal/runtime/schema.go IS
// C/S-04.T1's actual deliverable (the schema_version migration frame --
// see that file's own doc comment and P1-E03-W1-S04-T1's contract), not a
// separate hot/cold section registry: reload class (hot vs cold) is
// expressed structurally by hotreload.go's coldSections list, which this
// section is simply never added to (matching [fleet.accounts]'s own
// precedent, config_fleet.go). No edit to schema.go is needed for
// [fleet.economics] to be hot-reloadable: this file's own comment IS its
// registration.
//
// CONTRACT DEVIATION (second, R-16.79). internal/runtime cannot import
// internal/fleet/economics to decode directly into economics.Overrides:
// economics imports internal/fleet/topology, and topology's
// rpc_quota.go imports internal/rpc -> internal/events -> internal/runtime
// (bus.go), a real import cycle identical to config_fleet.go's own
// documented CONTRACT DEVIATION for the same reason. This file therefore
// decodes the block into a package-local economicsSection with the SAME
// shape economics.Overrides declares (plain string-keyed maps), and
// performs its own top-level-key and numeric-type validation; the deep
// semantic validation (unknown lane class, unknown mode, unknown weight
// name, non-finite value) is economics.ApplyOverrides's own job, run by
// whichever composition root converts this section into an
// economics.Overrides value.
//
// Inputs: the decoded generic config tree (env overrides already
//   applied).
// Outputs: an economicsSection, or a typed *ConfigError for an unknown
//   top-level key or a type mismatch.
// Constraints: exploration_rate defaults to 0.05 and
//   allow_premium_in_crunch to false when absent (08 §3) -- unlike
//   [logging.rotation]'s deliberate no-default rule, these two DO carry
//   shipped defaults per the ticket contract.
// SPORT: runtime/config-economics (ADD, P1-E41-W9-S79-T2).

package runtime

import "fmt"

// economicsSection is the [fleet.economics] table's typed view.
type economicsSection struct {
	ExplorationRate      float64
	AllowPremiumInCrunch bool
	// Multipliers is keyed by lane class then mode.
	Multipliers map[string]map[string]float64
	// Weights is keyed by mode then weight name.
	Weights map[string]map[string]float64
}

// defaultExplorationRate and defaultAllowPremiumInCrunch are 08 §3's
// [fleet.economics] shipped defaults.
const defaultExplorationRate = 0.05
const defaultAllowPremiumInCrunch = false

// economicsTopKeys is the closed set of keys a [fleet.economics] table
// recognises. An unknown key inside the block is a hard typed error
// (unlike [fleet.accounts]'s per-entry leniency): the ticket contract
// asks for validate-before-write, and a mistyped key here would otherwise
// silently do nothing.
var economicsTopKeys = map[string]bool{
	"exploration_rate": true, "allow_premium_in_crunch": true,
	"multipliers": true, "weights": true,
}

// parseEconomicsSection type-checks tree's [fleet.economics] table.
func parseEconomicsSection(tree map[string]interface{}) (economicsSection, error) {
	sec := economicsSection{ExplorationRate: defaultExplorationRate, AllowPremiumInCrunch: defaultAllowPremiumInCrunch}
	fleetRaw, _ := tree["fleet"].(map[string]interface{})
	econRaw, ok := fleetRaw["economics"].(map[string]interface{})
	if !ok {
		return sec, nil
	}
	for k := range econRaw {
		if !economicsTopKeys[k] {
			return economicsSection{}, &ConfigError{Field: "fleet.economics." + k, Reason: "unrecognised key in [fleet.economics]"}
		}
	}
	var err error
	if sec.ExplorationRate, err = economicsFloatField(econRaw, "exploration_rate", defaultExplorationRate); err != nil {
		return economicsSection{}, err
	}
	if sec.AllowPremiumInCrunch, err = economicsBoolField(econRaw); err != nil {
		return economicsSection{}, err
	}
	if sec.Multipliers, err = economicsNestedTable(econRaw, "multipliers"); err != nil {
		return economicsSection{}, err
	}
	if sec.Weights, err = economicsNestedTable(econRaw, "weights"); err != nil {
		return economicsSection{}, err
	}
	return sec, nil
}

// economicsFloatField reads a top-level float field, defaulting when
// absent.
func economicsFloatField(econRaw map[string]interface{}, field string, def float64) (float64, error) {
	v, ok := econRaw[field]
	if !ok {
		return def, nil
	}
	f, ok := tomlFloat(v)
	if !ok {
		return 0, &ConfigError{Field: "fleet.economics." + field, Reason: "must be a number"}
	}
	return f, nil
}

// economicsBoolField reads allow_premium_in_crunch, defaulting to false.
func economicsBoolField(econRaw map[string]interface{}) (bool, error) {
	v, ok := econRaw["allow_premium_in_crunch"]
	if !ok {
		return defaultAllowPremiumInCrunch, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, &ConfigError{Field: "fleet.economics.allow_premium_in_crunch", Reason: "must be a boolean"}
	}
	return b, nil
}

// economicsNestedTable reads the multipliers/weights two-level override
// sub-table: `[fleet.economics.<field>."<key>"] <name> = <float>`. Key
// and weight/mode names are passed through unvalidated -- the semantic
// check (a real lane class/mode/weight name) belongs to
// economics.ApplyOverrides, which this package cannot call (see this
// file's header CONTRACT DEVIATION).
func economicsNestedTable(econRaw map[string]interface{}, field string) (map[string]map[string]float64, error) {
	raw, ok := econRaw[field].(map[string]interface{})
	if !ok || len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]map[string]float64, len(raw))
	for key, v := range raw {
		entry, ok := v.(map[string]interface{})
		if !ok {
			return nil, &ConfigError{Field: fmt.Sprintf("fleet.economics.%s.%s", field, key), Reason: "must be a table"}
		}
		inner := make(map[string]float64, len(entry))
		for name, nv := range entry {
			f, ok := tomlFloat(nv)
			if !ok {
				return nil, &ConfigError{Field: fmt.Sprintf("fleet.economics.%s.%s.%s", field, key, name), Reason: "must be a number"}
			}
			inner[name] = f
		}
		out[key] = inner
	}
	return out, nil
}
