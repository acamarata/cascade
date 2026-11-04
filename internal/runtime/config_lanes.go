package runtime

import (
	"sort"
	"strings"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// LaneReversalClass is the validation boundary implemented by topology.LaneClass.
// The type parameter avoids runtime -> topology -> rpc -> runtime imports.
type LaneReversalClass interface {
	~string
	Valid() bool
}

// LaneReversalPin is a file-only override for one lane. Instantiate C with
// topology.LaneClass; its closed enum supplies validation, without a copied list.
type LaneReversalPin[C LaneReversalClass] struct {
	LaneID         string
	LaneClass      C
	ReversalUntil  time.Time
	ReversalReason string
}

// ParseLaneReversalPins reads the result of Load, refusing read errors as well
// as malformed pins. Call ParseLaneReversalPins[topology.LaneClass](cfg, err)
// at the topology binding boundary. No I/O, clock reads or topology writes occur;
// ReversePromotion enforces the 30-day bound against first apply.
func ParseLaneReversalPins[C LaneReversalClass](cfg *Config, readErr error) ([]LaneReversalPin[C], error) {
	if readErr != nil {
		return nil, cascade.Wrap(cascade.KindInvalidInput, readErr, "fleet.lanes: cannot read configuration")
	}
	if cfg == nil {
		return nil, lanePinError("fleet.lanes", "no configuration is loaded")
	}
	for key, source := range cfg.sources {
		if (key == "fleet" || key == "fleet.lanes" || strings.HasPrefix(key, "fleet.lanes.")) &&
			source != SourceFile && source != SourceDefault {
			return nil, lanePinError(key, "fleet.lanes must be set in the config file only")
		}
	}
	fleet, err := lanePinTable(cfg.Extra, "fleet", "fleet.lanes")
	if err != nil {
		return nil, err
	}
	lanes, err := lanePinTable(fleet, "lanes", "fleet.lanes")
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(lanes))
	for id := range lanes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]LaneReversalPin[C], 0, len(ids))
	for _, id := range ids {
		entry, err := lanePinTable(lanes, id, "fleet.lanes."+id)
		if err != nil {
			return nil, err
		}
		pin, err := parseLanePin[C](cfg, id, entry)
		if err != nil {
			return nil, err
		}
		out = append(out, pin)
	}
	return out, nil
}

func lanePinTable(parent map[string]interface{}, name, key string) (map[string]interface{}, error) {
	v, found := parent[name]
	if !found {
		return nil, nil
	}
	table, ok := v.(map[string]interface{})
	if !ok {
		return nil, lanePinError(key, "must be a table")
	}
	return table, nil
}

func parseLanePin[C LaneReversalClass](cfg *Config, id string, entry map[string]interface{}) (LaneReversalPin[C], error) {
	var pin LaneReversalPin[C]
	key := "fleet.lanes." + id
	if strings.TrimSpace(id) == "" {
		return pin, lanePinError(key, "lane id must not be empty")
	}
	class, err := lanePinString(cfg, entry, key, "lane_class")
	if err != nil {
		return pin, err
	}
	if !C(class).Valid() {
		return pin, lanePinError(key+".lane_class", "unknown lane class")
	}
	until, err := lanePinString(cfg, entry, key, "reversal_until")
	if err != nil {
		return pin, err
	}
	parsed, err := time.Parse(time.RFC3339, until)
	if err != nil || !strings.HasSuffix(until, "Z") {
		return pin, lanePinError(key+".reversal_until", "must be a UTC RFC3339 string ending in Z")
	}
	reason, err := lanePinString(cfg, entry, key, "reversal_reason")
	if err != nil {
		return pin, err
	}
	return LaneReversalPin[C]{LaneID: id, LaneClass: C(class), ReversalUntil: parsed, ReversalReason: reason}, nil
}

func lanePinString(cfg *Config, entry map[string]interface{}, key, field string) (string, error) {
	key += "." + field
	s, ok := entry[field].(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", lanePinError(key, "must be a non-empty string")
	}
	if cfg.Source(key) != SourceFile {
		return "", lanePinError(key, "must be set in the config file only")
	}
	return s, nil
}

func lanePinError(key, message string) error {
	return cascade.Wrap(cascade.KindInvalidInput, cascade.ErrInvalidInput, key+": "+message)
}
