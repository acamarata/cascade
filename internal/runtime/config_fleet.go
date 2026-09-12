package runtime

// Purpose: the `[fleet.accounts."<id>"]` table parser behind Load
//   (config.go): R-21.34/R-21.44's account-role, preserve_weekly_reserve
//   and executive-model-fraction keys. Split out as its own sibling file
//   per R-14.117's established remedy (config_logging.go/
//   config_retrieval*.go do the same) -- config.go was already at 285 of
//   its 300-line cap before this ticket's typed [fleet.accounts] read,
//   and this ticket's files_scope names only config.go/schema.go, but
//   R-16.79 (follow the tree) requires the same file-cap remedy every
//   sibling typed section already took. Recorded in this ticket's journal
//   as a files_scope deviation.
//
// CONTRACT DEVIATION (R-16.79, recorded here and in the journal). The
// ticket text says "the config load CALLS topology.ValidateReserve(v)".
// internal/runtime cannot import internal/fleet/topology: topology's
// rpc_quota.go imports internal/rpc -> internal/events -> internal/runtime
// (bus.go), a real, compiler-enforced import cycle (`go build
// ./internal/runtime/...` fails with "import cycle not allowed" the
// moment topology is imported here). internal/runtime sits below
// internal/fleet in this tree's dependency order and cannot reach up into
// it. This file therefore enforces the IDENTICAL [0.0,0.60] bound
// topology.ValidateReserve enforces, as its own local check, returning a
// *ConfigError (this package's own established validation-error type,
// used by every other section parser in this file's siblings) rather
// than topology.ErrConfigRange, which this package cannot name. The
// bound is a single named constant pair
// (fleetReserveMin/fleetReserveMax) equal to topology's own
// reserveMin/reserveMax, cross-referenced by comment so the two can never
// silently drift without both showing up in a grep for R-21.117.
//
// Inputs: the decoded generic config tree (env overrides already
//   applied).
// Outputs: a map[string]FleetAccountConfig keyed by account id, or a
//   typed *ConfigError for a malformed entry.
// Constraints: R-21.44/08-INIT-CONFIG-SPEC §Round-21 -- this section is
//   PERSONAL config (account id, role, reserve, fraction caps never
//   tracked in a repo file; this parser itself has no opinion on where
//   config.toml lives, only on its shape). A field absent from an
//   account's table is nil (Role: ""), never a silently invented default:
//   the R-21.34 role-conditioned preserve_weekly_reserve default and the
//   universal 0.35/0.47 fraction defaults are
//   internal/fleet/economics.RoleDefaults' own responsibility, not
//   duplicated here, so the two can never drift apart.
// SPORT: runtime/config-fleet-accounts (ADD, P1-E41-W9-S80-T1).

import "fmt"

// FleetAccountConfig is one `[fleet.accounts."<id>"]` entry. A nil pointer
// field means "not set in config.toml" -- RoleDefaults(role) supplies the
// R-21.34 default, never invented here. Role "" means unset; resolving an
// unset/unrecognised role to workforce (never executive) is
// economics.ResolveAccountRole's fail-closed job, not this parser's.
type FleetAccountConfig struct {
	Role                       string
	PreserveWeeklyReserve      *float64
	ExecutiveModelFractionSoft *float64
	ExecutiveModelFractionHard *float64
}

// fleetReserveMin/fleetReserveMax mirror internal/fleet/topology's own
// reserveMin/reserveMax (R-21.117) -- see this file's CONTRACT DEVIATION
// header comment for why they cannot simply be topology.ValidateReserve.
const (
	fleetReserveMin = 0.0
	fleetReserveMax = 0.60
)

// fleetAccountKeys is the closed set of keys a `[fleet.accounts."<id>"]`
// entry recognises.
var fleetAccountKeys = map[string]bool{
	"role": true, "preserve_weekly_reserve": true,
	"executive_model_fraction_soft": true, "executive_model_fraction_hard": true,
}

