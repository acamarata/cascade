// Purpose: the one decoder of the operator's [agents.lanes.<driver>] and
// [agents.delegation] tables in Config.Extra.
// Inputs: the raw Config.Extra tree.
// Outputs: lane and delegation settings, or KindInvalidInput naming the key.
// Constraints: no environment, vault, clock or file access; a credential
// value never appears here (credential choice lives in [agents.auth.<harness>]).
// SPORT: internal.runtime.config.agents/ADD (P1-AGT-08).

package runtime

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// DefaultDelegationWallClock is the default [agents.delegation] wall_clock.
const DefaultDelegationWallClock = 30 * time.Minute

// MaxDelegationWallClock is the ceiling on [agents.delegation] max_wall_clock.
const MaxDelegationWallClock = 24 * time.Hour

// Lane postures accepted in [agents.lanes.<driver>] posture.
const (
	// AgentPostureProgrammatic declares a programmatic entitlement.
	AgentPostureProgrammatic = "programmatic"
	// AgentPostureInteractive declares an interactive-only entitlement.
	AgentPostureInteractive = "interactive"
)

// laneDriverName is the shape of a lane table name.
var laneDriverName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// AgentLaneConfig is one [agents.lanes.<driver>] table.
type AgentLaneConfig struct {
	// Driver is the table name, the harness id.
	Driver string
	// Enabled reports whether the lane is offered to the fleet.
	Enabled bool
	// Model is the lane model id; "" is the driver default.
	Model string
	// Effort is the lane effort; "" is the driver default.
	Effort string
	// Posture is AgentPostureProgrammatic or AgentPostureInteractive.
	Posture string
}

// ProgrammaticEntitlement reports whether the lane declares a programmatic
// entitlement.
func (l AgentLaneConfig) ProgrammaticEntitlement() bool {
	return l.Posture == AgentPostureProgrammatic
}

// DelegationConfig is the [agents.delegation] table.
type DelegationConfig struct {
	// WallClock is the default per-delegation wall clock.
	WallClock time.Duration
	// MaxWallClock caps a requested wall clock; at most MaxDelegationWallClock.
	MaxWallClock time.Duration
}

// AgentsConfig is the parsed lane and delegation configuration.
type AgentsConfig struct {
	// Lanes is sorted by Driver.
	Lanes []AgentLaneConfig
	// Delegation holds the delegation wall-clock settings.
	Delegation DelegationConfig
}

// LaneModel returns the model and effort configured for lane "agent:<driver>".
// An unknown or disabled lane yields "", "" (the driver default).
func (c AgentsConfig) LaneModel(lane string) (model, effort string) {
	driver, ok := strings.CutPrefix(lane, "agent:")
	if !ok {
		return "", ""
	}
	for _, l := range c.Lanes {
		if l.Driver == driver && l.Enabled {
			return l.Model, l.Effort
		}
	}
	return "", ""
}

// ParseAgentsConfig decodes the lanes through ParseAgentLanes and reads
// [agents.delegation]; absent sections yield defaults.
func ParseAgentsConfig(extra map[string]interface{}) (AgentsConfig, error) {
	out := AgentsConfig{Delegation: DelegationConfig{
		WallClock: DefaultDelegationWallClock, MaxWallClock: MaxDelegationWallClock,
	}}
	lanes, err := ParseAgentLanes(extra)
	if err != nil {
		return out, err
	}
	out.Lanes = lanes
	section, ok := extra["agents"]
	if !ok {
		return out, nil
	}
	agents, ok := section.(map[string]interface{})
	if !ok {
		return out, cascade.New(cascade.KindInvalidInput, "config: [agents] must be a table")
	}
	raw, ok := agents["delegation"]
	if !ok {
		return out, nil
	}
	table, ok := raw.(map[string]interface{})
	if !ok {
		return out, cascade.New(cascade.KindInvalidInput, "config: [agents.delegation] must be a table")
	}
	out.Delegation, err = parseDelegation(table, out.Delegation)
	return out, err
}

// ParseAgentLanes decodes every [agents.lanes.<driver>] table, sorted by
// driver. An absent [agents] or [agents.lanes] yields nil, nil.
func ParseAgentLanes(extra map[string]interface{}) ([]AgentLaneConfig, error) {
	section, ok := extra["agents"]
	if !ok {
		return nil, nil
	}
	agents, ok := section.(map[string]interface{})
	if !ok {
		return nil, cascade.New(cascade.KindInvalidInput, "config: [agents] must be a table")
	}
	raw, ok := agents["lanes"]
	if !ok {
		return nil, nil
	}
	lanes, ok := raw.(map[string]interface{})
	if !ok {
		return nil, cascade.New(cascade.KindInvalidInput, "config: [agents.lanes] must be a table")
	}
	names := make([]string, 0, len(lanes))
	for name := range lanes {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]AgentLaneConfig, 0, len(names))
	for _, name := range names {
		lane, err := parseAgentLane(name, lanes[name])
		if err != nil {
			return nil, err
		}
		out = append(out, lane)
	}
	return out, nil
}

// parseAgentLane decodes one lane table; unknown keys and wrong types are refused.
func parseAgentLane(name string, raw interface{}) (AgentLaneConfig, error) {
	where := "agents.lanes." + name
	if !laneDriverName.MatchString(name) {
		return AgentLaneConfig{}, cascade.Newf(cascade.KindInvalidInput, "config: %s: invalid lane name", where)
	}
	table, ok := raw.(map[string]interface{})
	if !ok {
		return AgentLaneConfig{}, cascade.Newf(cascade.KindInvalidInput, "config: %s must be a table", where)
	}
	lane := AgentLaneConfig{Driver: name, Enabled: true, Posture: AgentPostureInteractive}
	for key, v := range table {
		ok := false
		switch key {
		case "enabled":
			lane.Enabled, ok = v.(bool)
		case "model":
			lane.Model, ok = v.(string)
		case "effort":
			lane.Effort, ok = v.(string)
		case "posture":
			lane.Posture, ok = v.(string)
			ok = ok && (lane.Posture == AgentPostureProgrammatic || lane.Posture == AgentPostureInteractive)
		default:
			return lane, cascade.Newf(cascade.KindInvalidInput, "config: %s.%s: unknown key", where, key)
		}
		if !ok {
			return lane, cascade.Newf(cascade.KindInvalidInput, "config: %s.%s: invalid value", where, key)
		}
	}
	return lane, nil
}

// parseDelegation decodes [agents.delegation] over the defaults in d.
func parseDelegation(table map[string]interface{}, d DelegationConfig) (DelegationConfig, error) {
	for key, v := range table {
		var dst *time.Duration
		switch key {
		case "wall_clock":
			dst = &d.WallClock
		case "max_wall_clock":
			dst = &d.MaxWallClock
		default:
			return d, cascade.Newf(cascade.KindInvalidInput, "config: agents.delegation.%s: unknown key", key)
		}
		s, ok := v.(string)
		dur, err := time.ParseDuration(s)
		if !ok || err != nil || dur <= 0 {
			return d, cascade.Newf(cascade.KindInvalidInput, "config: agents.delegation.%s: want a positive duration", key)
		}
		*dst = dur
	}
	if d.MaxWallClock > MaxDelegationWallClock {
		return d, cascade.Newf(cascade.KindInvalidInput, "config: agents.delegation.max_wall_clock: exceeds %s", MaxDelegationWallClock)
	}
	if d.WallClock > d.MaxWallClock {
		return d, cascade.New(cascade.KindInvalidInput, "config: agents.delegation.wall_clock: exceeds max_wall_clock")
	}
	return d, nil
}
