package runtime

// Purpose: the one decoder of [conductor].deny_task_classes, the owner's
//
//	list of task classes this machine refuses to dispatch.
//
// Inputs: a loaded *Config (the daemon passes its HotReloader's Current()).
// Outputs: the listed class names, or a KindInvalidInput naming the key.
// Constraints: shape only. Whether a name is a real task class is the
//
//	conductor's table, checked by the daemon's deny store; this package
//	does not import the conductor. An absent key is the documented default
//	(no class denied); a nil config is not "absent", it is unreadable.
//
// SPORT: runtime/conductor-deny (ADD, P1-CAP-07).

import "github.com/acamarata/cascade/pkg/cascade"

// ConductorDenyKey is the dotted config key of the owner deny list.
const ConductorDenyKey = "conductor.deny_task_classes"

// ConductorDenyTaskClasses returns Extra["conductor"]["deny_task_classes"].
// An absent [conductor] table or key is the empty list. A [conductor] that
// is not a table, a value that is not an array, or an element that is not a
// non-empty string is KindInvalidInput naming the key. A nil cfg is
// KindUnavailable: there is no configuration to read, which is never the
// same as an empty list.
func ConductorDenyTaskClasses(cfg *Config) ([]string, error) {
	if cfg == nil {
		return nil, cascade.Newf(cascade.KindUnavailable, "%s: no configuration is loaded", ConductorDenyKey)
	}
	raw, ok := cfg.Extra["conductor"]
	if !ok || raw == nil {
		return []string{}, nil
	}
	section, ok := raw.(map[string]interface{})
	if !ok {
		return nil, cascade.Newf(cascade.KindInvalidInput, "%s: [conductor] must be a table", ConductorDenyKey)
	}
	value, ok := section["deny_task_classes"]
	if !ok {
		return []string{}, nil
	}
	items, ok := value.([]interface{})
	if !ok {
		return nil, cascade.Newf(cascade.KindInvalidInput, "%s must be an array of task class names, got %T", ConductorDenyKey, value)
	}
	out := make([]string, 0, len(items))
	for i, item := range items {
		s, isStr := item.(string)
		if !isStr || s == "" {
			return nil, cascade.Newf(cascade.KindInvalidInput, "%s[%d] must be a non-empty task class name, got %T", ConductorDenyKey, i, item)
		}
		out = append(out, s)
	}
	return out, nil
}
