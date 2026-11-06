package runtime

// Purpose: the hot [fleet.discovery].interval key: its default, its floor and
// the one rule both the read path (FleetDiscoveryInterval) and the write path
// (Validate, via validateFleetDiscovery) apply.
// Outputs: a time.Duration, or ErrFleetDiscoveryInterval wrapped with the raw value.
// Constraints: the value must be a TOML string time.ParseDuration accepts and
// at least 1h. A non-table [fleet] or [fleet.discovery] is the same error.
// The key is hot: "fleet" is never a cold section.

import (
	"fmt"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// FleetDiscoveryIntervalDefault applies when [fleet.discovery].interval is unset.
const FleetDiscoveryIntervalDefault = 6 * time.Hour

// FleetDiscoveryIntervalMin is the smallest interval the config accepts.
const FleetDiscoveryIntervalMin = time.Hour //nolint:revive // the name is fixed by the fleet discovery interval contract; Min is the floor, not minutes

// ErrFleetDiscoveryInterval is the refusal for a bad [fleet.discovery].interval.
var ErrFleetDiscoveryInterval = cascade.New(cascade.KindInvalidInput,
	"runtime: config: fleet.discovery.interval must be a duration string of at least 1h")

// FleetDiscoveryInterval reads [fleet.discovery].interval from cfg.Extra. A nil
// cfg or an unset key yields the 6h default.
func FleetDiscoveryInterval(cfg *Config) (time.Duration, error) {
	if cfg == nil {
		return FleetDiscoveryIntervalDefault, nil
	}
	return fleetDiscoveryFromTree(cfg.Extra)
}

// validateFleetDiscovery applies the interval rule to a decoded config tree.
// An unset key is valid.
func validateFleetDiscovery(tree map[string]interface{}) error {
	_, err := fleetDiscoveryFromTree(tree)
	return err
}

// fleetDiscoveryFromTree is the one shared rule: walk [fleet] then
// [fleet.discovery], and parse interval when present.
func fleetDiscoveryFromTree(tree map[string]interface{}) (time.Duration, error) {
	fleetRaw, ok := tree["fleet"]
	if !ok {
		return FleetDiscoveryIntervalDefault, nil
	}
	fleet, ok := fleetRaw.(map[string]interface{})
	if !ok {
		return 0, fleetDiscoveryErr(fleetRaw)
	}
	discRaw, ok := fleet["discovery"]
	if !ok {
		return FleetDiscoveryIntervalDefault, nil
	}
	disc, ok := discRaw.(map[string]interface{})
	if !ok {
		return 0, fleetDiscoveryErr(discRaw)
	}
	raw, ok := disc["interval"]
	if !ok {
		return FleetDiscoveryIntervalDefault, nil
	}
	return parseFleetDiscoveryInterval(raw)
}

// parseFleetDiscoveryInterval accepts only a duration string of at least 1h.
func parseFleetDiscoveryInterval(raw interface{}) (time.Duration, error) {
	s, ok := raw.(string)
	if !ok {
		return 0, fleetDiscoveryErr(raw)
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < FleetDiscoveryIntervalMin {
		return 0, fleetDiscoveryErr(raw)
	}
	return d, nil
}

// fleetDiscoveryErr wraps the sentinel with the offending raw value.
func fleetDiscoveryErr(raw interface{}) error {
	return cascade.Wrapf(cascade.KindInvalidInput, ErrFleetDiscoveryInterval,
		"runtime: config: fleet.discovery.interval %q", fmt.Sprint(raw))
}
