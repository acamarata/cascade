package egressproxy

// Purpose: parse the cold, file-only [agents.egress.<driver-id>] tables out
// of runtime.Config.Extra into one exact allow list per driver.
//
// Constraints: only the closed driver ids, only the key `allow`, every
// entry through ParseDestination. Absent tables and empty lists deny all.
// Every refusal is KindInvalidInput and names the offending key.

import (
	"sort"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Config key path segments.
const (
	keyAgents = "agents"
	keyEgress = "egress"
	keyAllow  = "allow"
)

// ParseEgressAllowlists reads [agents.egress.<driver-id>] allow lists from
// a decoded config document's extra sections. Every driver in the closed
// set is present in the result; a driver without a table gets an empty
// list, which refuses every destination.
func ParseEgressAllowlists(extra map[string]any) (map[DriverID]DestinationAllowlist, error) {
	out := make(map[DriverID]DestinationAllowlist, len(knownDrivers))
	for _, id := range knownDrivers {
		out[id] = DestinationAllowlist{}
	}
	egress, err := egressTable(extra)
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(egress) {
		key := keyAgents + "." + keyEgress + "." + name
		id := DriverID(name)
		if !id.known() {
			return nil, keyError(key, "is not a known driver id")
		}
		list, err := driverAllowlist(key, egress[name])
		if err != nil {
			return nil, err
		}
		out[id] = list
	}
	return out, nil
}

// egressTable descends to agents.egress. A missing table is (nil, nil).
func egressTable(extra map[string]any) (map[string]any, error) {
	agentsRaw, ok := extra[keyAgents]
	if !ok {
		return nil, nil
	}
	agents, ok := agentsRaw.(map[string]any)
	if !ok {
		return nil, keyError(keyAgents, "must be a table")
	}
	egressRaw, ok := agents[keyEgress]
	if !ok {
		return nil, nil
	}
	egress, ok := egressRaw.(map[string]any)
	if !ok {
		return nil, keyError(keyAgents+"."+keyEgress, "must be a table")
	}
	return egress, nil
}

// driverAllowlist parses one driver table, whose only permitted key is
// `allow`.
func driverAllowlist(key string, raw any) (DestinationAllowlist, error) {
	table, ok := raw.(map[string]any)
	if !ok {
		return nil, keyError(key, "must be a table")
	}
	for _, k := range sortedKeys(table) {
		if k != keyAllow {
			return nil, keyError(key+"."+k, "is not a recognized key (only allow)")
		}
	}
	allowRaw, ok := table[keyAllow]
	if !ok {
		return DestinationAllowlist{}, nil
	}
	entries, ok := stringEntries(allowRaw)
	if !ok {
		return nil, keyError(key+"."+keyAllow, "must be an array of strings")
	}
	list := make(DestinationAllowlist, 0, len(entries))
	for i, e := range entries {
		d, err := ParseDestination(e)
		if err != nil {
			return nil, cascade.Wrapf(cascade.KindInvalidInput, err,
				"egressproxy: %s.%s[%d]", key, keyAllow, i)
		}
		list = append(list, d)
	}
	return list, nil
}

// stringEntries accepts a decoded TOML array ([]any of strings) or a
// []string built in code.
func stringEntries(raw any) ([]string, bool) {
	switch v := raw.(type) {
	case []string:
		return v, true
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

// keyError is the KindInvalidInput refusal naming key.
func keyError(key, problem string) error {
	return cascade.Newf(cascade.KindInvalidInput, "egressproxy: %s %s", key, problem)
}

// sortedKeys gives a deterministic walk, so the same bad document always
// names the same key.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