// parseFleetAccountsSection type-checks tree's `[fleet.accounts."<id>"]`
// tables. An unrecognised key inside one account's table is warned, never
// a hard error (matching [runtime]'s own leniency for additive future
// keys); a type mismatch or an out-of-range preserve_weekly_reserve is a
// hard typed error, since a value that silently failed to apply would
// leave a caller believing a reserve is enforced when it is not.
func parseFleetAccountsSection(tree map[string]interface{}, warn func(string, ...interface{})) (map[string]FleetAccountConfig, error) {
	fleetRaw, _ := tree["fleet"].(map[string]interface{})
	accountsRaw, _ := fleetRaw["accounts"].(map[string]interface{})
	if len(accountsRaw) == 0 {
		return nil, nil
	}
	out := make(map[string]FleetAccountConfig, len(accountsRaw))
	for id, v := range accountsRaw {
		entry, ok := v.(map[string]interface{})
		if !ok {
			return nil, &ConfigError{Field: "fleet.accounts." + id, Reason: "must be a table"}
		}
		cfg, err := parseOneFleetAccount(id, entry, warn)
		if err != nil {
			return nil, err
		}
		out[id] = cfg
	}
	return out, nil
}

// parseOneFleetAccount parses a single account id's table.
func parseOneFleetAccount(id string, entry map[string]interface{}, warn func(string, ...interface{})) (FleetAccountConfig, error) {
	for k := range entry {
		if !fleetAccountKeys[k] {
			warn("runtime: unknown key fleet.accounts.%s.%s in config.toml (preserved, not validated)", id, k)
		}
	}
	var cfg FleetAccountConfig
	if v, ok := entry["role"]; ok {
		s, ok := v.(string)
		if !ok {
			return FleetAccountConfig{}, &ConfigError{Field: "fleet.accounts." + id + ".role", Reason: "must be a string"}
		}
		cfg.Role = s
	}
	var err error
	if cfg.PreserveWeeklyReserve, err = parseFleetReserveField(id, entry); err != nil {
		return FleetAccountConfig{}, err
	}
	if cfg.ExecutiveModelFractionSoft, err = parseFleetFloatField(id, "executive_model_fraction_soft", entry); err != nil {
		return FleetAccountConfig{}, err
	}
	if cfg.ExecutiveModelFractionHard, err = parseFleetFloatField(id, "executive_model_fraction_hard", entry); err != nil {
		return FleetAccountConfig{}, err
	}
	return cfg, nil
}

// parseFleetFloatField reads a plain float64 field, tolerating TOML's
// int64 decode for a whole-number literal (e.g. `executive_model_fraction_hard = 0`).
func parseFleetFloatField(id, field string, entry map[string]interface{}) (*float64, error) {
	v, ok := entry[field]
	if !ok {
		return nil, nil
	}
	f, ok := tomlFloat(v)
	if !ok {
		return nil, &ConfigError{Field: fmt.Sprintf("fleet.accounts.%s.%s", id, field), Reason: "must be a number"}
	}
	return &f, nil
}

// parseFleetReserveField parses preserve_weekly_reserve and enforces the
// SAME [0.0,0.60] bound topology.ValidateReserve enforces (R-21.117/
// R-21.134(c)) -- see this file's CONTRACT DEVIATION header comment for
// why this package cannot call that function directly. A value outside
// the bound returns *ConfigError; the caller (Load) then rejects the
// whole load, so a rejected reload leaves the running values in place
// (hotreload.go's existing Reload error branch) -- functionally identical
// to topology.ValidateReserve's own "never silently accepted" guarantee.
func parseFleetReserveField(id string, entry map[string]interface{}) (*float64, error) {
	const field = "preserve_weekly_reserve"
	v, ok := entry[field]
	if !ok {
		return nil, nil
	}
	f, ok := tomlFloat(v)
	if !ok {
		return nil, &ConfigError{Field: fmt.Sprintf("fleet.accounts.%s.%s", id, field), Reason: "must be a number"}
	}
	if f < fleetReserveMin || f > fleetReserveMax {
		return nil, &ConfigError{
			Field:  fmt.Sprintf("fleet.accounts.%s.%s", id, field),
			Reason: fmt.Sprintf("%v is outside [%.2f,%.2f]", f, fleetReserveMin, fleetReserveMax),
		}
	}
	return &f, nil
}

// tomlFloat reads v as a float64, tolerating the numeric shapes a TOML
// decoder or a hand-built test tree may hand back (float64 from a real
// fractional literal, int64/int from a whole-number literal).
func tomlFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}
